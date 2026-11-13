package sdcpp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
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

func imageSize(t *testing.T, s string) (int, int) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	require.NoError(t, err)
	img, _, err := image.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	return img.Bounds().Dx(), img.Bounds().Dy()
}

type fakeJob struct {
	id    string
	batch int
}

type fakeSD struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	bodies   []map[string]any
	jobs     map[string]*fakeJob
	upscales []map[string]any
	capGets  int
	notReady int
	crash    bool
	loras    []map[string]any

	hold atomic.Bool
}

func newFakeSD(t *testing.T) *fakeSD {
	t.Helper()
	f := &fakeSD{t: t, jobs: map[string]*fakeJob{}, loras: []map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sdcpp/v1/img_gen", f.handleImgGen)
	mux.HandleFunc("GET /sdcpp/v1/jobs/{id}", f.handleJob)
	mux.HandleFunc("POST /sdcpp/v1/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{}) })
	mux.HandleFunc("POST /sdcpp/v1/upscale", f.handleUpscale)
	mux.HandleFunc("POST /sdcpp/v1/models/unload", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{}) })
	mux.HandleFunc("GET /sdcpp/v1/capabilities", f.handleCapabilities)
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
	f.bodies = append(f.bodies, body)
	batch := 1
	if v, ok := body["batch_count"].(float64); ok && v > 0 {
		batch = int(v)
	}
	id := fmt.Sprintf("job_%d", len(f.bodies))
	f.jobs[id] = &fakeJob{id: id, batch: batch}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "queued"})
}

func (f *fakeSD) handleJob(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	crash := f.crash
	job := f.jobs[r.PathValue("id")]
	f.mu.Unlock()
	if crash {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
		return
	}
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if f.hold.Load() {
		writeJSON(w, http.StatusOK, map[string]any{"id": job.id, "status": "generating"})
		return
	}
	images := make([]map[string]any, job.batch)
	for i := range images {
		images[i] = map[string]any{"index": i, "b64_json": pngB64(f.t, 8, 8)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": job.id, "status": "completed", "result": map[string]any{"images": images}})
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
	raw, _ := base64.StdEncoding.DecodeString(src)
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unreadable image"})
		return
	}
	b := img.Bounds()
	writeJSON(w, http.StatusOK, map[string]any{"images": []map[string]any{{"index": 0, "b64_json": pngB64(f.t, b.Dx()*4, b.Dy()*4)}}})
}

func (f *fakeSD) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capGets++
	if f.notReady > 0 {
		f.notReady--
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "loading"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"loras": f.loras})
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

func (r *fakeRestarter) setHook(fn func()) {
	r.mu.Lock()
	r.onRestart = fn
	r.mu.Unlock()
}

func (r *fakeRestarter) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

type testEnv struct {
	sd   *fakeSD
	rs   *fakeRestarter
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
	return &testEnv{sd: sd, rs: rs, dirs: d, cfg: cfg, f: New(cfg, rs, nil)}
}

func (e *testEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	rec := httptest.NewRecorder()
	e.f.ServeHTTP(rec, httptest.NewRequest(method, path, rd))
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), "body: %s", rec.Body.String())
	return m
}

func sub(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	require.True(t, ok, "%s must be an object in %v", key, m)
	return v
}

func model(checkpoint string, modules ...string) map[string]any {
	if modules == nil {
		modules = []string{}
	}
	return map[string]any{"checkpoint": checkpoint, "modules": modules}
}

func jobBody(kind string, m map[string]any, params map[string]any) map[string]any {
	b := map[string]any{"kind": kind, "client": "test", "params": params}
	if m != nil {
		b["model"] = m
	}
	return b
}

func txt(params map[string]any) map[string]any {
	return jobBody("txt2img", model("sdxl_base.safetensors"), params)
}

func (e *testEnv) submit(t *testing.T, body map[string]any) (int, map[string]any) {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/jobs", body)
	return rec.Code, decodeMap(t, rec)
}

func (e *testEnv) wait(t *testing.T, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := decodeMap(t, e.do(t, http.MethodGet, "/jobs/"+id, nil))
		if terminal(st["status"].(string)) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return nil
}

func (e *testEnv) run(t *testing.T, body map[string]any) (map[string]any, []string, map[string]any) {
	t.Helper()
	code, st := e.submit(t, body)
	require.Equal(t, http.StatusAccepted, code, st)
	st = e.wait(t, st["id"].(string))
	if st["status"] != statusCompleted {
		return st, nil, nil
	}
	res := decodeMap(t, e.do(t, http.MethodGet, "/jobs/"+st["id"].(string)+"/result", nil))
	var imgs []string
	for _, v := range res["images"].([]any) {
		imgs = append(imgs, v.(string))
	}
	return st, imgs, sub(t, res, "info")
}

func (e *testEnv) kleinModel() map[string]any {
	return model("flux-2-klein-4b.safetensors", e.dirs.te, e.dirs.vae)
}

func TestJobModel_RestartsWithBaseAndModelArgs(t *testing.T) {
	cases := []struct {
		name  string
		model func(e *testEnv) map[string]any
		want  func(e *testEnv) map[string]string
	}{
		{
			"sdxl",
			func(e *testEnv) map[string]any { return model("sdxl_base.safetensors") },
			func(e *testEnv) map[string]string {
				return map[string]string{"--model": filepath.Join(e.cfg.ModelsDir, "sdxl_base.safetensors")}
			},
		},
		{
			"klein",
			func(e *testEnv) map[string]any { return e.kleinModel() },
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
			func(e *testEnv) map[string]any { return model("moss_v1.safetensors", e.dirs.te, e.dirs.vae) },
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
			func(e *testEnv) map[string]any { return model("z-image-turbo-aio.safetensors") },
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
			st, _, _ := e.run(t, jobBody("txt2img", tc.model(e), map[string]any{"prompt": "p"}))
			require.Equal(t, statusCompleted, st["status"], st)
			calls := e.rs.Calls()
			require.Len(t, calls, 1)
			assert.Equal(t, testBaseArgs, calls[0][:len(testBaseArgs)])
			assert.Equal(t, tc.want(e), flagValues(t, calls[0][len(testBaseArgs):]))
		})
	}
}

func TestJobModel_RestartOnlyWhenArgsChange(t *testing.T) {
	e := newEnv(t)
	otherVAE := touch(t, filepath.Join(e.cfg.VAEDir, "ae.safetensors"))
	steps := []struct {
		name      string
		model     map[string]any
		wantCalls int
	}{
		{"first model", model("sdxl_base.safetensors"), 1},
		{"same model again", model("sdxl_base.safetensors"), 1},
		{"same model with klein flag", map[string]any{"checkpoint": "sdxl_base.safetensors", "klein_reference": true}, 1},
		{"other model", model("flux1-dev.gguf"), 2},
		{"klein", e.kleinModel(), 3},
		{"klein same modules", e.kleinModel(), 3},
		{"klein other vae", model("flux-2-klein-4b.safetensors", e.dirs.te, otherVAE), 4},
	}
	for _, s := range steps {
		st, _, _ := e.run(t, jobBody("txt2img", s.model, map[string]any{"prompt": "p"}))
		require.Equal(t, statusCompleted, st["status"], "%s: %v", s.name, st)
		assert.Len(t, e.rs.Calls(), s.wantCalls, s.name)
	}
}

func TestJobModel_ModelArgsError400(t *testing.T) {
	for _, ckpt := range []string{"z-image-turbo-aio.safetensors", "flux-2-klein-4b.safetensors", "moss_v1.safetensors"} {
		t.Run(ckpt, func(t *testing.T) {
			e := newEnv(t)
			code, m := e.submit(t, jobBody("txt2img", model(ckpt), map[string]any{"prompt": "p"}))
			require.Equal(t, http.StatusBadRequest, code, m)
			assert.Equal(t, "invalid_params", sub(t, m, "error")["code"])
			assert.Empty(t, e.rs.Calls())
		})
	}
}

func TestJobModel_WaitsForReadyAfterRestart(t *testing.T) {
	e := newEnv(t)
	var before int
	e.rs.setHook(func() {
		e.sd.set(func(f *fakeSD) {
			before = f.capGets
			f.notReady = 3
		})
	})
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusCompleted, st["status"], st)
	var gets, left int
	e.sd.set(func(f *fakeSD) { gets, left = f.capGets, f.notReady })
	assert.Equal(t, 0, left)
	assert.GreaterOrEqual(t, gets-before, 4)
}

func TestJobModel_VAE(t *testing.T) {
	restartVAEs := func(t *testing.T, e *testEnv, m map[string]any) []string {
		st, _, _ := e.run(t, jobBody("txt2img", m, map[string]any{"prompt": "p"}))
		require.Equal(t, statusCompleted, st["status"], st)
		calls := e.rs.Calls()
		require.Len(t, calls, 1)
		args := calls[0][len(testBaseArgs):]
		var vaes []string
		for i, a := range args {
			if a == "--vae" {
				vaes = append(vaes, args[i+1])
			}
		}
		assert.Equal(t, filepath.Join(e.cfg.ModelsDir, "sdxl_base.safetensors"), flagValues(t, args)["--model"])
		return vaes
	}
	t.Run("module VAE wins over vae", func(t *testing.T) {
		for _, name := range []string{"absolute", "file name"} {
			e := newEnv(t)
			touch(t, filepath.Join(e.cfg.VAEDir, "sdxl_vae.safetensors"))
			mod := e.dirs.vae
			if name == "file name" {
				mod = filepath.Base(mod)
			}
			m := model("sdxl_base.safetensors", mod)
			m["vae"] = "sdxl_vae.safetensors"
			assert.Equal(t, []string{e.dirs.vae}, restartVAEs(t, e, m), name)
		}
	})
	t.Run("vae without modules", func(t *testing.T) {
		e := newEnv(t)
		sdVAE := touch(t, filepath.Join(e.cfg.VAEDir, "sdxl_vae.safetensors"))
		m := model("sdxl_base.safetensors")
		m["vae"] = "sdxl_vae.safetensors"
		assert.Equal(t, []string{sdVAE}, restartVAEs(t, e, m))
	})
	t.Run("vae outside rejected", func(t *testing.T) {
		e := newEnv(t)
		m := model("sdxl_base.safetensors")
		m["vae"] = "../../../etc/x.safetensors"
		code, body := e.submit(t, jobBody("txt2img", m, map[string]any{"prompt": "p"}))
		assert.Equal(t, http.StatusBadRequest, code, body)
		assert.Empty(t, e.rs.Calls())
	})
}

func TestTxt2Img_BasicMapping(t *testing.T) {
	e := newEnv(t)
	st, imgs, info := e.run(t, txt(map[string]any{
		"prompt": "a cat on a chair", "negative_prompt": "blurry", "width": 512, "height": 768,
		"steps": 20, "cfg_scale": 6.5, "seed": 42, "sampler": "dpm++2m", "scheduler": "karras",
	}))
	require.Equal(t, statusCompleted, st["status"], st)
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
	require.Len(t, imgs, 1)
	w, h := imageSize(t, imgs[0])
	assert.Equal(t, 8, w)
	assert.Equal(t, 8, h)
	assert.EqualValues(t, 42, info["seed"])
	assert.Equal(t, "dpm++2m", info["sampler"])
}

func TestTxt2Img_DefaultSchedulerNotSent(t *testing.T) {
	e := newEnv(t)
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "p", "sampler": "euler", "scheduler": ""}))
	require.Equal(t, statusCompleted, st["status"], st)
	sp := sub(t, e.sd.imgGenBodies()[0], "sample_params")
	assert.Equal(t, "euler", sp["sample_method"])
	_, present := sp["scheduler"]
	assert.False(t, present)
}

func loraOf(t *testing.T, b map[string]any) []map[string]any {
	t.Helper()
	got := []map[string]any{}
	if v, ok := b["lora"].([]any); ok {
		for _, r := range v {
			m := r.(map[string]any)
			got = append(got, map[string]any{"path": m["path"], "multiplier": m["multiplier"]})
		}
	}
	return got
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
		{"only lora tag", "<lora:detail:1>", []map[string]any{{"path": "detail.safetensors", "multiplier": 1.0}}, nil},
		{"missing lora dropped", "a cat <lora:ghost:0.7> on a chair <lora:detail:0.8>", []map[string]any{{"path": "detail.safetensors", "multiplier": 0.8}}, []string{"a cat", "on a chair"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			st, _, info := e.run(t, txt(map[string]any{"prompt": tc.prompt, "seed": 1}))
			require.Equal(t, statusCompleted, st["status"], st)
			b := e.sd.imgGenBodies()[0]
			prompt, _ := b["prompt"].(string)
			assert.NotContains(t, prompt, "<lora")
			assert.NotContains(t, prompt, "ghost")
			for _, part := range tc.wantParts {
				assert.Contains(t, prompt, part)
			}
			if tc.wantParts == nil {
				assert.Empty(t, strings.TrimSpace(prompt))
			}
			assert.ElementsMatch(t, tc.wantLora, loraOf(t, b))
			assert.Equal(t, tc.prompt, info["prompt"])
		})
	}
}

func TestTxt2Img_LorasFromSDServerWithoutLoraDir(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.LoraDir = "" })
	e.sd.set(func(f *fakeSD) { f.loras = []map[string]any{{"name": "detail", "path": "detail.safetensors"}} })
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "a <lora:detail:0.5>"}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.Equal(t, []map[string]any{{"path": "detail.safetensors", "multiplier": 0.5}}, loraOf(t, e.sd.imgGenBodies()[0]))
	rec := e.do(t, http.MethodGet, "/loras", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"detail.safetensors"`)
}

func TestLorasNamedWithoutAnyExtension(t *testing.T) {
	e := newEnv(t)
	touch(t, filepath.Join(e.cfg.LoraDir, "old.pt"))
	touch(t, filepath.Join(e.cfg.LoraDir, "x.sft"))
	var body struct {
		Loras []loraEntry `json:"loras"`
	}
	require.NoError(t, json.Unmarshal(e.do(t, http.MethodGet, "/loras", nil).Body.Bytes(), &body))
	names := map[string]bool{}
	for _, l := range body.Loras {
		names[l.Name] = true
	}
	assert.True(t, names["old"], names)
	assert.True(t, names["x"], names)
}

func TestTxt2Img_BadLoraWeight400(t *testing.T) {
	e := newEnv(t)
	code, m := e.submit(t, txt(map[string]any{"prompt": "a <lora:detail:abc>"}))
	assert.Equal(t, http.StatusBadRequest, code, m)
}

func TestTxt2Img_BatchAndSeed(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		seed   float64
		batch  float64
	}{
		{"batch", map[string]any{"prompt": "p", "seed": 7, "batch_size": 3}, 7, 3},
		{"random seed", map[string]any{"prompt": "p", "seed": -1, "batch_size": 2}, -1, 2},
		{"seed missing", map[string]any{"prompt": "p"}, -1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			st, imgs, info := e.run(t, txt(tc.params))
			require.Equal(t, statusCompleted, st["status"], st)
			b := e.sd.imgGenBodies()[0]
			assert.EqualValues(t, tc.batch, b["batch_count"])
			assert.Len(t, imgs, int(tc.batch))
			seed, _ := b["seed"].(float64)
			if tc.seed < 0 {
				assert.GreaterOrEqual(t, seed, 0.0)
			} else {
				assert.Equal(t, tc.seed, seed)
			}
			assert.EqualValues(t, seed, info["seed"])
			assert.Len(t, info["all_seeds"], int(tc.batch))
		})
	}
}

func TestTxt2Img_Hires(t *testing.T) {
	cases := []struct {
		name  string
		hires map[string]any
		want  map[string]any
	}{
		{"latent default", map[string]any{"enable": true, "upscaler": "", "scale": 2, "steps": 12, "denoising": 0.45},
			map[string]any{"upscaler": "Latent", "scale": 2.0, "steps": 12.0, "denoising_strength": 0.45, "target_width": 0.0, "target_height": 0.0}},
		{"model upscaler with target size", map[string]any{"enable": true, "upscaler": "RealESRGAN_x4plus", "resize_x": 1024, "resize_y": 1536, "steps": 8, "denoising": 0.3},
			map[string]any{"upscaler": "RealESRGAN_x4plus", "target_width": 1024.0, "target_height": 1536.0}},
		{"lanczos", map[string]any{"enable": true, "upscaler": "lanczos"}, map[string]any{"upscaler": "Lanczos"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			st, _, _ := e.run(t, txt(map[string]any{"prompt": "p", "hires": tc.hires}))
			require.Equal(t, statusCompleted, st["status"], st)
			hr := sub(t, e.sd.imgGenBodies()[0], "hires")
			assert.Equal(t, true, hr["enabled"])
			for k, v := range tc.want {
				assert.Equal(t, v, hr[k], k)
			}
		})
	}
}

func TestTxt2Img_NoHiresWhenDisabled(t *testing.T) {
	e := newEnv(t)
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "p", "hires": map[string]any{"enable": false, "steps": 12}}))
	require.Equal(t, statusCompleted, st["status"], st)
	_, present := e.sd.imgGenBodies()[0]["hires"]
	assert.False(t, present)
}

func TestGeneration_ClipSkipDefaultNotSent(t *testing.T) {
	cases := []struct {
		name     string
		clipSkip any
		want     any
	}{
		{"absent", nil, nil},
		{"zero", 0, nil},
		{"one", 1, nil},
		{"two", 2, float64(2)},
	}
	for _, kind := range []string{"txt2img", "img2img"} {
		for _, tc := range cases {
			t.Run(kind+" "+tc.name, func(t *testing.T) {
				e := newEnv(t)
				p := map[string]any{"prompt": "a cat", "seed": 1}
				if tc.clipSkip != nil {
					p["clip_skip"] = tc.clipSkip
				}
				if kind == "img2img" {
					p["init_image"] = pngB64(t, 8, 8)
				}
				st, _, _ := e.run(t, jobBody(kind, model("sdxl_base.safetensors"), p))
				require.Equal(t, statusCompleted, st["status"], st)
				got, present := e.sd.imgGenBodies()[0]["clip_skip"]
				if tc.want == nil {
					assert.False(t, present, "clip_skip must not be sent, got %v", got)
				} else {
					assert.Equal(t, tc.want, got)
				}
			})
		}
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
			init := pngB64(t, 8, 8)
			p := map[string]any{"prompt": "edit", "seed": 9, "init_image": init, "mask": ""}
			if tc.strength != nil {
				p["denoising_strength"] = tc.strength
			}
			m := model("sdxl_base.safetensors")
			m["klein_reference"] = tc.kleinRef
			st, imgs, info := e.run(t, jobBody("img2img", m, p))
			require.Equal(t, statusCompleted, st["status"], st)
			b := e.sd.imgGenBodies()[0]
			assert.Equal(t, init, b["init_image"])
			assert.EqualValues(t, tc.wantStrength, b["strength"])
			_, hasMask := b["mask_image"]
			assert.False(t, hasMask)
			if tc.kleinRef {
				assert.Equal(t, []any{init}, b["ref_images"])
			} else {
				_, present := b["ref_images"]
				assert.False(t, present)
			}
			assert.Len(t, imgs, 1)
			assert.EqualValues(t, 9, info["seed"])
		})
	}
}

func TestImg2Img_MossReferenceRejected(t *testing.T) {
	e := newEnv(t)
	m := model("sdxl_base.safetensors")
	m["moss_reference"] = true
	code, body := e.submit(t, jobBody("img2img", m, map[string]any{"prompt": "edit", "init_image": pngB64(t, 8, 8)}))
	require.Equal(t, http.StatusBadRequest, code, body)
	assert.Contains(t, sub(t, body, "error")["message"], "Klein")
	assert.Empty(t, e.sd.imgGenBodies())
}

func TestGeneration_ServerCrashFailsJob(t *testing.T) {
	e := newEnv(t)
	e.sd.hold.Store(true)
	code, st := e.submit(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, http.StatusAccepted, code, st)
	require.Eventually(t, func() bool { return len(e.sd.imgGenBodies()) == 1 }, 2*time.Second, 5*time.Millisecond)
	e.sd.set(func(f *fakeSD) { f.crash = true })
	st = e.wait(t, st["id"].(string))
	assert.Equal(t, statusFailed, st["status"])
	assert.Equal(t, "sd_failed", sub(t, st, "error")["code"])
}

func TestGeneration_ServerDownFailsJob(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ReadyTimeout = 50 * time.Millisecond })
	e.sd.srv.Close()
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "p"}))
	assert.Equal(t, statusFailed, st["status"])
	assert.NotEmpty(t, sub(t, st, "error")["message"])
}

func TestUpscaleJob(t *testing.T) {
	cases := []struct {
		name         string
		upscaler     string
		scale        float64
		wantW, wantH int
		wantUpscales int
	}{
		{"model upscaler resized to x2", "RealESRGAN_x4plus", 2, 16, 12, 1},
		{"model upscaler x4", "RealESRGAN_x4plus", 4, 32, 24, 1},
		{"lanczos local", "lanczos", 2, 16, 12, 0},
		{"nearest local", "nearest", 3, 24, 18, 0},
		{"empty means lanczos", "", 2, 16, 12, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			src := pngB64(t, 8, 6)
			st, imgs, _ := e.run(t, jobBody("upscale", nil, map[string]any{"image": src, "upscaler": tc.upscaler, "scale": tc.scale}))
			require.Equal(t, statusCompleted, st["status"], st)
			require.Len(t, imgs, 1)
			w, h := imageSize(t, imgs[0])
			assert.Equal(t, tc.wantW, w)
			assert.Equal(t, tc.wantH, h)
			var ups []map[string]any
			e.sd.set(func(f *fakeSD) { ups = append(ups, f.upscales...) })
			require.Len(t, ups, tc.wantUpscales)
			if tc.wantUpscales > 0 {
				assert.Equal(t, tc.upscaler, ups[0]["upscaler"])
				assert.Equal(t, src, ups[0]["image"])
			}
			assert.Empty(t, e.rs.Calls())
		})
	}
}

func TestLoadTimeoutDoesNotRestartAgain(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ReadyTimeout = 50 * time.Millisecond })
	e.rs.setHook(func() { e.sd.set(func(f *fakeSD) { f.notReady = 1 << 30 }) })
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusFailed, st["status"], st)
	e.sd.set(func(f *fakeSD) { f.notReady = 0 })
	st, _, _ = e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.Len(t, e.rs.Calls(), 1)
}

func TestUnloadClearsPendingLoad(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ReadyTimeout = 50 * time.Millisecond })
	e.rs.setHook(func() { e.sd.set(func(f *fakeSD) { f.notReady = 1 << 30 }) })
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusFailed, st["status"], st)
	e.sd.set(func(f *fakeSD) { f.notReady = 0 })
	require.Equal(t, http.StatusOK, e.do(t, http.MethodPost, "/unload", nil).Code)
	e.rs.setHook(nil)
	st, _, _ = e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.Len(t, e.rs.Calls(), 2)
}

type deadRestarter struct{ fakeRestarter }

func (*deadRestarter) Alive() bool { return false }

func TestLoadRestartsWhenProcessDied(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ReadyTimeout = 50 * time.Millisecond })
	rs := &deadRestarter{}
	rs.setHook(func() { e.sd.set(func(f *fakeSD) { f.notReady = 1 << 30 }) })
	e.f = New(e.cfg, rs, nil)
	st, _, _ := e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusFailed, st["status"], st)
	rs.setHook(func() { e.sd.set(func(f *fakeSD) { f.notReady = 0 }) })
	st, _, _ = e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.Len(t, rs.Calls(), 2)
}

func TestUnloadConflictWhileGenerating(t *testing.T) {
	e := newEnv(t)
	e.sd.hold.Store(true)
	code, st := e.submit(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, http.StatusAccepted, code)
	require.Eventually(t, func() bool { return len(e.sd.imgGenBodies()) == 1 }, 2*time.Second, 5*time.Millisecond)
	rec := e.do(t, http.MethodPost, "/unload", nil)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	e.sd.hold.Store(false)
	assert.Equal(t, statusCompleted, e.wait(t, st["id"].(string))["status"])
}

func TestInMemoryJobsWithoutJobsDir(t *testing.T) {
	e := newEnv(t)
	st, imgs, _ := e.run(t, txt(map[string]any{"prompt": "p"}))
	require.Equal(t, statusCompleted, st["status"], st)
	assert.Len(t, imgs, 1)
	assert.Equal(t, http.StatusNotFound, e.do(t, http.MethodGet, "/jobs/"+st["id"].(string)+"/x", nil).Code)
}
