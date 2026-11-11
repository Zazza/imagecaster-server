package sdcpp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pngB64(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 10), G: uint8(y * 10), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func decodeB64Image(s string) (image.Image, error) {
	if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i >= 0 {
		s = s[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	return img, err
}

func imageSize(t *testing.T, s string) (int, int) {
	t.Helper()
	img, err := decodeB64Image(s)
	require.NoError(t, err)
	b := img.Bounds()
	return b.Dx(), b.Dy()
}

type fakeJob struct {
	id        string
	batch     int
	cancelled bool
}

type fakeSD struct {
	t   *testing.T
	srv *httptest.Server

	mu           sync.Mutex
	bodies       []map[string]any
	jobs         map[string]*fakeJob
	cancels      []string
	upscales     []map[string]any
	unloads      int
	optionGets   int
	notReady     int
	final        string
	submitStatus int
	crash        bool
	loraGets     int
	loras        []map[string]any

	hold     atomic.Bool
	holdFrom atomic.Int32
}

func newFakeSD(t *testing.T) *fakeSD {
	t.Helper()
	f := &fakeSD{
		t:     t,
		jobs:  map[string]*fakeJob{},
		final: "completed",
		loras: []map[string]any{
			{"name": "detail", "path": "detail.safetensors"},
			{"name": "sub", "path": "styles/sub.safetensors"},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sdcpp/v1/img_gen", f.handleImgGen)
	mux.HandleFunc("GET /sdcpp/v1/jobs/{id}", f.handleJob)
	mux.HandleFunc("POST /sdcpp/v1/jobs/{id}/cancel", f.handleCancel)
	mux.HandleFunc("GET /sdapi/v1/loras", f.handleLoras)
	mux.HandleFunc("POST /sdcpp/v1/upscale", f.handleUpscale)
	mux.HandleFunc("POST /sdcpp/v1/models/unload", f.handleUnload)
	mux.HandleFunc("GET /sdapi/v1/options", f.handleOptions)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("fake sd-server: unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeSD) handleImgGen(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submitStatus != 0 {
		writeJSON(w, f.submitStatus, map[string]any{"error": map[string]any{"code": "model_not_loaded", "message": "no model loaded"}})
		return
	}
	f.bodies = append(f.bodies, body)
	batch := 1
	if v, ok := body["batch_count"].(float64); ok && v > 0 {
		batch = int(v)
	}
	id := fmt.Sprintf("job_%d", len(f.bodies))
	f.jobs[id] = &fakeJob{id: id, batch: batch}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id": id, "kind": "img_gen", "status": "queued", "created": time.Now().UnixMilli(), "poll_url": "/sdcpp/v1/jobs/" + id,
	})
}

func (f *fakeSD) handleJob(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	crash := f.crash
	job := f.jobs[r.PathValue("id")]
	final := f.final
	f.mu.Unlock()
	if crash {
		hj, ok := w.(http.Hijacker)
		if !ok {
			f.t.Errorf("fake sd-server: hijack unsupported")
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	f.mu.Lock()
	cancelled := job.cancelled
	f.mu.Unlock()
	resp := map[string]any{"id": job.id, "kind": "img_gen", "created": time.Now().UnixMilli(), "queue_position": 0}
	switch {
	case cancelled:
		resp["status"] = "cancelled"
		resp["result"] = nil
		resp["error"] = map[string]any{"code": "cancelled", "message": "job cancelled by client"}
	case f.hold.Load() || f.held(job.id):
		resp["status"] = "generating"
		resp["result"] = nil
		resp["error"] = nil
		resp["progress"] = map[string]any{"stage": "sampling", "detail": "", "pass": 1, "completed": 5, "total": 10, "unit": "steps", "stage_elapsed_ms": 10, "elapsed_ms": 20}
	case final == "failed":
		resp["status"] = "failed"
		resp["result"] = nil
		resp["error"] = map[string]any{"code": "generation_failed", "message": "generate_image returned empty results"}
	default:
		images := make([]map[string]any, job.batch)
		for i := range images {
			images[i] = map[string]any{"index": i, "b64_json": pngB64(f.t, 8, 8)}
		}
		resp["status"] = "completed"
		resp["result"] = map[string]any{"output_format": "png", "images": images}
		resp["error"] = nil
		resp["progress"] = map[string]any{"stage": "encoding", "detail": "image", "pass": 1, "completed": 1, "total": 1, "unit": "images", "stage_elapsed_ms": 1, "elapsed_ms": 30}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (f *fakeSD) held(id string) bool {
	from := f.holdFrom.Load()
	var n int32
	_, err := fmt.Sscanf(id, "job_%d", &n)
	return from > 0 && err == nil && n >= from
}

func (f *fakeSD) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, id)
	job := f.jobs[id]
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	job.cancelled = true
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "generating", "cancel_requested": true})
}

func (f *fakeSD) handleLoras(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loraGets++
	writeJSON(w, http.StatusOK, f.loras)
}

func (f *fakeSD) handleUpscale(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	f.mu.Lock()
	f.upscales = append(f.upscales, body)
	f.mu.Unlock()
	src, _ := body["image"].(string)
	img, err := decodeB64Image(src)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unreadable image"})
		return
	}
	b := img.Bounds()
	out := pngB64(f.t, b.Dx()*4, b.Dy()*4)
	writeJSON(w, http.StatusOK, map[string]any{
		"images":   []map[string]any{{"index": 0, "b64_json": out}},
		"upscaler": body["upscaler"], "scale": 4, "repeats": 1,
		"width": b.Dx() * 4, "height": b.Dy() * 4, "output_format": "png",
	})
}

func (f *fakeSD) handleUnload(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unloads++
	writeJSON(w, http.StatusOK, map[string]any{"unloaded": true})
}

func (f *fakeSD) handleOptions(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.optionGets++
	if f.notReady > 0 {
		f.notReady--
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "loading"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples_format": "png", "sd_model_checkpoint": ""})
}

func (f *fakeSD) imgGenBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...)
}

func (f *fakeSD) set(fn func(f *fakeSD)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeSD) get(fn func(f *fakeSD)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

type fakeRestarter struct {
	mu        sync.Mutex
	calls     [][]string
	onRestart func()
}

func (r *fakeRestarter) RestartWithArgs(args []string) error {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	hook := r.onRestart
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (r *fakeRestarter) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

type fakeMemory struct {
	stats MemoryStats
	calls atomic.Int32
}

func (m *fakeMemory) Memory() (MemoryStats, error) {
	m.calls.Add(1)
	return m.stats, nil
}

type testEnv struct {
	sd   *fakeSD
	rs   *fakeRestarter
	mem  *fakeMemory
	dirs modelDirs
	cfg  Config
	f    *Facade
}

var testBaseArgs = []string{"--listen-port", "7861", "--host", "127.0.0.1"}

func newEnv(t *testing.T, opts ...func(*Config)) *testEnv {
	t.Helper()
	sd := newFakeSD(t)
	d := newModelDirs(t)
	cfg := d.cfg
	cfg.BaseURL = sd.srv.URL
	cfg.BaseArgs = append([]string(nil), testBaseArgs...)
	cfg.PollInterval = 5 * time.Millisecond
	cfg.ReadyTimeout = 2 * time.Second
	touch(t, filepath.Join(cfg.UpscalerDir, "RealESRGAN_x4plus.pth"))
	touch(t, filepath.Join(cfg.LoraDir, "detail.safetensors"))
	touch(t, filepath.Join(cfg.LoraDir, "styles", "sub.safetensors"))
	for _, o := range opts {
		o(&cfg)
	}
	rs := &fakeRestarter{}
	mem := &fakeMemory{stats: MemoryStats{
		RAM:  Pool{Free: 1e9, Used: 2e9, Total: 3e9},
		CUDA: &CUDA{System: Pool{Free: 4e9, Used: 5e9, Total: 9e9}},
	}}
	return &testEnv{sd: sd, rs: rs, mem: mem, dirs: d, cfg: cfg, f: New(cfg, rs, mem)}
}

func newRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func (e *testEnv) start(t *testing.T, method, path string, body any) <-chan *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest(t, method, path, body)
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		e.f.ServeHTTP(rec, req)
		out <- rec
	}()
	return out
}

func wait(t *testing.T, ch <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-ch:
		return rec
	case <-time.After(5 * time.Second):
		t.Fatal("facade did not respond within 5s")
		return nil
	}
}

func (e *testEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return wait(t, e.start(t, method, path, body))
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), "body: %s", rec.Body.String())
	return m
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var l []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &l), "body: %s", rec.Body.String())
	return l
}

func sub(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	require.True(t, ok, "%s must be an object in %v", key, m)
	return v
}

func images(t *testing.T, m map[string]any) []string {
	t.Helper()
	raw, ok := m["images"].([]any)
	require.True(t, ok, "images must be an array: %v", m["images"])
	out := make([]string, len(raw))
	for i, v := range raw {
		s, ok := v.(string)
		require.True(t, ok, "image %d must be a base64 string", i)
		out[i] = s
	}
	return out
}

func infoMap(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	s, ok := m["info"].(string)
	require.True(t, ok, "info must be a JSON string: %v", m["info"])
	var info map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &info), "info: %s", s)
	return info
}

func (e *testEnv) kleinOptions() map[string]any {
	return map[string]any{
		"sd_model_checkpoint":      "flux-2-klein-4b.safetensors",
		"forge_additional_modules": []string{e.dirs.te, e.dirs.vae},
	}
}

func TestOptionsPost_RestartsWithBaseAndModelArgs(t *testing.T) {
	cases := []struct {
		name string
		body func(e *testEnv) map[string]any
		want func(e *testEnv) map[string]string
	}{
		{
			"sdxl",
			func(e *testEnv) map[string]any { return map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors"} },
			func(e *testEnv) map[string]string {
				return map[string]string{"--model": filepath.Join(e.cfg.ModelsDir, "sdxl_base.safetensors")}
			},
		},
		{
			"klein",
			func(e *testEnv) map[string]any { return e.kleinOptions() },
			func(e *testEnv) map[string]string {
				return map[string]string{
					"--diffusion-model": filepath.Join(e.cfg.ModelsDir, "flux-2-klein-4b.safetensors"),
					"--llm":             e.dirs.te,
					"--vae":             e.dirs.vae,
				}
			},
		},
		{
			"moss",
			func(e *testEnv) map[string]any {
				return map[string]any{"sd_model_checkpoint": "moss_v1.safetensors", "forge_additional_modules": []string{e.dirs.te, e.dirs.vae}}
			},
			func(e *testEnv) map[string]string {
				return map[string]string{
					"--diffusion-model": filepath.Join(e.cfg.ModelsDir, "moss_v1.safetensors"),
					"--llm":             e.dirs.te,
					"--vae":             e.dirs.vae,
					"--tokenizer":       filepath.Join(e.cfg.TokenizerDir, "moss.tokenizer.json"),
				}
			},
		},
		{
			"z-image via alias",
			func(e *testEnv) map[string]any {
				return map[string]any{"sd_model_checkpoint": "z-image-turbo-aio.safetensors"}
			},
			func(e *testEnv) map[string]string {
				return map[string]string{"--diffusion-model": "/m/z.gguf", "--llm": "/m/q.gguf", "--vae": "/m/ae.safetensors"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, func(c *Config) {
				c.Aliases = map[string][]string{"z-image-turbo-aio.safetensors": {"--diffusion-model", "/m/z.gguf", "--llm", "/m/q.gguf", "--vae", "/m/ae.safetensors"}}
			})
			rec := e.do(t, http.MethodPost, "/sdapi/v1/options", tc.body(e))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "null", strings.TrimSpace(rec.Body.String()))
			calls := e.rs.Calls()
			require.Len(t, calls, 1)
			args := calls[0]
			require.GreaterOrEqual(t, len(args), len(testBaseArgs))
			assert.Equal(t, testBaseArgs, args[:len(testBaseArgs)])
			assert.Equal(t, tc.want(e), flagValues(t, args[len(testBaseArgs):]))
		})
	}
}

func TestOptionsPost_RestartOnlyWhenArgsChange(t *testing.T) {
	e := newEnv(t)
	steps := []struct {
		name      string
		body      map[string]any
		wantCalls int
	}{
		{"first model", map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors"}, 1},
		{"same model again", map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors"}, 1},
		{"same model with hash", map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors [31e35c80fc]"}, 1},
		{"reference flags only", map[string]any{"klein_do_reference": true, "moss_do_reference": "Disable"}, 1},
		{"other model", map[string]any{"sd_model_checkpoint": "flux1-dev.gguf"}, 2},
		{"klein", e.kleinOptions(), 3},
		{"klein same modules", e.kleinOptions(), 3},
		{"klein other vae", map[string]any{
			"sd_model_checkpoint":      "flux-2-klein-4b.safetensors",
			"forge_additional_modules": []string{e.dirs.te, touch(t, filepath.Join(e.cfg.VAEDir, "ae.safetensors"))},
		}, 4},
	}
	for _, s := range steps {
		rec := e.do(t, http.MethodPost, "/sdapi/v1/options", s.body)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", s.name, rec.Body.String())
		assert.Len(t, e.rs.Calls(), s.wantCalls, s.name)
	}
}

func TestOptionsPost_ReferenceFlagsWithoutModelNoRestart(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"klein_do_reference": true, "moss_do_reference": "Disable"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, e.rs.Calls())
}

func TestOptionsPost_ModelArgsError400(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{"z-image without alias", map[string]any{"sd_model_checkpoint": "z-image-turbo-aio.safetensors"}},
		{"klein without modules", map[string]any{"sd_model_checkpoint": "flux-2-klein-4b.safetensors"}},
		{"moss without modules", map[string]any{"sd_model_checkpoint": "moss_v1.safetensors"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(t, http.MethodPost, "/sdapi/v1/options", tc.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			m := decodeMap(t, rec)
			msg, _ := m["error"].(string)
			assert.NotEmpty(t, msg)
			assert.Empty(t, e.rs.Calls())
		})
	}
}

func TestOptionsPost_WaitsForReadyAfterRestart(t *testing.T) {
	e := newEnv(t)
	var before int
	e.rs.onRestart = func() {
		e.sd.set(func(f *fakeSD) {
			before = f.optionGets
			f.notReady = 3
		})
	}
	rec := e.do(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var gets, left int
	e.sd.get(func(f *fakeSD) { gets, left = f.optionGets, f.notReady })
	assert.Equal(t, 0, left)
	assert.GreaterOrEqual(t, gets-before, 4)
}

func TestOptionsPost_ConflictWhileGenerating(t *testing.T) {
	e := newEnv(t)
	e.sd.hold.Store(true)
	gen := e.start(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "a cat", "seed": 1})
	require.True(t, waitFor(t, 2*time.Second, func() bool { return len(e.sd.imgGenBodies()) == 1 }))

	rec := e.do(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"sd_model_checkpoint": "flux1-dev.gguf"})
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Empty(t, e.rs.Calls())

	e.sd.hold.Store(false)
	res := wait(t, gen)
	assert.Equal(t, http.StatusOK, res.Code, res.Body.String())
}

func TestOptionsGet_ReturnsWritten(t *testing.T) {
	e := newEnv(t)
	body := e.kleinOptions()
	body["sd_vae"] = "flux2-vae.safetensors"
	body["klein_do_reference"] = true
	body["moss_do_reference"] = "Disable"
	rec := e.do(t, http.MethodPost, "/sdapi/v1/options", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := decodeMap(t, e.do(t, http.MethodGet, "/sdapi/v1/options", nil))
	assert.Equal(t, "flux-2-klein-4b.safetensors", got["sd_model_checkpoint"])
	assert.Equal(t, []any{e.dirs.te, e.dirs.vae}, got["forge_additional_modules"])
	assert.Equal(t, "flux2-vae.safetensors", got["sd_vae"])
	assert.Equal(t, true, got["klein_do_reference"])
	assert.Equal(t, "Disable", got["moss_do_reference"])

	rec = e.do(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"klein_do_reference": false})
	require.Equal(t, http.StatusOK, rec.Code)
	got = decodeMap(t, e.do(t, http.MethodGet, "/sdapi/v1/options", nil))
	assert.Equal(t, false, got["klein_do_reference"])
	assert.Equal(t, "flux-2-klein-4b.safetensors", got["sd_model_checkpoint"])
}

func TestOptionsGet_Defaults(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodGet, "/sdapi/v1/options", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	got := decodeMap(t, rec)
	for _, k := range []string{"sd_model_checkpoint", "forge_additional_modules", "klein_do_reference", "moss_do_reference", "sd_vae"} {
		assert.Contains(t, got, k)
	}
	assert.Equal(t, "Automatic", got["sd_vae"])
	assert.Equal(t, false, got["klein_do_reference"])
}

func TestTxt2Img_BasicMapping(t *testing.T) {
	e := newEnv(t)
	req := map[string]any{
		"prompt":          "a cat on a chair",
		"negative_prompt": "blurry",
		"width":           512,
		"height":          768,
		"steps":           20,
		"cfg_scale":       6.5,
		"seed":            42,
		"sampler_name":    "DPM++ 2M",
		"scheduler":       "Karras",
	}
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	bodies := e.sd.imgGenBodies()
	require.Len(t, bodies, 1)
	b := bodies[0]
	assert.Equal(t, "a cat on a chair", b["prompt"])
	assert.Equal(t, "blurry", b["negative_prompt"])
	assert.EqualValues(t, 512, b["width"])
	assert.EqualValues(t, 768, b["height"])
	assert.EqualValues(t, 42, b["seed"])
	sp := sub(t, b, "sample_params")
	assert.Equal(t, "dpm++2m", sp["sample_method"])
	assert.Equal(t, "karras", sp["scheduler"])
	assert.EqualValues(t, 20, sp["sample_steps"])
	assert.EqualValues(t, 6.5, sub(t, sp, "guidance")["txt_cfg"])

	resp := decodeMap(t, rec)
	imgs := images(t, resp)
	require.Len(t, imgs, 1)
	w, h := imageSize(t, imgs[0])
	assert.Equal(t, 8, w)
	assert.Equal(t, 8, h)
	params := sub(t, resp, "parameters")
	assert.Equal(t, "a cat on a chair", params["prompt"])
	assert.Equal(t, "DPM++ 2M", params["sampler_name"])
	assert.EqualValues(t, 42, infoMap(t, resp)["seed"])
}

func TestTxt2Img_SamplerSchedulerTable(t *testing.T) {
	cases := []struct {
		sampler, scheduler     string
		wantSampler, wantSched string
	}{
		{"Euler", "Simple", "euler", "simple"},
		{"Euler a", "Karras", "euler_a", "karras"},
		{"DPM++ 2M", "Automatic", "dpm++2m", ""},
	}
	for _, tc := range cases {
		t.Run(tc.sampler+"/"+tc.scheduler, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p", "sampler_name": tc.sampler, "scheduler": tc.scheduler})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			bodies := e.sd.imgGenBodies()
			require.Len(t, bodies, 1)
			sp := sub(t, bodies[0], "sample_params")
			assert.Equal(t, tc.wantSampler, sp["sample_method"])
			if tc.wantSched == "" {
				v, present := sp["scheduler"]
				assert.True(t, !present || v == "" || v == nil, "Automatic must not force a scheduler, got %v", v)
			} else {
				assert.Equal(t, tc.wantSched, sp["scheduler"])
			}
		})
	}
}

func TestTxt2Img_LoraFromPrompt(t *testing.T) {
	cases := []struct {
		name      string
		prompt    string
		wantLora  []map[string]any
		wantParts []string
	}{
		{
			"two loras", "a cat <lora:detail:0.8> on a chair <lora:sub:1.2>",
			[]map[string]any{{"path": "detail.safetensors", "multiplier": 0.8}, {"path": "styles/sub.safetensors", "multiplier": 1.2}},
			[]string{"a cat", "on a chair"},
		},
		{
			"only lora tag", "<lora:detail:1>",
			[]map[string]any{{"path": "detail.safetensors", "multiplier": 1.0}},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": tc.prompt, "seed": 1})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			bodies := e.sd.imgGenBodies()
			require.Len(t, bodies, 1)
			prompt, _ := bodies[0]["prompt"].(string)
			assert.NotContains(t, prompt, "<lora")
			for _, part := range tc.wantParts {
				assert.Contains(t, prompt, part)
			}
			if tc.wantParts == nil {
				assert.Empty(t, strings.TrimSpace(prompt))
			}
			raw, ok := bodies[0]["lora"].([]any)
			require.True(t, ok, "lora must be an array: %v", bodies[0]["lora"])
			got := make([]map[string]any, 0, len(raw))
			for _, r := range raw {
				m := r.(map[string]any)
				got = append(got, map[string]any{"path": m["path"], "multiplier": m["multiplier"]})
			}
			assert.ElementsMatch(t, tc.wantLora, got)
			assert.Equal(t, tc.prompt, sub(t, decodeMap(t, rec), "parameters")["prompt"])
		})
	}
}

func TestGeneration_BadRequest400(t *testing.T) {
	cases := []struct {
		name string
		path string
		body map[string]any
	}{
		{"unknown sampler", "/sdapi/v1/txt2img", map[string]any{"prompt": "a cat", "sampler_name": "Definitely Not A Sampler"}},
		{"unknown scheduler", "/sdapi/v1/txt2img", map[string]any{"prompt": "a cat", "scheduler": "Definitely Not A Scheduler"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.path == "/sdapi/v1/img2img" {
				tc.body["init_images"] = []string{pngB64(t, 8, 8)}
			}
			rec := e.do(t, http.MethodPost, tc.path, tc.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Empty(t, e.sd.imgGenBodies())
		})
	}
}

func TestTxt2Img_BatchAndIterations(t *testing.T) {
	cases := []struct {
		name       string
		req        map[string]any
		wantSeeds  []float64
		wantBatch  float64
		wantImages int
	}{
		{"batch only", map[string]any{"prompt": "p", "seed": 7, "batch_size": 3}, []float64{7}, 3, 3},
		{"n_iter with batch", map[string]any{"prompt": "p", "seed": 42, "batch_size": 2, "n_iter": 3}, []float64{42, 44, 46}, 2, 6},
		{"n_iter random seed", map[string]any{"prompt": "p", "seed": -1, "batch_size": 1, "n_iter": 2}, []float64{-1, -1}, 1, 2},
		{"seed missing", map[string]any{"prompt": "p", "batch_size": 1}, []float64{-1}, 1, 1},
		{"random seed with batch", map[string]any{"prompt": "p", "seed": -1, "batch_size": 2, "n_iter": 2}, []float64{-1, -1}, 2, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", tc.req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			bodies := e.sd.imgGenBodies()
			require.Len(t, bodies, len(tc.wantSeeds))
			random := tc.wantSeeds[0] < 0
			first, _ := bodies[0]["seed"].(float64)
			for i, b := range bodies {
				if random {
					assert.GreaterOrEqual(t, first, 0.0, "random seed must be chosen by the facade")
					assert.EqualValues(t, first+float64(i)*tc.wantBatch, b["seed"], "job %d seed", i)
				} else {
					assert.EqualValues(t, tc.wantSeeds[i], b["seed"], "job %d seed", i)
				}
				assert.EqualValues(t, tc.wantBatch, b["batch_count"], "job %d batch_count", i)
			}
			resp := decodeMap(t, rec)
			assert.Len(t, images(t, resp), tc.wantImages)
			if random {
				assert.EqualValues(t, first, infoMap(t, resp)["seed"], "info seed is the real seed")
			}
		})
	}
}

func TestTxt2Img_EmptyPrompt(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "", "seed": 3})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	bodies := e.sd.imgGenBodies()
	require.Len(t, bodies, 1)
	assert.Equal(t, "", bodies[0]["prompt"])
	assert.Len(t, images(t, decodeMap(t, rec)), 1)
}

func TestTxt2Img_Hires(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{
		"prompt":                "p",
		"seed":                  5,
		"steps":                 20,
		"enable_hr":             true,
		"hr_upscaler":           "Latent",
		"hr_scale":              2,
		"hr_resize_x":           0,
		"hr_resize_y":           0,
		"hr_second_pass_steps":  12,
		"hr_denoising_strength": 0.45,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	bodies := e.sd.imgGenBodies()
	require.Len(t, bodies, 1)
	hr := sub(t, bodies[0], "hires")
	assert.Equal(t, true, hr["enabled"])
	assert.Equal(t, "Latent", hr["upscaler"])
	assert.EqualValues(t, 2, hr["scale"])
	assert.EqualValues(t, 12, hr["steps"])
	assert.EqualValues(t, 0.45, hr["denoising_strength"])
	assert.EqualValues(t, 0, hr["target_width"])
	assert.EqualValues(t, 0, hr["target_height"])
}

func TestTxt2Img_HiresTargetSize(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{
		"prompt": "p", "enable_hr": true, "hr_upscaler": "RealESRGAN_x4plus",
		"hr_resize_x": 1024, "hr_resize_y": 1536, "hr_second_pass_steps": 8, "hr_denoising_strength": 0.3,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	hr := sub(t, e.sd.imgGenBodies()[0], "hires")
	assert.Equal(t, "RealESRGAN_x4plus", hr["upscaler"])
	assert.EqualValues(t, 1024, hr["target_width"])
	assert.EqualValues(t, 1536, hr["target_height"])
}

func TestTxt2Img_NoHiresWhenDisabled(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p", "enable_hr": false, "hr_second_pass_steps": 12})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	b := e.sd.imgGenBodies()[0]
	if hr, ok := b["hires"].(map[string]any); ok {
		assert.NotEqual(t, true, hr["enabled"])
	}
}

func TestImg2Img_Mapping(t *testing.T) {
	cases := []struct {
		name         string
		kleinRef     bool
		strength     any
		wantStrength float64
	}{
		{"strength from denoising", false, 0.4, 0.4},
		{"default strength", false, nil, 0.75},
		{"klein reference", true, 0.6, 0.6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"klein_do_reference": tc.kleinRef})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			init := pngB64(t, 8, 8)
			req := map[string]any{"prompt": "edit", "seed": 9, "init_images": []string{init}}
			if tc.strength != nil {
				req["denoising_strength"] = tc.strength
			}
			rec = e.do(t, http.MethodPost, "/sdapi/v1/img2img", req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			bodies := e.sd.imgGenBodies()
			require.Len(t, bodies, 1)
			b := bodies[0]
			assert.Equal(t, init, b["init_image"])
			assert.EqualValues(t, tc.wantStrength, b["strength"])
			if tc.kleinRef {
				assert.Equal(t, []any{init}, b["ref_images"])
			} else {
				ref, present := b["ref_images"]
				if present && ref != nil {
					assert.Empty(t, ref)
				}
			}
			resp := decodeMap(t, rec)
			assert.Len(t, images(t, resp), 1)
			assert.EqualValues(t, 9, infoMap(t, resp)["seed"])
		})
	}
}

func TestImg2Img_MossReference(t *testing.T) {
	cases := []struct {
		name     string
		moss     string
		wantCode int
	}{
		{"disable allowed", "Disable", http.StatusOK},
		{"empty allowed", "", http.StatusOK},
		{"edit rejected", "Edit", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"moss_do_reference": tc.moss})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			rec = e.do(t, http.MethodPost, "/sdapi/v1/img2img", map[string]any{"prompt": "edit", "init_images": []string{pngB64(t, 8, 8)}})
			require.Equal(t, tc.wantCode, rec.Code, rec.Body.String())
			if tc.wantCode == http.StatusBadRequest {
				assert.Contains(t, rec.Body.String(), "Klein")
				assert.Empty(t, e.sd.imgGenBodies())
			}
		})
	}
}

func TestGeneration_JobFailed500(t *testing.T) {
	for _, path := range []string{"/sdapi/v1/txt2img", "/sdapi/v1/img2img"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t)
			e.sd.set(func(f *fakeSD) { f.final = "failed" })
			rec := e.do(t, http.MethodPost, path, map[string]any{"prompt": "p", "init_images": []string{pngB64(t, 8, 8)}})
			require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			msg, _ := decodeMap(t, rec)["error"].(string)
			assert.NotEmpty(t, msg)
		})
	}
}

func TestGeneration_ModelNotLoaded503(t *testing.T) {
	e := newEnv(t)
	e.sd.set(func(f *fakeSD) { f.submitStatus = http.StatusServiceUnavailable })
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p"})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

func TestGeneration_ServerCrash502(t *testing.T) {
	e := newEnv(t)
	e.sd.hold.Store(true)
	gen := e.start(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p"})
	require.True(t, waitFor(t, 2*time.Second, func() bool { return len(e.sd.imgGenBodies()) == 1 }))
	e.sd.set(func(f *fakeSD) { f.crash = true })
	rec := wait(t, gen)
	assert.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
}

func TestGeneration_ServerDown502(t *testing.T) {
	e := newEnv(t)
	e.sd.srv.Close()
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p"})
	assert.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
}

func progressOf(t *testing.T, e *testEnv) (float64, float64) {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/sdapi/v1/progress", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := decodeMap(t, rec)
	p, _ := m["progress"].(float64)
	st := sub(t, m, "state")
	jc, _ := st["job_count"].(float64)
	return p, jc
}

func TestProgress(t *testing.T) {
	e := newEnv(t)
	p, jc := progressOf(t, e)
	assert.Zero(t, p)
	assert.Zero(t, jc)

	e.sd.hold.Store(true)
	gen := e.start(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p", "steps": 10})
	var gotP, gotJC float64
	ok := waitFor(t, 2*time.Second, func() bool {
		gotP, gotJC = progressOf(t, e)
		return gotP > 0
	})
	require.True(t, ok, "progress never became > 0 while job generating")
	assert.InDelta(t, 0.5, gotP, 0.001)
	assert.GreaterOrEqual(t, gotJC, 1.0)

	rec := e.do(t, http.MethodGet, "/sdapi/v1/progress", nil)
	m := decodeMap(t, rec)
	assert.Contains(t, m, "current_image")
	assert.Contains(t, m, "eta_relative")

	e.sd.hold.Store(false)
	res := wait(t, gen)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	p, jc = progressOf(t, e)
	assert.Zero(t, p)
	assert.Zero(t, jc)
}

func TestProgress_JobCountDuringIterations(t *testing.T) {
	e := newEnv(t)
	e.sd.hold.Store(true)
	gen := e.start(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p", "n_iter": 3})
	require.True(t, waitFor(t, 2*time.Second, func() bool { return len(e.sd.imgGenBodies()) == 1 }))
	require.True(t, waitFor(t, 2*time.Second, func() bool { _, jc := progressOf(t, e); return jc >= 1 }))
	e.sd.hold.Store(false)
	res := wait(t, gen)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	_, jc := progressOf(t, e)
	assert.Zero(t, jc)
}

func TestInterrupt(t *testing.T) {
	for _, path := range []string{"/sdapi/v1/txt2img", "/sdapi/v1/img2img"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t)
			e.sd.hold.Store(true)
			gen := e.start(t, http.MethodPost, path, map[string]any{"prompt": "p", "n_iter": 3, "init_images": []string{pngB64(t, 8, 8)}})
			require.True(t, waitFor(t, 2*time.Second, func() bool { return len(e.sd.imgGenBodies()) == 1 }))

			rec := e.do(t, http.MethodPost, "/sdapi/v1/interrupt", nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			res := wait(t, gen)
			require.Equal(t, http.StatusOK, res.Code, res.Body.String())
			assert.Empty(t, images(t, decodeMap(t, res)))

			var cancels []string
			e.sd.get(func(f *fakeSD) { cancels = append(cancels, f.cancels...) })
			assert.Equal(t, []string{"job_1"}, cancels)
			time.Sleep(30 * time.Millisecond)
			assert.Len(t, e.sd.imgGenBodies(), 1, "remaining n_iter jobs must not start")
		})
	}
}

func TestInterrupt_Idle(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/interrupt", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var cancels int
	e.sd.get(func(f *fakeSD) { cancels = len(f.cancels) })
	assert.Zero(t, cancels)
}

func TestInterrupt_DoesNotAffectNextGeneration(t *testing.T) {
	e := newEnv(t)
	e.sd.hold.Store(true)
	gen := e.start(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p"})
	require.True(t, waitFor(t, 2*time.Second, func() bool { return len(e.sd.imgGenBodies()) == 1 }))
	require.Equal(t, http.StatusOK, e.do(t, http.MethodPost, "/sdapi/v1/interrupt", nil).Code)
	require.Equal(t, http.StatusOK, wait(t, gen).Code)

	e.sd.hold.Store(false)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p", "n_iter": 2})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, images(t, decodeMap(t, rec)), 2)
}

func TestSDModels(t *testing.T) {
	e := newEnv(t, func(c *Config) {
		c.Aliases = map[string][]string{"z-image-turbo.safetensors": {"--diffusion-model", "/m/z.gguf"}}
	})
	touch(t, filepath.Join(e.cfg.ModelsDir, "readme.txt"))
	rec := e.do(t, http.MethodGet, "/sdapi/v1/sd-models", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := decodeList(t, rec)

	byTitle := map[string]map[string]any{}
	for _, m := range list {
		title, _ := m["title"].(string)
		byTitle[title] = m
	}
	for _, name := range []string{"sdxl_base.safetensors", "flux1-dev.gguf", "old_sd15.ckpt", "flux-2-klein-4b.safetensors"} {
		m, ok := byTitle[name]
		require.True(t, ok, "%s must be listed: %v", name, list)
		assert.Equal(t, strings.TrimSuffix(name, filepath.Ext(name)), m["model_name"])
		assert.Equal(t, filepath.Join(e.cfg.ModelsDir, name), m["filename"])
		assert.Contains(t, m, "hash")
		assert.Nil(t, m["hash"])
	}
	assert.NotContains(t, byTitle, "readme.txt")
	assert.Contains(t, byTitle, "z-image-turbo.safetensors")
}

func TestSDVae(t *testing.T) {
	e := newEnv(t)
	touch(t, filepath.Join(e.cfg.VAEDir, "ae.safetensors"))
	rec := e.do(t, http.MethodGet, "/sdapi/v1/sd-vae", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := map[string]any{}
	for _, m := range decodeList(t, rec) {
		got[m["model_name"].(string)] = m["filename"]
	}
	assert.Equal(t, map[string]any{
		"flux2-vae.safetensors": filepath.Join(e.cfg.VAEDir, "flux2-vae.safetensors"),
		"ae.safetensors":        filepath.Join(e.cfg.VAEDir, "ae.safetensors"),
	}, got)
}

func TestLoras(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodGet, "/sdapi/v1/loras", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := decodeList(t, rec)
	require.Len(t, list, 2)
	got := map[string]map[string]any{}
	for _, m := range list {
		got[m["name"].(string)] = m
	}
	assert.Equal(t, "detail", got["detail"]["alias"])
	assert.Equal(t, "detail.safetensors", got["detail"]["path"])
	assert.Equal(t, "sub", got["sub"]["alias"])
	assert.Equal(t, "styles/sub.safetensors", got["sub"]["path"])
}

func TestSamplersAndSchedulers(t *testing.T) {
	e := newEnv(t)

	rec := e.do(t, http.MethodGet, "/sdapi/v1/samplers", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var samplers []string
	for _, m := range decodeList(t, rec) {
		name := m["name"].(string)
		samplers = append(samplers, name)
		assert.Contains(t, m, "aliases")
		assert.Contains(t, m, "options")
		_, ok := SamplerName(name)
		assert.True(t, ok, "listed sampler %q must be accepted", name)
	}
	for _, want := range []string{"Euler", "Euler a", "DPM++ 2M"} {
		assert.Contains(t, samplers, want)
	}
	assert.NotContains(t, samplers, "euler_a")
	assert.NotContains(t, samplers, "dpm++2m")

	rec = e.do(t, http.MethodGet, "/sdapi/v1/schedulers", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var scheds []string
	for _, m := range decodeList(t, rec) {
		name := m["name"].(string)
		scheds = append(scheds, name)
		assert.Contains(t, m, "label")
		_, ok := SchedulerName(name)
		assert.True(t, ok, "listed scheduler %q must be accepted", name)
	}
	for _, want := range []string{"Simple", "Karras"} {
		assert.Contains(t, scheds, want)
	}
	assert.NotContains(t, scheds, "simple")
	assert.NotContains(t, scheds, "karras")
}

func TestUpscalers(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodGet, "/sdapi/v1/upscalers", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := map[string]map[string]any{}
	for _, m := range decodeList(t, rec) {
		got[m["name"].(string)] = m
	}
	for _, builtin := range []string{"None", "Lanczos", "Nearest"} {
		assert.Contains(t, got, builtin)
	}
	require.Contains(t, got, "RealESRGAN_x4plus")
	assert.EqualValues(t, 4, got["RealESRGAN_x4plus"]["scale"])
}

func TestExtraSingleImage(t *testing.T) {
	cases := []struct {
		name         string
		upscaler     string
		resize       float64
		wantW, wantH int
		wantUpscales int
	}{
		{"model upscaler resized to x2", "RealESRGAN_x4plus", 2, 16, 12, 1},
		{"model upscaler x4", "RealESRGAN_x4plus", 4, 32, 24, 1},
		{"lanczos local", "Lanczos", 2, 16, 12, 0},
		{"nearest local", "Nearest", 3, 24, 18, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			src := pngB64(t, 8, 6)
			rec := e.do(t, http.MethodPost, "/sdapi/v1/extra-single-image", map[string]any{
				"image": src, "upscaler_1": tc.upscaler, "upscaling_resize": tc.resize,
			})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			m := decodeMap(t, rec)
			out, ok := m["image"].(string)
			require.True(t, ok)
			w, h := imageSize(t, out)
			assert.Equal(t, tc.wantW, w)
			assert.Equal(t, tc.wantH, h)
			assert.Contains(t, m, "html_info")
			var ups []map[string]any
			e.sd.get(func(f *fakeSD) { ups = append(ups, f.upscales...) })
			require.Len(t, ups, tc.wantUpscales)
			if tc.wantUpscales > 0 {
				assert.Equal(t, tc.upscaler, ups[0]["upscaler"])
				assert.Equal(t, src, ups[0]["image"])
			}
		})
	}
}

func TestMemory(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodGet, "/sdapi/v1/memory", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := decodeMap(t, rec)
	ram := sub(t, m, "ram")
	assert.EqualValues(t, 1e9, ram["free"])
	assert.EqualValues(t, 2e9, ram["used"])
	assert.EqualValues(t, 3e9, ram["total"])
	sys := sub(t, sub(t, m, "cuda"), "system")
	assert.EqualValues(t, 4e9, sys["free"])
	assert.EqualValues(t, 5e9, sys["used"])
	assert.EqualValues(t, 9e9, sys["total"])
	assert.GreaterOrEqual(t, e.mem.calls.Load(), int32(1))
}

func TestUnloadCheckpoint(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/unload-checkpoint", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var unloads int
	e.sd.get(func(f *fakeSD) { unloads = f.unloads })
	assert.Equal(t, 1, unloads)
}

func TestUnknownPath404(t *testing.T) {
	cases := []struct{ method, path string }{
		{http.MethodGet, "/sdapi/v1/does-not-exist"},
		{http.MethodPost, "/sdapi/v1/does-not-exist"},
		{http.MethodGet, "/sdapi/v2/txt2img"},
		{http.MethodGet, "/not-sdapi"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(t, tc.method, tc.path, nil)
			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.Empty(t, e.sd.imgGenBodies())
		})
	}
}

func TestStatePath_PersistAndRestore(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "sdcpp-state.json")
	e := newEnv(t, func(c *Config) { c.StatePath = statePath })
	body := e.kleinOptions()
	body["klein_do_reference"] = true
	rec := e.do(t, http.MethodPost, "/sdapi/v1/options", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	first := e.rs.Calls()
	require.Len(t, first, 1)

	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	assert.True(t, json.Valid(raw), "state file must be JSON: %s", raw)

	rs2 := &fakeRestarter{}
	f2 := New(e.cfg, rs2, e.mem)
	e2 := &testEnv{sd: e.sd, rs: rs2, mem: e.mem, dirs: e.dirs, cfg: e.cfg, f: f2}

	got := decodeMap(t, e2.do(t, http.MethodGet, "/sdapi/v1/options", nil))
	assert.Equal(t, "flux-2-klein-4b.safetensors", got["sd_model_checkpoint"])
	assert.Equal(t, true, got["klein_do_reference"])

	gen := e2.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p"})
	require.Equal(t, http.StatusOK, gen.Code, gen.Body.String())
	calls := rs2.Calls()
	require.NotEmpty(t, calls, "restored model must be started via RestartWithArgs")
	assert.Equal(t, first[0], calls[len(calls)-1])

	gen = e2.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p"})
	require.Equal(t, http.StatusOK, gen.Code, gen.Body.String())
	assert.Len(t, rs2.Calls(), len(calls), "restore must happen once")
}

func TestStatePath_NotWrittenOnError(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "sdcpp-state.json")
	e := newEnv(t, func(c *Config) { c.StatePath = statePath })
	rec := e.do(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"sd_model_checkpoint": "z-image-turbo-aio.safetensors"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	_, err := os.Stat(statePath)
	assert.True(t, os.IsNotExist(err), "state must not be written after a failed options update")
}

func TestStatePath_MissingFileNoRestart(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.StatePath = filepath.Join(t.TempDir(), "absent.json") })
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, e.rs.Calls())
}

func TestGeneration_InterruptDuringModelLoad(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = e.do(t, http.MethodPost, "/sdapi/v1/unload-checkpoint", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	entered := make(chan struct{})
	release := make(chan struct{})
	e.rs.mu.Lock()
	e.rs.onRestart = func() {
		close(entered)
		<-release
	}
	e.rs.mu.Unlock()
	gen := e.start(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p", "seed": 1})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("model load did not start")
	}
	_, jc := progressOf(t, e)
	assert.Equal(t, float64(1), jc)
	rec = e.do(t, http.MethodPost, "/sdapi/v1/interrupt", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	close(release)

	res := wait(t, gen)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	assert.Empty(t, images(t, decodeMap(t, res)))
	assert.Empty(t, e.sd.imgGenBodies())
}

func TestInterruptAfterFirstIterationReturnsEmpty(t *testing.T) {
	e := newEnv(t)
	e.sd.holdFrom.Store(2)
	gen := e.start(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "p", "seed": 1, "n_iter": 3})
	require.True(t, waitFor(t, 2*time.Second, func() bool { return len(e.sd.imgGenBodies()) == 2 }))
	rec := e.do(t, http.MethodPost, "/sdapi/v1/interrupt", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := wait(t, gen)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	assert.Empty(t, images(t, decodeMap(t, res)))
	assert.Len(t, e.sd.imgGenBodies(), 2)
}

func TestConcurrentOptionsKeepReferenceFlag(t *testing.T) {
	e := newEnv(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	e.rs.mu.Lock()
	e.rs.onRestart = func() {
		close(entered)
		<-release
	}
	e.rs.mu.Unlock()
	a := e.start(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors"})
	<-entered
	b := e.start(t, http.MethodPost, "/sdapi/v1/options", map[string]any{"klein_do_reference": true})
	time.Sleep(50 * time.Millisecond)
	close(release)
	require.Equal(t, http.StatusOK, wait(t, a).Code)
	require.Equal(t, http.StatusOK, wait(t, b).Code)
	got := decodeMap(t, e.do(t, http.MethodGet, "/sdapi/v1/options", nil))
	assert.Equal(t, true, got["klein_do_reference"])
	assert.Contains(t, got["sd_model_checkpoint"], "sdxl_base")
}

func TestLoadTimeoutDoesNotRestartAgain(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ReadyTimeout = 50 * time.Millisecond })
	e.rs.mu.Lock()
	e.rs.onRestart = func() { e.sd.set(func(f *fakeSD) { f.notReady = 1 << 30 }) }
	e.rs.mu.Unlock()
	body := map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors"}
	rec := e.do(t, http.MethodPost, "/sdapi/v1/options", body)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	e.sd.set(func(f *fakeSD) { f.notReady = 0 })
	rec = e.do(t, http.MethodPost, "/sdapi/v1/options", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, e.rs.Calls(), 1)
}

func TestLorasNamedWithoutAnyExtension(t *testing.T) {
	e := newEnv(t)
	touch(t, filepath.Join(e.cfg.LoraDir, "old.pt"))
	touch(t, filepath.Join(e.cfg.LoraDir, "x.sft"))
	names := map[string]bool{}
	for _, m := range decodeList(t, e.do(t, http.MethodGet, "/sdapi/v1/loras", nil)) {
		names[m["name"].(string)] = true
	}
	assert.True(t, names["old"], names)
	assert.True(t, names["x"], names)
	rec := e.do(t, http.MethodPost, "/sdapi/v1/txt2img", map[string]any{"prompt": "a <lora:old:0.5>", "seed": 1})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestUnloadClearsPendingLoad(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ReadyTimeout = 50 * time.Millisecond })
	e.rs.mu.Lock()
	e.rs.onRestart = func() { e.sd.set(func(f *fakeSD) { f.notReady = 1 << 30 }) }
	e.rs.mu.Unlock()
	body := map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors"}
	require.Equal(t, http.StatusBadGateway, e.do(t, http.MethodPost, "/sdapi/v1/options", body).Code)
	e.sd.set(func(f *fakeSD) { f.notReady = 0 })
	require.Equal(t, http.StatusOK, e.do(t, http.MethodPost, "/sdapi/v1/unload-checkpoint", nil).Code)
	e.rs.mu.Lock()
	e.rs.onRestart = nil
	e.rs.mu.Unlock()
	require.Equal(t, http.StatusOK, e.do(t, http.MethodPost, "/sdapi/v1/options", body).Code)
	assert.Len(t, e.rs.Calls(), 2)
}

type deadRestarter struct{ fakeRestarter }

func (*deadRestarter) Alive() bool { return false }

func TestLoadRestartsWhenProcessDied(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ReadyTimeout = 50 * time.Millisecond })
	rs := &deadRestarter{}
	rs.onRestart = func() { e.sd.set(func(f *fakeSD) { f.notReady = 1 << 30 }) }
	f := New(e.cfg, rs, e.mem)
	body := map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors"}
	do := func() int {
		rec := httptest.NewRecorder()
		f.ServeHTTP(rec, newRequest(t, http.MethodPost, "/sdapi/v1/options", body))
		return rec.Code
	}
	require.Equal(t, http.StatusBadGateway, do())
	rs.mu.Lock()
	rs.onRestart = func() { e.sd.set(func(f *fakeSD) { f.notReady = 0 }) }
	rs.mu.Unlock()
	require.Equal(t, http.StatusOK, do())
	assert.Len(t, rs.Calls(), 2)
}
