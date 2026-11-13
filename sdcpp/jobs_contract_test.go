package sdcpp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
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

func jcPNG(w, h int) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 7), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

type jcSDJob struct {
	id        string
	batch     int
	cancelled bool
	model     string
	prompt    string
}

type jcSD struct {
	srv *httptest.Server

	mu         sync.Mutex
	loaded     string
	jobs       map[string]*jcSDJob
	gens       []*jcSDJob
	cancels    []string
	upscales   int
	unloads    int
	failed     bool
	unexpected []string

	hold atomic.Bool
}

func newJcSD(t *testing.T) *jcSD {
	t.Helper()
	f := &jcSD{jobs: map[string]*jcSDJob{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sdcpp/v1/img_gen", f.handleImgGen)
	mux.HandleFunc("GET /sdcpp/v1/jobs/{id}", f.handleJob)
	mux.HandleFunc("POST /sdcpp/v1/jobs/{id}/cancel", f.handleCancel)
	mux.HandleFunc("POST /sdcpp/v1/upscale", f.handleUpscale)
	mux.HandleFunc("POST /sdcpp/v1/models/unload", f.handleUnload)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			jcWrite(w, http.StatusOK, map[string]any{"samples_format": "png", "sd_model_checkpoint": ""})
			return
		}
		f.mu.Lock()
		f.unexpected = append(f.unexpected, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		http.NotFound(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, u := range f.unexpected {
			t.Errorf("fake sd-server: unexpected request %s", u)
		}
	})
	return f
}

func jcWrite(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *jcSD) handleImgGen(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jcWrite(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	batch := 1
	if v, ok := body["batch_count"].(float64); ok && v > 0 {
		batch = int(v)
	}
	prompt, _ := body["prompt"].(string)
	id := fmt.Sprintf("sd_%d", len(f.gens)+1)
	j := &jcSDJob{id: id, batch: batch, model: f.loaded, prompt: prompt}
	f.jobs[id] = j
	f.gens = append(f.gens, j)
	jcWrite(w, http.StatusAccepted, map[string]any{
		"id": id, "kind": "img_gen", "status": "queued", "created": time.Now().UnixMilli(), "poll_url": "/sdcpp/v1/jobs/" + id,
	})
}

func (f *jcSD) handleJob(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	j := f.jobs[r.PathValue("id")]
	failed := f.failed
	var cancelled bool
	var batch int
	if j != nil {
		cancelled, batch = j.cancelled, j.batch
	}
	f.mu.Unlock()
	if j == nil {
		jcWrite(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	resp := map[string]any{"id": j.id, "kind": "img_gen", "created": time.Now().UnixMilli(), "queue_position": 0}
	switch {
	case cancelled:
		resp["status"] = "cancelled"
		resp["result"] = nil
		resp["error"] = map[string]any{"code": "cancelled", "message": "job cancelled by client"}
	case f.hold.Load():
		resp["status"] = "generating"
		resp["result"] = nil
		resp["error"] = nil
		resp["progress"] = map[string]any{"stage": "sampling", "detail": "", "pass": 1, "completed": 5, "total": 10, "unit": "steps", "stage_elapsed_ms": 10, "elapsed_ms": 20}
	case failed:
		resp["status"] = "failed"
		resp["result"] = nil
		resp["error"] = map[string]any{"code": "generation_failed", "message": "generate_image returned empty results"}
	default:
		imgs := make([]map[string]any, batch)
		for i := range imgs {
			imgs[i] = map[string]any{"index": i, "b64_json": jcPNG(8, 8)}
		}
		resp["status"] = "completed"
		resp["result"] = map[string]any{"output_format": "png", "images": imgs}
		resp["error"] = nil
		resp["progress"] = map[string]any{"stage": "encoding", "detail": "image", "pass": 1, "completed": 1, "total": 1, "unit": "images", "stage_elapsed_ms": 1, "elapsed_ms": 30}
	}
	jcWrite(w, http.StatusOK, resp)
}

func (f *jcSD) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, id)
	j := f.jobs[id]
	if j == nil {
		jcWrite(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	j.cancelled = true
	jcWrite(w, http.StatusOK, map[string]any{"id": id, "status": "generating", "cancel_requested": true})
}

func (f *jcSD) handleUpscale(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jcWrite(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	f.mu.Lock()
	f.upscales++
	f.mu.Unlock()
	src, _ := body["image"].(string)
	img, err := jcDecode(src)
	if err != nil {
		jcWrite(w, http.StatusBadRequest, map[string]any{"error": "unreadable image"})
		return
	}
	b := img.Bounds()
	jcWrite(w, http.StatusOK, map[string]any{
		"images":   []map[string]any{{"index": 0, "b64_json": jcPNG(b.Dx()*4, b.Dy()*4)}},
		"upscaler": body["upscaler"], "scale": 4, "repeats": 1,
		"width": b.Dx() * 4, "height": b.Dy() * 4, "output_format": "png",
	})
}

func (f *jcSD) handleUnload(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unloads++
	f.loaded = ""
	jcWrite(w, http.StatusOK, map[string]any{"unloaded": true})
}

func (f *jcSD) snapshot() (gens []jcSDJob, cancels []string, unloads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.gens {
		gens = append(gens, *g)
	}
	return gens, append([]string(nil), f.cancels...), f.unloads
}

func jcDecode(s string) (image.Image, error) {
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

type jcRestarter struct {
	sd    *jcSD
	mu    sync.Mutex
	calls [][]string
}

func (r *jcRestarter) RestartWithArgs(args []string) error {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	r.mu.Unlock()
	model := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--model" || args[i] == "--diffusion-model" {
			model = args[i+1]
		}
	}
	r.sd.mu.Lock()
	r.sd.loaded = model
	r.sd.mu.Unlock()
	return nil
}

func (r *jcRestarter) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

type jcMemory struct{ stats MemoryStats }

func (m *jcMemory) Memory() (MemoryStats, error) { return m.stats, nil }

type jcClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *jcClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *jcClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type jcEnv struct {
	sd   *jcSD
	rs   *jcRestarter
	mem  *jcMemory
	dirs modelDirs
	cfg  Config
	f    *Facade
}

func newJcEnv(t *testing.T, opts ...func(*Config)) *jcEnv {
	t.Helper()
	sd := newJcSD(t)
	d := newModelDirs(t)
	cfg := d.cfg
	cfg.BaseURL = sd.srv.URL
	cfg.BaseArgs = []string{"--listen-port", "7861", "--host", "127.0.0.1"}
	cfg.PollInterval = 5 * time.Millisecond
	cfg.ReadyTimeout = 2 * time.Second
	touch(t, filepath.Join(cfg.UpscalerDir, "RealESRGAN_x4plus.pth"))
	touch(t, filepath.Join(cfg.LoraDir, "detail.safetensors"))
	touch(t, filepath.Join(cfg.LoraDir, "styles", "sub.safetensors"))
	for _, o := range opts {
		o(&cfg)
	}
	rs := &jcRestarter{sd: sd}
	mem := &jcMemory{stats: MemoryStats{
		RAM:  Pool{Free: 1e9, Used: 2e9, Total: 3e9},
		CUDA: &CUDA{System: Pool{Free: 4e9, Used: 5e9, Total: 9e9}},
	}}
	return &jcEnv{sd: sd, rs: rs, mem: mem, dirs: d, cfg: cfg, f: New(cfg, rs, mem)}
}

func (e *jcEnv) reopen() *Facade { return New(e.cfg, e.rs, e.mem) }

func jcJobsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdjobs-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func jcServe(t *testing.T, h http.Handler, ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd).WithContext(ctx)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		out <- rec
	}()
	select {
	case rec := <-out:
		return rec
	case <-time.After(5 * time.Second):
		t.Fatalf("%s %s: no response within 5s", method, path)
		return nil
	}
}

func jcDo(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return jcServe(t, h, context.Background(), method, path, body)
}

func jcMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), "body: %s", rec.Body.String())
	return m
}

func jcErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	m := jcMap(t, rec)
	e, ok := m["error"].(map[string]any)
	require.True(t, ok, "error must be an object: %s", rec.Body.String())
	code, _ := e["code"].(string)
	msg, _ := e["message"].(string)
	assert.NotEmpty(t, code, "error.code: %s", rec.Body.String())
	assert.NotEmpty(t, msg, "error.message: %s", rec.Body.String())
	return code
}

func jcGenParams(prompt string) map[string]any {
	return map[string]any{
		"prompt": prompt, "negative_prompt": "", "sampler": "euler_a", "scheduler": "karras",
		"steps": 4, "cfg_scale": 5.0, "distilled_cfg_scale": 3.5, "width": 64, "height": 64,
		"seed": 42, "clip_skip": 0, "batch_size": 1,
		"hires": map[string]any{"enable": false, "scale": 2.0, "upscaler": "lanczos", "denoising": 0.5, "steps": 0, "resize_x": 0, "resize_y": 0},
	}
}

func jcImg2ImgParams(prompt string) map[string]any {
	p := jcGenParams(prompt)
	p["init_image"] = jcPNG(8, 8)
	p["mask"] = ""
	p["denoising_strength"] = 0.5
	return p
}

func jcUpscaleParams(upscaler string) map[string]any {
	return map[string]any{"image": jcPNG(8, 8), "upscaler": upscaler, "scale": 4}
}

func jcJob(kind, client, ref, checkpoint string, params map[string]any) map[string]any {
	body := map[string]any{"kind": kind, "client": client, "params": params}
	if ref != "" {
		body["client_ref"] = ref
	}
	if checkpoint != "" || kind != "upscale" {
		body["model"] = map[string]any{"checkpoint": checkpoint, "vae": "", "modules": []string{}, "klein_reference": false, "moss_reference": false}
	}
	return body
}

func jcTxt(client, ref, checkpoint, prompt string) map[string]any {
	return jcJob("txt2img", client, ref, checkpoint, jcGenParams(prompt))
}

func jcSubmit(t *testing.T, h http.Handler, body any, wantCode int) map[string]any {
	t.Helper()
	rec := jcDo(t, h, http.MethodPost, "/jobs", body)
	require.Equal(t, wantCode, rec.Code, rec.Body.String())
	st := jcMap(t, rec)
	id, _ := st["id"].(string)
	require.NotEmpty(t, id, "job id: %s", rec.Body.String())
	return st
}

func jcID(st map[string]any) string {
	id, _ := st["id"].(string)
	return id
}

func jcStatus(t *testing.T, h http.Handler, id string) map[string]any {
	t.Helper()
	rec := jcDo(t, h, http.MethodGet, "/jobs/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return jcMap(t, rec)
}

func jcWaitStatus(t *testing.T, h http.Handler, id, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = jcStatus(t, h, id)
		if last["status"] == want {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s: want status %s, last %v", id, want, last)
	return nil
}

func jcWaitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func jcTime(t *testing.T, st map[string]any, key string) time.Time {
	t.Helper()
	s, ok := st[key].(string)
	require.True(t, ok, "%s must be an RFC3339 string: %v", key, st[key])
	ts, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err, "%s: %q", key, s)
	return ts
}

func jcResult(t *testing.T, h http.Handler, id string) ([]string, map[string]any) {
	t.Helper()
	rec := jcDo(t, h, http.MethodGet, "/jobs/"+id+"/result", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := jcMap(t, rec)
	raw, ok := m["images"].([]any)
	require.True(t, ok, "images must be an array: %s", rec.Body.String())
	imgs := make([]string, len(raw))
	for i, v := range raw {
		s, ok := v.(string)
		require.True(t, ok, "image %d must be a base64 string", i)
		imgs[i] = s
	}
	info, ok := m["info"].(map[string]any)
	require.True(t, ok, "info must be an object: %v", m["info"])
	return imgs, info
}

type jcLeaser struct {
	mu        sync.Mutex
	reqs      []LeaseRequest
	released  []string
	genAtRel  []int
	sd        *jcSD
	block     func(LeaseRequest) bool
	err       error
	unblock   chan struct{}
	closeOnce sync.Once
	ctxDone   atomic.Int32
}

func newJcLeaser(t *testing.T, sd *jcSD) *jcLeaser {
	l := &jcLeaser{sd: sd, unblock: make(chan struct{})}
	t.Cleanup(l.open)
	return l
}

func (l *jcLeaser) open() { l.closeOnce.Do(func() { close(l.unblock) }) }

func (l *jcLeaser) lease(ctx context.Context, req LeaseRequest) (func(), error) {
	l.mu.Lock()
	l.reqs = append(l.reqs, req)
	blk := l.block != nil && l.block(req)
	err := l.err
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if blk {
		select {
		case <-l.unblock:
		case <-ctx.Done():
			l.ctxDone.Add(1)
			return nil, ctx.Err()
		}
	}
	return func() {
		l.sd.mu.Lock()
		n := len(l.sd.gens) + l.sd.upscales
		l.sd.mu.Unlock()
		l.mu.Lock()
		l.released = append(l.released, req.JobID)
		l.genAtRel = append(l.genAtRel, n)
		l.mu.Unlock()
	}, nil
}

func (l *jcLeaser) requests() []LeaseRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LeaseRequest(nil), l.reqs...)
}

func (l *jcLeaser) releasedIDs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.released...)
}

func blockAll(LeaseRequest) bool { return true }

func TestJobsSubmitAnswersQueuedImmediately(t *testing.T) {
	e := newJcEnv(t)
	l := newJcLeaser(t, e.sd)
	l.block = blockAll
	e.f.SetLeaser(l.lease)

	start := time.Now()
	st := jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a cat"), http.StatusAccepted)
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, "queued", st["status"])
	assert.Equal(t, "txt2img", st["kind"])
	assert.Equal(t, "test", st["client"])
	assert.EqualValues(t, 0, st["position"])
	assert.Nil(t, st["error"])
	assert.Nil(t, st["started_at"])
	assert.Nil(t, st["finished_at"])
	assert.Nil(t, st["expires_at"])
	jcTime(t, st, "created_at")
	_, ok := st["progress"].(map[string]any)
	assert.True(t, ok, "progress must be an object: %v", st["progress"])

	id := jcID(st)
	assert.Equal(t, "queued", jcStatus(t, e.f, id)["status"])
	gens, _, _ := e.sd.snapshot()
	assert.Empty(t, gens)

	l.open()
	jcWaitStatus(t, e.f, id, "completed")
}

func TestJobsLifecycleAndResult(t *testing.T) {
	cases := []struct {
		name        string
		body        map[string]any
		seeRunning  bool
		wantW       int
		wantH       int
		checkPrompt bool
	}{
		{"txt2img", jcTxt("test", "", "sdxl_base.safetensors", "a cat"), true, 0, 0, true},
		{"img2img", jcJob("img2img", "test", "", "sdxl_base.safetensors", jcImg2ImgParams("a cat")), true, 0, 0, true},
		{"upscale without model", jcJob("upscale", "test", "", "", jcUpscaleParams("RealESRGAN_x4plus")), false, 32, 32, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newJcEnv(t)
			e.f.SetLeaser(nil)
			e.sd.hold.Store(tc.seeRunning)
			id := jcID(jcSubmit(t, e.f, tc.body, http.StatusAccepted))

			if tc.seeRunning {
				st := jcWaitStatus(t, e.f, id, "running")
				assert.EqualValues(t, 0, st["position"])
				jcTime(t, st, "started_at")
				assert.Nil(t, st["finished_at"])
				prog, ok := st["progress"].(map[string]any)
				require.True(t, ok, "progress: %v", st["progress"])
				assert.EqualValues(t, 5, prog["step"])
				assert.EqualValues(t, 10, prog["steps"])

				rec := jcDo(t, e.f, http.MethodGet, "/jobs/"+id+"/result", nil)
				assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
				assert.Equal(t, "not_ready", jcErrCode(t, rec))
				e.sd.hold.Store(false)
			}

			st := jcWaitStatus(t, e.f, id, "completed")
			assert.Nil(t, st["error"])
			fin := jcTime(t, st, "finished_at")
			exp := jcTime(t, st, "expires_at")
			assert.True(t, exp.After(fin))
			imgs, info := jcResult(t, e.f, id)
			require.Len(t, imgs, 1)
			img, err := jcDecode(imgs[0])
			require.NoError(t, err)
			if tc.wantW > 0 {
				assert.Equal(t, tc.wantW, img.Bounds().Dx())
				assert.Equal(t, tc.wantH, img.Bounds().Dy())
			}
			if tc.checkPrompt {
				assert.Equal(t, "a cat", info["prompt"])
				assert.Contains(t, info, "seed")
			}

			rec := jcDo(t, e.f, http.MethodDelete, "/jobs/"+id, nil)
			assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			for _, p := range []string{"/jobs/" + id, "/jobs/" + id + "/result"} {
				rec = jcDo(t, e.f, http.MethodGet, p, nil)
				assert.Equal(t, http.StatusNotFound, rec.Code, p)
				assert.Equal(t, "not_found", jcErrCode(t, rec), p)
			}
			rec = jcDo(t, e.f, http.MethodDelete, "/jobs/"+id, nil)
			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

func TestJobsUnknownIDNotFound(t *testing.T) {
	e := newJcEnv(t)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/jobs/nope"},
		{http.MethodGet, "/jobs/nope/result"},
		{http.MethodDelete, "/jobs/nope"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := jcDo(t, e.f, tc.method, tc.path, nil)
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.Equal(t, "not_found", jcErrCode(t, rec))
		})
	}
}

func TestJobsListFiltersByClient(t *testing.T) {
	e := newJcEnv(t)
	a := jcID(jcSubmit(t, e.f, jcTxt("alpha", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
	jcWaitStatus(t, e.f, a, "completed")
	b := jcID(jcSubmit(t, e.f, jcTxt("beta", "", "sdxl_base.safetensors", "b"), http.StatusAccepted))

	list := func(path string) map[string]string {
		rec := jcDo(t, e.f, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			Jobs []map[string]any `json:"jobs"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
		require.NotNil(t, body.Jobs, rec.Body.String())
		out := map[string]string{}
		for _, j := range body.Jobs {
			c, _ := j["client"].(string)
			out[jcID(j)] = c
		}
		return out
	}

	all := list("/jobs")
	assert.Equal(t, "alpha", all[a])
	assert.Equal(t, "beta", all[b])
	onlyA := list("/jobs?client=alpha")
	assert.Contains(t, onlyA, a)
	assert.NotContains(t, onlyA, b)
	for id, c := range onlyA {
		assert.Equal(t, "alpha", c, id)
	}
}

func TestJobsCancel(t *testing.T) {
	t.Run("running job cancelled, repeat is harmless", func(t *testing.T) {
		e := newJcEnv(t)
		e.sd.hold.Store(true)
		id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
		jcWaitStatus(t, e.f, id, "running")

		rec := jcDo(t, e.f, http.MethodPost, "/jobs/"+id+"/cancel", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, id, jcID(jcMap(t, rec)))
		jcWaitStatus(t, e.f, id, "cancelled")
		gens, cancels, _ := e.sd.snapshot()
		require.Len(t, gens, 1)
		assert.Contains(t, cancels, gens[0].id)

		rec = jcDo(t, e.f, http.MethodPost, "/jobs/"+id+"/cancel", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "cancelled", jcMap(t, rec)["status"])
		assert.Equal(t, "cancelled", jcStatus(t, e.f, id)["status"])
	})

	t.Run("completed job unchanged", func(t *testing.T) {
		e := newJcEnv(t)
		id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
		jcWaitStatus(t, e.f, id, "completed")
		for i := 0; i < 2; i++ {
			rec := jcDo(t, e.f, http.MethodPost, "/jobs/"+id+"/cancel", nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "completed", jcMap(t, rec)["status"])
		}
		imgs, _ := jcResult(t, e.f, id)
		assert.Len(t, imgs, 1)
		_, cancels, _ := e.sd.snapshot()
		assert.Empty(t, cancels)
	})
}

func TestJobsClientDisconnectDoesNotCancel(t *testing.T) {
	e := newJcEnv(t)
	e.sd.hold.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	rec := jcServe(t, e.f, ctx, http.MethodPost, "/jobs", jcTxt("test", "", "sdxl_base.safetensors", "a"))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	id := jcID(jcMap(t, rec))
	cancel()

	jcWaitStatus(t, e.f, id, "running")
	gone, stop := context.WithCancel(context.Background())
	stop()
	jcServe(t, e.f, gone, http.MethodGet, "/jobs/"+id, nil)
	jcServe(t, e.f, gone, http.MethodGet, "/jobs/"+id+"/result", nil)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, "running", jcStatus(t, e.f, id)["status"])

	e.sd.hold.Store(false)
	jcWaitStatus(t, e.f, id, "completed")
	imgs, _ := jcResult(t, e.f, id)
	assert.Len(t, imgs, 1)
	_, cancels, _ := e.sd.snapshot()
	assert.Empty(t, cancels)
}

func TestJobsClientRefReturnsExistingJob(t *testing.T) {
	e := newJcEnv(t)
	e.sd.hold.Store(true)

	a := jcID(jcSubmit(t, e.f, jcTxt("test", "r1", "sdxl_base.safetensors", "a"), http.StatusAccepted))
	again := jcSubmit(t, e.f, jcTxt("test", "r1", "sdxl_base.safetensors", "a"), http.StatusOK)
	assert.Equal(t, a, jcID(again))
	assert.Equal(t, "r1", again["client_ref"])

	b := jcID(jcSubmit(t, e.f, jcTxt("test", "r2", "sdxl_base.safetensors", "a"), http.StatusAccepted))
	assert.NotEqual(t, a, b)
	n1 := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
	n2 := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
	assert.NotEqual(t, n1, n2)

	e.sd.hold.Store(false)
	for _, id := range []string{a, b, n1, n2} {
		jcWaitStatus(t, e.f, id, "completed")
	}
	done := jcSubmit(t, e.f, jcTxt("test", "r1", "sdxl_base.safetensors", "a"), http.StatusOK)
	assert.Equal(t, a, jcID(done))
	assert.Equal(t, "completed", done["status"])
	gens, _, _ := e.sd.snapshot()
	assert.Len(t, gens, 4)

	require.Equal(t, http.StatusNoContent, jcDo(t, e.f, http.MethodDelete, "/jobs/"+a, nil).Code)
	fresh := jcID(jcSubmit(t, e.f, jcTxt("test", "r1", "sdxl_base.safetensors", "a"), http.StatusAccepted))
	assert.NotEqual(t, a, fresh)
}

func TestJobsEachJobLoadsItsOwnModel(t *testing.T) {
	e := newJcEnv(t)
	p1 := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "first"), http.StatusAccepted))
	p2 := jcID(jcSubmit(t, e.f, jcTxt("test", "", "flux1-dev.gguf", "second"), http.StatusAccepted))
	jcWaitStatus(t, e.f, p1, "completed")
	jcWaitStatus(t, e.f, p2, "completed")

	gens, _, _ := e.sd.snapshot()
	byPrompt := map[string]string{}
	for _, g := range gens {
		byPrompt[g.prompt] = g.model
	}
	assert.Equal(t, filepath.Join(e.cfg.ModelsDir, "sdxl_base.safetensors"), byPrompt["first"])
	assert.Equal(t, filepath.Join(e.cfg.ModelsDir, "flux1-dev.gguf"), byPrompt["second"])

	rec := jcDo(t, e.f, http.MethodGet, "/vaes", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var vaes struct {
		VAEs []struct{ Name, File string } `json:"vaes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &vaes))
	vaeName := ""
	for _, v := range vaes.VAEs {
		if v.File == e.dirs.vae {
			vaeName = v.Name
		}
	}
	require.NotEmpty(t, vaeName, "vae %s not listed: %s", e.dirs.vae, rec.Body.String())

	body := jcTxt("test", "", "sdxl_base.safetensors", "third")
	body["model"].(map[string]any)["vae"] = vaeName
	id := jcID(jcSubmit(t, e.f, body, http.StatusAccepted))
	jcWaitStatus(t, e.f, id, "completed")
	calls := e.rs.Calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, e.dirs.vae, flagValues(t, last[len(e.cfg.BaseArgs):])["--vae"])
}

func TestJobsLeaser(t *testing.T) {
	t.Run("once per job, job queued while leasing, release after end", func(t *testing.T) {
		e := newJcEnv(t)
		l := newJcLeaser(t, e.sd)
		l.block = blockAll
		e.f.SetLeaser(l.lease)

		type sub struct{ client, checkpoint string }
		subs := []sub{{"imagecaster", "sdxl_base.safetensors"}, {"mcp", "flux1-dev.gguf"}}
		ids := make([]string, len(subs))
		var wg sync.WaitGroup
		recs := make([]*httptest.ResponseRecorder, len(subs))
		for i, s := range subs {
			wg.Add(1)
			go func(i int, s sub) {
				defer wg.Done()
				rec := httptest.NewRecorder()
				raw, _ := json.Marshal(jcTxt(s.client, "", s.checkpoint, s.client))
				req := httptest.NewRequest(http.MethodPost, "/jobs", bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				e.f.ServeHTTP(rec, req)
				recs[i] = rec
			}(i, s)
		}
		wg.Wait()
		for i, rec := range recs {
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			ids[i] = jcID(jcMap(t, rec))
		}
		assert.NotEqual(t, ids[0], ids[1])

		require.True(t, jcWaitFor(2*time.Second, func() bool { return len(l.requests()) == 2 }))
		time.Sleep(50 * time.Millisecond)
		for _, id := range ids {
			assert.Equal(t, "queued", jcStatus(t, e.f, id)["status"])
		}
		gens, _, _ := e.sd.snapshot()
		assert.Empty(t, gens)
		got := map[string]LeaseRequest{}
		for _, r := range l.requests() {
			got[r.JobID] = r
		}
		for i, s := range subs {
			assert.Equal(t, LeaseRequest{JobID: ids[i], Client: s.client, Checkpoint: s.checkpoint}, got[ids[i]])
		}

		l.open()
		for _, id := range ids {
			jcWaitStatus(t, e.f, id, "completed")
		}
		require.True(t, jcWaitFor(2*time.Second, func() bool { return len(l.releasedIDs()) >= 2 }))
		assert.ElementsMatch(t, ids, l.releasedIDs()[:2])
		assert.Len(t, l.requests(), 2)
	})

	t.Run("release not before generation ends", func(t *testing.T) {
		e := newJcEnv(t)
		l := newJcLeaser(t, e.sd)
		e.f.SetLeaser(l.lease)
		e.sd.hold.Store(true)
		id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
		jcWaitStatus(t, e.f, id, "running")
		time.Sleep(30 * time.Millisecond)
		assert.Empty(t, l.releasedIDs())
		e.sd.hold.Store(false)
		jcWaitStatus(t, e.f, id, "completed")
		require.True(t, jcWaitFor(2*time.Second, func() bool { return len(l.releasedIDs()) >= 1 }))
		assert.Equal(t, id, l.releasedIDs()[0])
		l.mu.Lock()
		assert.GreaterOrEqual(t, l.genAtRel[0], 1)
		l.mu.Unlock()
		assert.Len(t, l.requests(), 1)
	})

	t.Run("release after failed job", func(t *testing.T) {
		e := newJcEnv(t)
		l := newJcLeaser(t, e.sd)
		e.f.SetLeaser(l.lease)
		e.sd.mu.Lock()
		e.sd.failed = true
		e.sd.mu.Unlock()
		id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
		st := jcWaitStatus(t, e.f, id, "failed")
		errObj, ok := st["error"].(map[string]any)
		require.True(t, ok, "error: %v", st["error"])
		assert.Equal(t, "sd_failed", errObj["code"])
		assert.NotEmpty(t, errObj["message"])
		jcTime(t, st, "finished_at")
		require.True(t, jcWaitFor(2*time.Second, func() bool { return len(l.releasedIDs()) >= 1 }))
		assert.Equal(t, id, l.releasedIDs()[0])
	})

	t.Run("leaser cancelled means job cancelled", func(t *testing.T) {
		e := newJcEnv(t)
		l := newJcLeaser(t, e.sd)
		l.err = context.Canceled
		e.f.SetLeaser(l.lease)
		id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
		jcWaitStatus(t, e.f, id, "cancelled")
		gens, _, _ := e.sd.snapshot()
		assert.Empty(t, gens)
		assert.Empty(t, e.rs.Calls())
	})

	t.Run("cancel while queued stops waiting for lease", func(t *testing.T) {
		e := newJcEnv(t)
		l := newJcLeaser(t, e.sd)
		l.block = blockAll
		e.f.SetLeaser(l.lease)
		id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
		require.True(t, jcWaitFor(2*time.Second, func() bool { return len(l.requests()) == 1 }))
		assert.Equal(t, "queued", jcStatus(t, e.f, id)["status"])

		rec := jcDo(t, e.f, http.MethodPost, "/jobs/"+id+"/cancel", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		jcWaitStatus(t, e.f, id, "cancelled")
		assert.True(t, jcWaitFor(2*time.Second, func() bool { return l.ctxDone.Load() == 1 }), "lease wait must be cancelled")
		gens, cancels, _ := e.sd.snapshot()
		assert.Empty(t, gens)
		assert.Empty(t, cancels)
		assert.Len(t, l.requests(), 1)
	})
}

func TestJobsLeaseCheckpointIsJobModel(t *testing.T) {
	e := newJcEnv(t)
	l := newJcLeaser(t, e.sd)
	e.f.SetLeaser(l.lease)
	first := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
	jcWaitStatus(t, e.f, first, "completed")
	second := jcID(jcSubmit(t, e.f, jcTxt("test", "", "flux1-dev.gguf", "b"), http.StatusAccepted))
	jcWaitStatus(t, e.f, second, "completed")

	reqs := l.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, LeaseRequest{JobID: first, Client: "test", Checkpoint: "sdxl_base.safetensors"}, reqs[0])
	assert.Equal(t, LeaseRequest{JobID: second, Client: "test", Checkpoint: "flux1-dev.gguf"}, reqs[1])
}

func TestJobsPositionFromQueue(t *testing.T) {
	e := newJcEnv(t)
	l := newJcLeaser(t, e.sd)
	l.block = blockAll
	e.f.SetLeaser(l.lease)
	var asked sync.Map
	e.f.SetPositionFunc(func(id string) int {
		asked.Store(id, true)
		return 3
	})
	id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
	st := jcStatus(t, e.f, id)
	assert.Equal(t, "queued", st["status"])
	assert.EqualValues(t, 3, st["position"])
	_, ok := asked.Load(id)
	assert.True(t, ok)

	l.open()
	st = jcWaitStatus(t, e.f, id, "completed")
	assert.EqualValues(t, 0, st["position"])
}

func TestJobsSurviveRestart(t *testing.T) {
	t.Run("completed result survives", func(t *testing.T) {
		dir := jcJobsDir(t)
		e := newJcEnv(t, func(c *Config) { c.JobsDir = dir })
		id := jcID(jcSubmit(t, e.f, jcTxt("test", "keep", "sdxl_base.safetensors", "a cat"), http.StatusAccepted))
		jcWaitStatus(t, e.f, id, "completed")
		before, _ := jcResult(t, e.f, id)

		f2 := e.reopen()
		st := jcStatus(t, f2, id)
		assert.Equal(t, "completed", st["status"])
		assert.Equal(t, "keep", st["client_ref"])
		after, info := jcResult(t, f2, id)
		assert.Equal(t, before, after)
		assert.Equal(t, "a cat", info["prompt"])
		assert.Equal(t, id, jcID(jcSubmit(t, f2, jcTxt("test", "keep", "sdxl_base.safetensors", "a cat"), http.StatusOK)))
	})

	t.Run("queued and running become lost", func(t *testing.T) {
		dir := jcJobsDir(t)
		e := newJcEnv(t, func(c *Config) { c.JobsDir = dir })
		l := newJcLeaser(t, e.sd)
		l.block = func(r LeaseRequest) bool { return r.Client == "waiting" }
		e.f.SetLeaser(l.lease)
		e.sd.hold.Store(true)

		r := jcID(jcSubmit(t, e.f, jcTxt("running", "ref-r", "sdxl_base.safetensors", "r"), http.StatusAccepted))
		jcWaitStatus(t, e.f, r, "running")
		q := jcID(jcSubmit(t, e.f, jcTxt("waiting", "ref-q", "sdxl_base.safetensors", "q"), http.StatusAccepted))
		require.True(t, jcWaitFor(2*time.Second, func() bool { return len(l.requests()) == 2 }))
		assert.Equal(t, "queued", jcStatus(t, e.f, q)["status"])

		f2 := e.reopen()
		for _, id := range []string{r, q} {
			st := jcStatus(t, f2, id)
			assert.Equal(t, "lost", st["status"], id)
			errObj, ok := st["error"].(map[string]any)
			require.True(t, ok, "error: %v", st["error"])
			assert.Equal(t, "server_restarted", errObj["code"])
		}
		fresh := jcID(jcSubmit(t, f2, jcTxt("running", "ref-r", "sdxl_base.safetensors", "r"), http.StatusAccepted))
		assert.NotEqual(t, r, fresh)
	})

	t.Run("ttl from config and expired removed", func(t *testing.T) {
		dir := jcJobsDir(t)
		clock := &jcClock{now: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
		e := newJcEnv(t, func(c *Config) {
			c.JobsDir = dir
			c.JobTTL = time.Hour
			c.Now = clock.Now
		})
		id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
		st := jcWaitStatus(t, e.f, id, "completed")
		assert.Equal(t, time.Hour, jcTime(t, st, "expires_at").Sub(jcTime(t, st, "finished_at")))

		clock.Add(30 * time.Minute)
		f2 := e.reopen()
		assert.Equal(t, "completed", jcStatus(t, f2, id)["status"])

		clock.Add(2 * time.Hour)
		f3 := e.reopen()
		rec := jcDo(t, f3, http.MethodGet, "/jobs/"+id, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		traces := func() []string {
			var out []string
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || !d.Type().IsRegular() {
					return nil
				}
				b, _ := os.ReadFile(p)
				if strings.Contains(d.Name(), id) || bytes.Contains(b, []byte(id)) {
					out = append(out, p)
				}
				return nil
			})
			return out
		}
		assert.True(t, jcWaitFor(3*time.Second, func() bool { return len(traces()) == 0 }), "expired job still on disk: %v", traces())
	})

	t.Run("default ttl is 24h", func(t *testing.T) {
		clock := &jcClock{now: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
		e := newJcEnv(t, func(c *Config) {
			c.JobsDir = jcJobsDir(t)
			c.Now = clock.Now
		})
		id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
		st := jcWaitStatus(t, e.f, id, "completed")
		assert.Equal(t, 24*time.Hour, jcTime(t, st, "expires_at").Sub(jcTime(t, st, "finished_at")))
	})
}

func TestJobsOldSDAPIRoutesGone(t *testing.T) {
	e := newJcEnv(t)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/sdapi/v1/options"},
		{http.MethodPost, "/sdapi/v1/options"},
		{http.MethodPost, "/options"},
		{http.MethodPost, "/sdapi/v1/txt2img"},
		{http.MethodPost, "/sdapi/v1/img2img"},
		{http.MethodGet, "/sdapi/v1/progress"},
		{http.MethodPost, "/sdapi/v1/interrupt"},
		{http.MethodGet, "/sdapi/v1/sd-models"},
		{http.MethodGet, "/sdapi/v1/sd-vae"},
		{http.MethodGet, "/sdapi/v1/loras"},
		{http.MethodGet, "/sdapi/v1/samplers"},
		{http.MethodGet, "/sdapi/v1/schedulers"},
		{http.MethodGet, "/sdapi/v1/upscalers"},
		{http.MethodPost, "/sdapi/v1/extra-single-image"},
		{http.MethodGet, "/sdapi/v1/memory"},
		{http.MethodPost, "/sdapi/v1/unload-checkpoint"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body any
			if tc.method == http.MethodPost {
				body = map[string]any{"sd_model_checkpoint": "sdxl_base.safetensors", "prompt": "a"}
			}
			rec := jcDo(t, e.f, tc.method, tc.path, body)
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		})
	}
	gens, _, unloads := e.sd.snapshot()
	assert.Empty(t, gens)
	assert.Zero(t, unloads)
	assert.Empty(t, e.rs.Calls())
}

func TestJobsLists(t *testing.T) {
	e := newJcEnv(t)
	type entry = map[string]any
	get := func(t *testing.T, path, key string) []any {
		t.Helper()
		rec := jcDo(t, e.f, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		m := jcMap(t, rec)
		l, ok := m[key].([]any)
		require.True(t, ok, "%s must be an array: %s", key, rec.Body.String())
		return l
	}
	strs := func(l []any) []string {
		out := []string{}
		for _, v := range l {
			s, ok := v.(string)
			if ok {
				out = append(out, s)
			}
		}
		return out
	}
	objs := func(t *testing.T, l []any) []entry {
		out := []entry{}
		for _, v := range l {
			o, ok := v.(entry)
			require.True(t, ok, "entry must be an object: %v", v)
			out = append(out, o)
		}
		return out
	}
	find := func(l []entry, key, val string) entry {
		for _, o := range l {
			if o[key] == val {
				return o
			}
		}
		return nil
	}

	t.Run("checkpoints", func(t *testing.T) {
		l := objs(t, get(t, "/checkpoints", "checkpoints"))
		o := find(l, "name", "sdxl_base.safetensors")
		require.NotNil(t, o, "%v", l)
		assert.Equal(t, filepath.Join(e.cfg.ModelsDir, "sdxl_base.safetensors"), o["file"])
	})
	t.Run("vaes", func(t *testing.T) {
		l := objs(t, get(t, "/vaes", "vaes"))
		o := find(l, "file", e.dirs.vae)
		require.NotNil(t, o, "%v", l)
		assert.NotEmpty(t, o["name"])
	})
	t.Run("loras", func(t *testing.T) {
		l := objs(t, get(t, "/loras", "loras"))
		for _, p := range []string{"detail.safetensors", "styles/sub.safetensors"} {
			o := find(l, "path", p)
			require.NotNil(t, o, "%s in %v", p, l)
			assert.NotEmpty(t, o["name"])
		}
	})
	t.Run("samplers", func(t *testing.T) {
		l := strs(get(t, "/samplers", "samplers"))
		assert.Contains(t, l, "euler")
		assert.Contains(t, l, "euler_a")
		assert.NotContains(t, l, "Euler a")
	})
	t.Run("schedulers", func(t *testing.T) {
		l := strs(get(t, "/schedulers", "schedulers"))
		assert.Contains(t, l, "")
		assert.Contains(t, l, "karras")
		assert.NotContains(t, l, "Karras")
		assert.NotContains(t, l, "Automatic")
	})
	t.Run("upscalers", func(t *testing.T) {
		l := objs(t, get(t, "/upscalers", "upscalers"))
		for _, n := range []string{"RealESRGAN_x4plus", "lanczos", "nearest"} {
			o := find(l, "name", n)
			require.NotNil(t, o, "%s in %v", n, l)
			_, ok := o["scale"].(float64)
			assert.True(t, ok, "scale of %s must be a number: %v", n, o["scale"])
		}
		for _, n := range []string{"R-ESRGAN 4x+", "Lanczos", "Nearest", "None", "RealESRGAN_x4plus.pth"} {
			assert.Nil(t, find(l, "name", n), n)
		}
	})
	t.Run("memory", func(t *testing.T) {
		rec := jcDo(t, e.f, http.MethodGet, "/memory", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got MemoryStats
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, e.mem.stats, got)
	})
}

func TestJobsStateAndUnload(t *testing.T) {
	e := newJcEnv(t)
	state := func() map[string]any {
		rec := jcDo(t, e.f, http.MethodGet, "/state", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return jcMap(t, rec)
	}
	assert.Equal(t, map[string]any{"loaded_checkpoint": "", "running_job": ""}, state())

	e.sd.hold.Store(true)
	id := jcID(jcSubmit(t, e.f, jcTxt("test", "", "sdxl_base.safetensors", "a"), http.StatusAccepted))
	jcWaitStatus(t, e.f, id, "running")
	assert.Equal(t, map[string]any{"loaded_checkpoint": "sdxl_base.safetensors", "running_job": id}, state())

	e.sd.hold.Store(false)
	jcWaitStatus(t, e.f, id, "completed")
	assert.True(t, jcWaitFor(2*time.Second, func() bool { return state()["running_job"] == "" }))
	assert.Equal(t, "sdxl_base.safetensors", state()["loaded_checkpoint"])

	rec := jcDo(t, e.f, http.MethodPost, "/unload", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, map[string]any{}, jcMap(t, rec))
	_, _, unloads := e.sd.snapshot()
	assert.Equal(t, 1, unloads)
	assert.Equal(t, "", state()["loaded_checkpoint"])
}

func TestJobsRejectsInvalidParams(t *testing.T) {
	with := func(body map[string]any, fn func(p map[string]any)) map[string]any {
		fn(body["params"].(map[string]any))
		return body
	}
	cases := []struct {
		name  string
		body  any
		codes []string
	}{
		{"malformed json", "{", []string{"invalid_params"}},
		{"unknown kind", jcJob("txt3img", "test", "", "sdxl_base.safetensors", jcGenParams("a")), []string{"invalid_params"}},
		{"forge sampler", with(jcTxt("test", "", "sdxl_base.safetensors", "a"), func(p map[string]any) { p["sampler"] = "Euler a" }), []string{"invalid_params"}},
		{"unknown sampler", with(jcTxt("test", "", "sdxl_base.safetensors", "a"), func(p map[string]any) { p["sampler"] = "nope" }), []string{"invalid_params"}},
		{"forge scheduler", with(jcTxt("test", "", "sdxl_base.safetensors", "a"), func(p map[string]any) { p["scheduler"] = "Karras" }), []string{"invalid_params"}},
		{"forge scheduler automatic", with(jcTxt("test", "", "sdxl_base.safetensors", "a"), func(p map[string]any) { p["scheduler"] = "Automatic" }), []string{"invalid_params"}},
		{"forge hires upscaler", with(jcTxt("test", "", "sdxl_base.safetensors", "a"), func(p map[string]any) {
			p["hires"] = map[string]any{"enable": true, "scale": 2.0, "upscaler": "R-ESRGAN 4x+", "denoising": 0.5, "steps": 0, "resize_x": 0, "resize_y": 0}
		}), []string{"invalid_params"}},
		{"forge upscale upscaler", jcJob("upscale", "test", "", "", jcUpscaleParams("R-ESRGAN 4x+")), []string{"invalid_params"}},
		{"forge builtin upscaler", jcJob("upscale", "test", "", "", jcUpscaleParams("Lanczos")), []string{"invalid_params"}},
		{"img2img without init image", with(jcJob("img2img", "test", "", "sdxl_base.safetensors", jcImg2ImgParams("a")), func(p map[string]any) { delete(p, "init_image") }), []string{"invalid_params"}},
		{"upscale without image", with(jcJob("upscale", "test", "", "", jcUpscaleParams("RealESRGAN_x4plus")), func(p map[string]any) { delete(p, "image") }), []string{"invalid_params"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newJcEnv(t)
			l := newJcLeaser(t, e.sd)
			e.f.SetLeaser(l.lease)
			rec := jcDo(t, e.f, http.MethodPost, "/jobs", tc.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, tc.codes, jcErrCode(t, rec))

			list := jcDo(t, e.f, http.MethodGet, "/jobs", nil)
			require.Equal(t, http.StatusOK, list.Code)
			var body struct {
				Jobs []map[string]any `json:"jobs"`
			}
			require.NoError(t, json.Unmarshal(list.Body.Bytes(), &body))
			assert.Empty(t, body.Jobs)
			assert.Empty(t, l.requests())
			gens, _, _ := e.sd.snapshot()
			assert.Empty(t, gens)
		})
	}
}

func TestJobsAcceptsSDCppNames(t *testing.T) {
	with := func(body map[string]any, fn func(p map[string]any)) map[string]any {
		fn(body["params"].(map[string]any))
		return body
	}
	cases := []struct {
		name string
		body map[string]any
	}{
		{"euler_a karras", jcTxt("test", "", "sdxl_base.safetensors", "a")},
		{"default scheduler", with(jcTxt("test", "", "sdxl_base.safetensors", "a"), func(p map[string]any) { p["sampler"] = "euler"; p["scheduler"] = "" })},
		{"hires with model upscaler", with(jcTxt("test", "", "sdxl_base.safetensors", "a"), func(p map[string]any) {
			p["hires"] = map[string]any{"enable": true, "scale": 2.0, "upscaler": "RealESRGAN_x4plus", "denoising": 0.5, "steps": 0, "resize_x": 0, "resize_y": 0}
		})},
		{"upscale model file", jcJob("upscale", "test", "", "", jcUpscaleParams("RealESRGAN_x4plus"))},
		{"upscale lanczos", jcJob("upscale", "test", "", "", jcUpscaleParams("lanczos"))},
		{"upscale nearest", jcJob("upscale", "test", "", "", jcUpscaleParams("nearest"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newJcEnv(t)
			id := jcID(jcSubmit(t, e.f, tc.body, http.StatusAccepted))
			jcWaitStatus(t, e.f, id, "completed")
			imgs, _ := jcResult(t, e.f, id)
			assert.Len(t, imgs, 1)
		})
	}
}

func TestJobsMissingModel(t *testing.T) {
	e := newJcEnv(t)
	rec := jcDo(t, e.f, http.MethodPost, "/jobs", jcTxt("test", "", "nope.safetensors", "a"))
	switch rec.Code {
	case http.StatusBadRequest:
		assert.Equal(t, "model_not_found", jcErrCode(t, rec))
	case http.StatusAccepted:
		st := jcWaitStatus(t, e.f, jcID(jcMap(t, rec)), "failed")
		errObj, ok := st["error"].(map[string]any)
		require.True(t, ok, "error: %v", st["error"])
		assert.Equal(t, "model_not_found", errObj["code"])
		assert.NotEmpty(t, errObj["message"])
	default:
		t.Fatalf("want 400 or 202, got %d: %s", rec.Code, rec.Body.String())
	}
	gens, _, _ := e.sd.snapshot()
	assert.Empty(t, gens)
}
