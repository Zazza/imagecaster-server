package dwpose

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanvasSize(t *testing.T) {
	tests := []struct {
		name           string
		w, h, res      int
		tw, th, cw, ch int
	}{
		{"portrait 832x1216 res 1024", 832, 1216, 1024, 1024, 1497, 1024, 1536},
		{"landscape 1216x832 res 1024", 1216, 832, 1024, 1497, 1024, 1536, 1024},
		{"landscape 1920x1080 res 512", 1920, 1080, 512, 910, 512, 960, 512},
		{"portrait 600x800 res 512", 600, 800, 512, 512, 683, 512, 704},
		{"square same size", 512, 512, 512, 512, 512, 512, 512},
		{"square downscale", 1000, 1000, 512, 512, 512, 512, 512},
		{"downscale already multiple of 64", 2048, 1024, 512, 1024, 512, 1024, 512},
		{"upscale already multiple of 64", 320, 640, 512, 512, 1024, 512, 1024},
		{"scale to multiples of 64", 640, 1280, 512, 512, 1024, 512, 1024},
		{"res not multiple of 64", 100, 100, 100, 100, 100, 128, 128},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tw, th, cw, ch := CanvasSize(tt.w, tt.h, tt.res)
			assert.Equal(t, []int{tt.tw, tt.th, tt.cw, tt.ch}, []int{tw, th, cw, ch})
		})
	}
}

func rawKeypoints() ([133][2]float64, [133]float64) {
	var kp [133][2]float64
	var sc [133]float64
	for i := range kp {
		kp[i] = [2]float64{float64(i * 10), float64(i*10 + 1)}
		sc[i] = 0.5 + float64(i)*0.001
	}
	return kp, sc
}

func TestAssembleReorder(t *testing.T) {
	kp, sc := rawKeypoints()
	p := Assemble(kp, sc)

	from := map[int]int{
		0: 0, 2: 6, 3: 8, 4: 10, 5: 5, 6: 7, 7: 9, 8: 12, 9: 14, 10: 16,
		11: 11, 12: 13, 13: 15, 14: 2, 15: 1, 16: 4, 17: 3,
	}
	for dst, src := range from {
		assert.Equal(t, Keypoint{X: kp[src][0], Y: kp[src][1], Score: sc[src]}, p[dst], "body index %d must come from raw %d", dst, src)
	}
	assert.Equal(t, Keypoint{X: 55, Y: 56, Score: 1}, p[1], "neck")
	for i := 18; i < 134; i++ {
		assert.Equal(t, Keypoint{X: kp[i-1][0], Y: kp[i-1][1], Score: sc[i-1]}, p[i], "index %d must be raw %d shifted by neck", i, i-1)
	}
}

func TestAssembleNeckScore(t *testing.T) {
	tests := []struct {
		name   string
		s5, s6 float64
		want   float64
	}{
		{"both visible", 0.9, 0.31, 1},
		{"left shoulder at threshold", 0.3, 0.9, 0},
		{"right shoulder low", 0.9, 0.2, 0},
		{"both low", 0.1, 0.1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kp, sc := rawKeypoints()
			kp[5] = [2]float64{100, 200}
			kp[6] = [2]float64{300, 401}
			sc[5], sc[6] = tt.s5, tt.s6
			p := Assemble(kp, sc)
			assert.Equal(t, Keypoint{X: 200, Y: 300.5, Score: tt.want}, p[1])
		})
	}
}

func pts(n int) []float64 { return make([]float64, 3*n) }

func setPt(f []float64, i int, x, y float64) {
	f[3*i], f[3*i+1], f[3*i+2] = x, y, 1
}

func TestFormatEmptyPerson(t *testing.T) {
	var p Person
	got := Format(p)
	assert.Equal(t, pts(18), got.Pose)
	assert.Nil(t, got.Face)
	assert.Nil(t, got.HandLeft)
	assert.Nil(t, got.HandRight)

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"face_keypoints_2d", "hand_left_keypoints_2d", "hand_right_keypoints_2d"} {
		v, ok := m[k]
		assert.True(t, ok, "%s must be present as null", k)
		assert.Nil(t, v, k)
	}
	assert.Len(t, m["pose_keypoints_2d"], 54)
}

func TestFormatBodyThreshold(t *testing.T) {
	var p Person
	p[0] = Keypoint{X: 10, Y: 11, Score: 0.3}
	p[1] = Keypoint{X: 20, Y: 21, Score: 0.29}
	p[17] = Keypoint{X: 30, Y: 31, Score: 0.95}
	got := Format(p)

	want := pts(18)
	setPt(want, 0, 10, 11)
	setPt(want, 17, 30, 31)
	assert.Equal(t, want, got.Pose)
	assert.Nil(t, got.Face)
}

func TestFormatFace(t *testing.T) {
	var p Person
	for i := 24; i < 92; i++ {
		p[i] = Keypoint{X: float64(i), Y: float64(i + 1000), Score: 0.8}
	}
	p[30].Score = 0.1
	p[14] = Keypoint{X: 500, Y: 501, Score: 0.9}
	p[15] = Keypoint{X: 600, Y: 601, Score: 0.2}
	got := Format(p)

	want := pts(70)
	for i := 24; i < 92; i++ {
		if i != 30 {
			setPt(want, i-24, float64(i), float64(i+1000))
		}
	}
	setPt(want, 68, 500, 501)
	require.Len(t, got.Face, 210)
	assert.Equal(t, want, got.Face)
}

func TestFormatFaceNullIgnoresBodyEyes(t *testing.T) {
	var p Person
	p[14] = Keypoint{X: 500, Y: 501, Score: 0.9}
	p[15] = Keypoint{X: 600, Y: 601, Score: 0.9}
	for i := 24; i < 92; i++ {
		p[i] = Keypoint{X: 1, Y: 1, Score: 0.29}
	}
	got := Format(p)
	assert.Nil(t, got.Face)
}

func TestFormatHands(t *testing.T) {
	var p Person
	p[92] = Keypoint{X: 1, Y: 2, Score: 0.5}
	p[112] = Keypoint{X: 3, Y: 4, Score: 0.3}
	p[133] = Keypoint{X: 5, Y: 6, Score: 0.7}
	got := Format(p)

	left := pts(21)
	setPt(left, 0, 1, 2)
	setPt(left, 20, 3, 4)
	right := pts(21)
	setPt(right, 20, 5, 6)
	assert.Equal(t, left, got.HandLeft)
	assert.Equal(t, right, got.HandRight)
	assert.Nil(t, got.Face)
}

func TestFormatOnlyRightHand(t *testing.T) {
	var p Person
	p[113] = Keypoint{X: 7, Y: 8, Score: 0.9}
	got := Format(p)
	assert.Nil(t, got.HandLeft)
	right := pts(21)
	setPt(right, 0, 7, 8)
	assert.Equal(t, right, got.HandRight)
}

type fakeEstimator struct {
	mu     sync.Mutex
	people []Person
	err    error
	sizes  [][2]int
	imgs   []*image.RGBA
}

func (f *fakeEstimator) Estimate(_ context.Context, img *image.RGBA) ([]Person, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := img.Bounds()
	f.sizes = append(f.sizes, [2]int{b.Dx(), b.Dy()})
	f.imgs = append(f.imgs, img)
	return f.people, f.err
}

func (f *fakeEstimator) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sizes)
}

func pngB64(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func solidB64(t *testing.T, w, h int, c color.Color) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return pngB64(t, img)
}

func grayB64(t *testing.T, w, h int) string {
	return solidB64(t, w, h, color.NRGBA{R: 90, G: 120, B: 150, A: 255})
}

func detectBody(t *testing.T, module string, res int, images ...string) []byte {
	t.Helper()
	req := map[string]any{
		"controlnet_module":       module,
		"controlnet_input_images": images,
	}
	if res != 0 {
		req["controlnet_processor_res"] = res
	}
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	return raw
}

func serve(h http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/controlnet/detect", bytes.NewReader(body))
	req.URL.Path = path
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type detectResp struct {
	Images []any  `json:"images"`
	Info   string `json:"info"`
	Poses  []struct {
		People       []json.RawMessage `json:"people"`
		Animals      []any             `json:"animals"`
		CanvasWidth  int               `json:"canvas_width"`
		CanvasHeight int               `json:"canvas_height"`
	} `json:"poses"`
}

func decodeResp(t *testing.T, rec *httptest.ResponseRecorder) detectResp {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var r detectResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &r), rec.Body.String())
	return r
}

func samplePerson() Person {
	var p Person
	for i := range p {
		p[i] = Keypoint{X: float64(i) + 0.5, Y: float64(i) + 100.25, Score: 0.9}
	}
	p[3].Score = 0.1
	for i := 92; i < 134; i++ {
		p[i].Score = 0
	}
	return p
}

func TestHandlerSuccessShape(t *testing.T) {
	p := samplePerson()
	est := &fakeEstimator{people: []Person{p}}
	rec := serve(NewHandler(est), "/controlnet/detect", detectBody(t, "dw_openpose_full", 1024, grayB64(t, 832, 1216)))
	r := decodeResp(t, rec)

	assert.Equal(t, "Success", r.Info)
	require.NotNil(t, r.Images)
	assert.Empty(t, r.Images)
	require.Len(t, r.Poses, 1)
	pose := r.Poses[0]
	assert.Equal(t, 1024, pose.CanvasWidth)
	assert.Equal(t, 1536, pose.CanvasHeight)
	require.NotNil(t, pose.Animals)
	assert.Empty(t, pose.Animals)
	require.Len(t, pose.People, 1)

	wantRaw, err := json.Marshal(Format(p))
	require.NoError(t, err)
	assert.JSONEq(t, string(wantRaw), string(pose.People[0]))

	var person map[string]any
	require.NoError(t, json.Unmarshal(pose.People[0], &person))
	assert.Len(t, person["face_keypoints_2d"], 210)
	assert.Len(t, person["pose_keypoints_2d"], 54)
	assert.Nil(t, person["hand_left_keypoints_2d"])
	assert.Nil(t, person["hand_right_keypoints_2d"])

	require.Equal(t, 1, est.calls())
	assert.Equal(t, [2]int{1024, 1536}, est.sizes[0])
}

func TestHandlerCanvasMatchesCanvasSize(t *testing.T) {
	tests := []struct {
		name string
		w, h int
		res  int
	}{
		{"portrait", 832, 1216, 1024},
		{"landscape", 300, 200, 512},
		{"square", 64, 64, 64},
		{"odd size", 333, 517, 384},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			est := &fakeEstimator{}
			rec := serve(NewHandler(est), "/controlnet/detect", detectBody(t, "dw_openpose_full", tt.res, grayB64(t, tt.w, tt.h)))
			r := decodeResp(t, rec)
			_, _, cw, ch := CanvasSize(tt.w, tt.h, tt.res)
			require.Len(t, r.Poses, 1)
			assert.Equal(t, cw, r.Poses[0].CanvasWidth)
			assert.Equal(t, ch, r.Poses[0].CanvasHeight)
			require.Equal(t, 1, est.calls())
			assert.Equal(t, [2]int{cw, ch}, est.sizes[0])
		})
	}
}

func TestHandlerDefaultProcessorRes(t *testing.T) {
	for _, res := range []int{0, -5} {
		t.Run(fmt.Sprintf("res=%d", res), func(t *testing.T) {
			est := &fakeEstimator{}
			body := detectBody(t, "dw_openpose_full", res, grayB64(t, 100, 200))
			r := decodeResp(t, serve(NewHandler(est), "/controlnet/detect", body))
			require.Len(t, r.Poses, 1)
			assert.Equal(t, 512, r.Poses[0].CanvasWidth)
			assert.Equal(t, 1024, r.Poses[0].CanvasHeight)
			require.Equal(t, 1, est.calls())
			assert.Equal(t, [2]int{512, 1024}, est.sizes[0])
		})
	}
}

func TestHandlerNoPeople(t *testing.T) {
	for name, people := range map[string][]Person{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			est := &fakeEstimator{people: people}
			rec := serve(NewHandler(est), "/controlnet/detect", detectBody(t, "dw_openpose_full", 64, grayB64(t, 64, 64)))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var m struct {
				Poses []map[string]json.RawMessage `json:"poses"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
			require.Len(t, m.Poses, 1)
			assert.JSONEq(t, `[]`, string(m.Poses[0]["people"]))
		})
	}
}

func TestHandlerMultiplePeople(t *testing.T) {
	a, b := samplePerson(), Person{}
	est := &fakeEstimator{people: []Person{a, b}}
	r := decodeResp(t, serve(NewHandler(est), "/controlnet/detect", detectBody(t, "dw_openpose_full", 64, grayB64(t, 64, 64))))
	require.Len(t, r.Poses, 1)
	require.Len(t, r.Poses[0].People, 2)
	wantB, err := json.Marshal(Format(b))
	require.NoError(t, err)
	assert.JSONEq(t, string(wantB), string(r.Poses[0].People[1]))
}

func TestHandlerMultipleImages(t *testing.T) {
	est := &fakeEstimator{people: []Person{samplePerson()}}
	body := detectBody(t, "dw_openpose_full", 128, grayB64(t, 64, 64), grayB64(t, 200, 100), grayB64(t, 100, 300))
	r := decodeResp(t, serve(NewHandler(est), "/controlnet/detect", body))
	require.Len(t, r.Poses, 3)
	require.Equal(t, 3, est.calls())
	for i, wh := range [][2]int{{64, 64}, {200, 100}, {100, 300}} {
		_, _, cw, ch := CanvasSize(wh[0], wh[1], 128)
		assert.Equal(t, cw, r.Poses[i].CanvasWidth, "pose %d", i)
		assert.Equal(t, ch, r.Poses[i].CanvasHeight, "pose %d", i)
		assert.Len(t, r.Poses[i].People, 1, "pose %d", i)
	}
}

func TestHandlerTransparentBecomesWhite(t *testing.T) {
	est := &fakeEstimator{}
	img := solidB64(t, 64, 64, color.NRGBA{R: 255, G: 0, B: 0, A: 0})
	decodeResp(t, serve(NewHandler(est), "/controlnet/detect", detectBody(t, "dw_openpose_full", 64, img)))
	require.Equal(t, 1, est.calls())
	got := est.imgs[0]
	b := got.Bounds()
	for _, pt := range []image.Point{{b.Min.X, b.Min.Y}, {b.Min.X + 32, b.Min.Y + 32}, {b.Max.X - 1, b.Max.Y - 1}} {
		assert.Equal(t, color.RGBA{R: 255, G: 255, B: 255, A: 255}, got.RGBAAt(pt.X, pt.Y), "pixel %v", pt)
	}
}

func TestHandlerOpaqueColorKept(t *testing.T) {
	est := &fakeEstimator{}
	c := color.NRGBA{R: 200, G: 40, B: 10, A: 255}
	decodeResp(t, serve(NewHandler(est), "/controlnet/detect", detectBody(t, "dw_openpose_full", 64, solidB64(t, 64, 64, c))))
	require.Equal(t, 1, est.calls())
	got := est.imgs[0]
	b := got.Bounds()
	assert.Equal(t, color.RGBA{R: 200, G: 40, B: 10, A: 255}, got.RGBAAt(b.Min.X+32, b.Min.Y+32))
}

func TestHandlerDataURL(t *testing.T) {
	est := &fakeEstimator{people: []Person{samplePerson()}}
	img := "data:image/png;base64," + grayB64(t, 64, 64)
	r := decodeResp(t, serve(NewHandler(est), "/controlnet/detect", detectBody(t, "dw_openpose_full", 64, img)))
	require.Len(t, r.Poses, 1)
	assert.Len(t, r.Poses[0].People, 1)
	assert.Equal(t, 1, est.calls())
}

func TestHandlerPathWithoutLeadingSlash(t *testing.T) {
	est := &fakeEstimator{people: []Person{samplePerson()}}
	r := decodeResp(t, serve(NewHandler(est), "controlnet/detect", detectBody(t, "dw_openpose_full", 64, grayB64(t, 64, 64))))
	require.Len(t, r.Poses, 1)
	assert.Len(t, r.Poses[0].People, 1)
}

func TestHandlerBadRequests(t *testing.T) {
	notImage := base64.StdEncoding.EncodeToString([]byte("definitely not an image"))
	tests := []struct {
		name string
		body func(t *testing.T) []byte
	}{
		{"other module", func(t *testing.T) []byte { return detectBody(t, "openpose_full", 64, grayB64(t, 64, 64)) }},
		{"empty module", func(t *testing.T) []byte { return detectBody(t, "", 64, grayB64(t, 64, 64)) }},
		{"no images", func(t *testing.T) []byte { return detectBody(t, "dw_openpose_full", 64) }},
		{"images field missing", func(t *testing.T) []byte { return []byte(`{"controlnet_module":"dw_openpose_full"}`) }},
		{"invalid base64", func(t *testing.T) []byte { return detectBody(t, "dw_openpose_full", 64, "%%%not-base64%%%") }},
		{"base64 of non-image", func(t *testing.T) []byte { return detectBody(t, "dw_openpose_full", 64, notImage) }},
		{"one good one broken", func(t *testing.T) []byte {
			return detectBody(t, "dw_openpose_full", 64, grayB64(t, 64, 64), notImage)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			est := &fakeEstimator{}
			rec := serve(NewHandler(est), "/controlnet/detect", tt.body(t))
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			if tt.name != "one good one broken" {
				assert.Equal(t, 0, est.calls())
			}
		})
	}
}

func TestHandlerEstimatorErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{"unavailable", ErrUnavailable, http.StatusServiceUnavailable},
		{"wrapped unavailable", fmt.Errorf("load onnxruntime from /x/libonnxruntime.so: %w", ErrUnavailable), http.StatusServiceUnavailable},
		{"other error", errors.New("inference failed"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			est := &fakeEstimator{err: tt.err}
			rec := serve(NewHandler(est), "/controlnet/detect", detectBody(t, "dw_openpose_full", 64, grayB64(t, 64, 64)))
			assert.Equal(t, tt.code, rec.Code, rec.Body.String())
			if tt.code == http.StatusServiceUnavailable {
				assert.Contains(t, rec.Body.String(), ErrUnavailable.Error())
			}
		})
	}
}

func TestONNXUnavailable(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"missing library", Config{LibPath: filepath.Join(t.TempDir(), "libonnxruntime-missing.so"), ModelsDir: t.TempDir()}},
		{"library is not a library", Config{LibPath: writeJunk(t), ModelsDir: t.TempDir()}},
		{"default library, no models", Config{ModelsDir: t.TempDir()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewONNX(tt.cfg)
			defer e.Close()
			_, err := e.Estimate(context.Background(), image.NewRGBA(image.Rect(0, 0, 64, 64)))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnavailable)

			rec := serve(NewHandler(e), "/controlnet/detect", detectBody(t, "dw_openpose_full", 64, grayB64(t, 64, 64)))
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
		})
	}
}

func writeJunk(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "libonnxruntime.so")
	require.NoError(t, os.WriteFile(path, []byte("not a shared library"), 0o644))
	return path
}
