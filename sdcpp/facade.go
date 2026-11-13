package sdcpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Restarter interface {
	RestartWithArgs(args []string) error
}

type aliveChecker interface {
	Alive() bool
}

type Pool struct {
	Free  float64 `json:"free"`
	Used  float64 `json:"used"`
	Total float64 `json:"total"`
}

type CUDA struct {
	System Pool `json:"system"`
}

type MemoryStats struct {
	RAM  Pool  `json:"ram"`
	CUDA *CUDA `json:"cuda,omitempty"`
}

type MemoryReader interface {
	Memory() (MemoryStats, error)
}

type Config struct {
	BaseURL        string
	ModelsDir      string
	LoraDir        string
	VAEDir         string
	TextEncoderDir string
	UpscalerDir    string
	TokenizerDir   string
	BaseArgs       []string
	Aliases        map[string][]string
	PollInterval   time.Duration
	ReadyTimeout   time.Duration
	JobsDir        string
	JobTTL         time.Duration
	Now            func() time.Time
}

type LeaseRequest struct {
	JobID      string
	Client     string
	Checkpoint string
}

var ErrQueueFull = errors.New("queue full")

type Facade struct {
	cfg     Config
	restart Restarter
	mem     MemoryReader
	hc      *http.Client

	mu         sync.Mutex
	args       []string
	loadedName string
	loaded     ModelSpec
	pending    []string
	pendingAt  time.Time

	gen chan struct{}

	jobs   *jobStore
	leaser func(ctx context.Context, req LeaseRequest) (func(), error)
	posFn  func(jobID string) int
}

func New(cfg Config, r Restarter, mem MemoryReader) *Facade {
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 200 * time.Millisecond
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 60 * time.Second
	}
	if cfg.JobTTL <= 0 {
		cfg.JobTTL = 24 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	f := &Facade{cfg: cfg, restart: r, mem: mem, hc: &http.Client{}, gen: make(chan struct{}, 1)}
	f.jobs = newJobStore(cfg.JobsDir, cfg.JobTTL, cfg.Now)
	return f
}

func (f *Facade) SetLeaser(fn func(ctx context.Context, req LeaseRequest) (release func(), err error)) {
	f.mu.Lock()
	f.leaser = fn
	f.mu.Unlock()
}

func (f *Facade) SetPositionFunc(fn func(jobID string) int) {
	f.mu.Lock()
	f.posFn = fn
	f.mu.Unlock()
}

func (f *Facade) LoadedCheckpoint() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadedName
}

type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func apiErr(status int, code, format string, a ...any) error {
	return &apiError{status: status, code: code, msg: fmt.Sprintf(format, a...)}
}

func errorCode(err error, def string) string {
	var ae *apiError
	if errors.As(err, &ae) && ae.code != "" {
		return ae.code
	}
	return def
}

func respondJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func writeErr(w http.ResponseWriter, err error) {
	status, code := http.StatusBadGateway, "sd_failed"
	var ae *apiError
	if errors.As(err, &ae) {
		status, code = ae.status, ae.code
	}
	respondJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
}

func (f *Facade) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := "/" + strings.Trim(r.URL.Path, "/")
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	switch {
	case p == "/jobs" && post:
		f.submitJob(w, r)
	case p == "/jobs" && get:
		f.listJobs(w, r)
	case strings.HasPrefix(p, "/jobs/"):
		f.jobRoute(w, r, strings.TrimPrefix(p, "/jobs/"))
	case p == "/checkpoints" && get:
		f.checkpoints(w)
	case p == "/vaes" && get:
		f.vaes(w)
	case p == "/loras" && get:
		f.loras(w, r)
	case p == "/samplers" && get:
		respondJSON(w, http.StatusOK, map[string]any{"samplers": samplerNames})
	case p == "/schedulers" && get:
		respondJSON(w, http.StatusOK, map[string]any{"schedulers": append([]string{""}, schedulerNames...)})
	case p == "/upscalers" && get:
		f.upscalers(w)
	case p == "/memory" && get:
		f.memory(w)
	case p == "/state" && get:
		f.state(w)
	case p == "/unload" && post:
		f.unload(w, r)
	default:
		writeErr(w, apiErr(http.StatusNotFound, "not_found", "not found: %s %s", r.Method, p))
	}
}

func (f *Facade) jobRoute(w http.ResponseWriter, r *http.Request, rest string) {
	id, action, _ := strings.Cut(rest, "/")
	switch {
	case action == "" && r.Method == http.MethodGet:
		f.getJob(w, id)
	case action == "" && r.Method == http.MethodDelete:
		f.deleteJob(w, id)
	case action == "result" && r.Method == http.MethodGet:
		f.jobResult(w, id)
	case action == "cancel" && r.Method == http.MethodPost:
		f.cancelJobHTTP(w, id)
	default:
		writeErr(w, apiErr(http.StatusNotFound, "not_found", "not found: %s /jobs/%s", r.Method, rest))
	}
}

func (f *Facade) modelArgs(m ModelSpec) ([]string, error) {
	args, err := ModelArgs(f.cfg, m.Checkpoint, m.Modules)
	if err != nil {
		return nil, err
	}
	if usesSingleFile(args) && !slices.Contains(args, "--vae") {
		if v := strings.TrimSpace(m.VAE); v != "" {
			p := v
			if !filepath.IsAbs(p) {
				p = filepath.Join(f.cfg.VAEDir, v)
			}
			if !within(p, f.cfg.VAEDir, f.cfg.ModelsDir) {
				return nil, fmt.Errorf("vae %q is outside %s", v, f.cfg.VAEDir)
			}
			if !fileExists(p) {
				return nil, fmt.Errorf("vae %q not found on the server", v)
			}
			args = append(args, "--vae", p)
		}
	}
	return args, nil
}

func (f *Facade) checkModel(m ModelSpec) ([]string, error) {
	name := cleanName(m.Checkpoint)
	if name == "" {
		return nil, apiErr(http.StatusBadRequest, "invalid_params", "model.checkpoint is required")
	}
	if _, ok := lookupAlias(f.cfg, name); !ok {
		p, err := checkpointPath(f.cfg, name)
		if err != nil || !fileExists(p) {
			return nil, apiErr(http.StatusBadRequest, "model_not_found", "checkpoint %q not found on the server", m.Checkpoint)
		}
	}
	args, err := f.modelArgs(m)
	if err != nil {
		return nil, apiErr(http.StatusBadRequest, "invalid_params", "%v", err)
	}
	return args, nil
}

func (f *Facade) ensureModel(m ModelSpec) error {
	args, err := f.checkModel(m)
	if err != nil {
		return err
	}
	f.mu.Lock()
	same := f.args != nil && slices.Equal(args, f.args)
	f.mu.Unlock()
	if !same {
		if err := f.load(args); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.loadedName = m.Checkpoint
	f.loaded = ModelSpec{Checkpoint: m.Checkpoint, VAE: m.VAE, Modules: slices.Clone(m.Modules)}
	f.mu.Unlock()
	return nil
}

func (f *Facade) load(args []string) error {
	full := append(slices.Clone(f.cfg.BaseArgs), args...)
	f.mu.Lock()
	f.args = nil
	f.loadedName = ""
	f.loaded = ModelSpec{}
	f.mu.Unlock()
	if f.restart == nil {
		return apiErr(http.StatusInternalServerError, "sd_failed", "sd-server restart is not configured")
	}
	f.mu.Lock()
	resume := slices.Equal(f.pending, args) && time.Since(f.pendingAt) < loadGrace
	f.mu.Unlock()
	if ac, ok := f.restart.(aliveChecker); ok && resume && !ac.Alive() {
		resume = false
	}
	if !resume {
		if err := f.restart.RestartWithArgs(full); err != nil {
			return apiErr(http.StatusBadGateway, "sd_failed", "restart sd-server: %v", err)
		}
		f.mu.Lock()
		f.pending, f.pendingAt = slices.Clone(args), time.Now()
		f.mu.Unlock()
	}
	if err := f.waitReady(); err != nil {
		return err
	}
	f.mu.Lock()
	f.args = args
	f.pending = nil
	f.mu.Unlock()
	return nil
}

const loadGrace = 10 * time.Minute

func (f *Facade) waitReady() error {
	deadline := time.Now().Add(f.cfg.ReadyTimeout)
	var last string
	for {
		code, body, err := f.do(context.Background(), http.MethodGet, "/sdcpp/v1/capabilities", nil, 5*time.Second)
		switch {
		case err != nil:
			last = err.Error()
		case code == http.StatusOK:
			return nil
		default:
			last = fmt.Sprintf("capabilities HTTP %d: %s", code, truncate(body, 200))
		}
		if time.Now().After(deadline) {
			return apiErr(http.StatusBadGateway, "sd_failed", "sd-server not ready after %s: %s", f.cfg.ReadyTimeout, last)
		}
		time.Sleep(f.cfg.PollInterval)
	}
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

func (f *Facade) do(ctx context.Context, method, path string, body any, timeout time.Duration) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, f.cfg.BaseURL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

var loraTag = regexp.MustCompile(`<lora:([^:>]+)(?::([^:>]*))?(?::[^>]*)?>`)

type loraEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

var loraExts = map[string]bool{".gguf": true, ".pt": true, ".pth": true, ".safetensors": true, ".ckpt": true, ".sft": true}

func scanLoras(dir string) []loraEntry {
	out := []loraEntry{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() && loraExts[strings.ToLower(filepath.Ext(p))] {
			if rel, err := filepath.Rel(dir, p); err == nil {
				out = append(out, loraEntry{Name: strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())), Path: filepath.ToSlash(rel)})
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (f *Facade) fetchLoras(ctx context.Context) ([]loraEntry, error) {
	if f.cfg.LoraDir != "" {
		return scanLoras(f.cfg.LoraDir), nil
	}
	code, body, err := f.do(ctx, http.MethodGet, "/sdcpp/v1/capabilities", nil, 30*time.Second)
	if err != nil {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server unavailable: %v", err)
	}
	if code != http.StatusOK {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server capabilities HTTP %d: %s", code, truncate(body, 200))
	}
	var caps struct {
		Loras []loraEntry `json:"loras"`
	}
	if err := json.Unmarshal(body, &caps); err != nil {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "decode capabilities: %v", err)
	}
	if caps.Loras == nil {
		caps.Loras = []loraEntry{}
	}
	return caps.Loras, nil
}

func checkLoraWeights(prompt string) error {
	for _, m := range loraTag.FindAllStringSubmatch(prompt, -1) {
		if s := strings.TrimSpace(m[2]); s != "" {
			if _, err := strconv.ParseFloat(s, 64); err != nil {
				return apiErr(http.StatusBadRequest, "invalid_params", "LoRA %q: bad weight %q", strings.TrimSpace(m[1]), s)
			}
		}
	}
	return nil
}

func (f *Facade) extractLoras(ctx context.Context, prompt string) (string, []map[string]any, error) {
	matches := loraTag.FindAllStringSubmatch(prompt, -1)
	out := []map[string]any{}
	if len(matches) == 0 {
		return prompt, out, nil
	}
	if err := checkLoraWeights(prompt); err != nil {
		return "", nil, err
	}
	list, err := f.fetchLoras(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		mult := 1.0
		if s := strings.TrimSpace(m[2]); s != "" {
			mult, _ = strconv.ParseFloat(s, 64)
		}
		path := ""
		for _, l := range list {
			if l.Name == name || stem(l.Path) == name || l.Name == stem(name) {
				path = l.Path
				break
			}
		}
		if path == "" {
			log.Printf("[sdcpp] LoRA %q not found, skipped", name)
			continue
		}
		out = append(out, map[string]any{"path": path, "multiplier": mult})
	}
	clean := strings.Join(strings.Fields(loraTag.ReplaceAllString(prompt, " ")), " ")
	return clean, out, nil
}

func (f *Facade) resolveUpscaler(name string, hires bool) (string, error) {
	n := strings.TrimSpace(name)
	switch {
	case n == "" && hires, n == "latent" && hires:
		return "Latent", nil
	case n == "", n == "lanczos":
		return "Lanczos", nil
	case n == "nearest":
		return "Nearest", nil
	}
	for _, u := range f.upscalerFiles() {
		base := filepath.Base(u)
		if strings.TrimSuffix(base, filepath.Ext(base)) == n {
			return n, nil
		}
	}
	return "", apiErr(http.StatusBadRequest, "invalid_params", "upscaler %q is not available on this server", name)
}

func (f *Facade) upscalerFiles() []string {
	if f.cfg.UpscalerDir == "" {
		return nil
	}
	ents, err := os.ReadDir(f.cfg.UpscalerDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".pth", ".safetensors", ".bin", ".gguf":
			out = append(out, filepath.Join(f.cfg.UpscalerDir, e.Name()))
		}
	}
	return out
}

func (f *Facade) buildNative(ctx context.Context, p GenParams, img2img bool, m ModelSpec) (map[string]any, error) {
	prompt, loras, err := f.extractLoras(ctx, p.Prompt)
	if err != nil {
		return nil, err
	}
	steps := p.Steps
	if steps <= 0 {
		steps = 20
	}
	cfg := 7.0
	if p.CfgScale != nil {
		cfg = *p.CfgScale
	}
	guidance := map[string]any{"txt_cfg": cfg}
	if p.DistilledCfgScale != nil {
		guidance["distilled_guidance"] = *p.DistilledCfgScale
	}
	sp := map[string]any{"sample_steps": steps, "guidance": guidance}
	if p.Sampler != "" {
		sp["sample_method"] = p.Sampler
	}
	if p.Scheduler != "" {
		sp["scheduler"] = p.Scheduler
	}
	n := map[string]any{
		"prompt":          prompt,
		"negative_prompt": loraTag.ReplaceAllString(p.NegativePrompt, ""),
		"sample_params":   sp,
		"lora":            loras,
		"output_format":   "png",
		"preview":         "none",
	}
	if p.Width > 0 {
		n["width"] = p.Width
	}
	if p.Height > 0 {
		n["height"] = p.Height
	}
	if p.ClipSkip != nil && *p.ClipSkip > 1 {
		n["clip_skip"] = *p.ClipSkip
	}
	if img2img {
		n["init_image"] = p.InitImage
		strength := 0.75
		if p.DenoisingStrength != nil {
			strength = *p.DenoisingStrength
		}
		n["strength"] = strength
		if m.KleinReference {
			n["ref_images"] = []string{p.InitImage}
		}
		if p.Mask != "" {
			n["mask_image"] = p.Mask
		}
	} else if p.Hires != nil && p.Hires.Enable {
		up, err := f.resolveUpscaler(p.Hires.Upscaler, true)
		if err != nil {
			return nil, err
		}
		h := map[string]any{"enabled": true, "upscaler": up, "target_width": p.Hires.ResizeX, "target_height": p.Hires.ResizeY, "steps": p.Hires.Steps}
		if p.Hires.Scale != nil {
			h["scale"] = *p.Hires.Scale
		}
		if p.Hires.Denoising != nil {
			h["denoising_strength"] = *p.Hires.Denoising
		}
		n["hires"] = h
	}
	return n, nil
}

func (f *Facade) generate(ctx context.Context, j *job, m ModelSpec, p GenParams) ([]string, map[string]any, error) {
	if err := f.ensureModel(m); err != nil {
		return nil, nil, err
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	img2img := j.kind == kindImg2Img
	native, err := f.buildNative(ctx, p, img2img, m)
	if err != nil {
		return nil, nil, err
	}
	batch := p.BatchSize
	if batch <= 0 {
		batch = 1
	}
	native["batch_count"] = batch
	seed := int64(-1)
	if p.Seed != nil {
		seed = *p.Seed
	}
	if seed < 0 {
		seed = rand.Int64N(1 << 32)
	}
	native["seed"] = seed
	imgs, err := f.runNative(ctx, native, j)
	if err != nil {
		return nil, nil, err
	}
	seeds := make([]int64, len(imgs))
	for i := range imgs {
		seeds[i] = seed + int64(i)
	}
	steps := p.Steps
	if steps <= 0 {
		steps = 20
	}
	cfgScale := 7.0
	if p.CfgScale != nil {
		cfgScale = *p.CfgScale
	}
	width, height := p.Width, p.Height
	if (width <= 0 || height <= 0) && len(imgs) > 0 {
		if w, h, err := imageSizeB64(imgs[0]); err == nil {
			width, height = w, h
		} else {
			log.Printf("[sdcpp] job %s: read result size: %v", j.st.ID, err)
		}
	}
	info := map[string]any{
		"prompt":          p.Prompt,
		"negative_prompt": p.NegativePrompt,
		"seed":            seed,
		"all_seeds":       seeds,
		"width":           width,
		"height":          height,
		"sampler":         p.Sampler,
		"scheduler":       p.Scheduler,
		"steps":           steps,
		"cfg_scale":       cfgScale,
		"batch_size":      batch,
		"checkpoint":      m.Checkpoint,
		"vae":             m.VAE,
		"modules":         m.Modules,
		"sd_model_name":   stem(cleanName(m.Checkpoint)),
		"backend":         "sd.cpp",
	}
	if img2img {
		strength := 0.75
		if p.DenoisingStrength != nil {
			strength = *p.DenoisingStrength
		}
		info["denoising_strength"] = strength
	}
	return imgs, info, nil
}

type nativeJob struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Progress struct {
		Completed *int   `json:"completed"`
		Total     *int   `json:"total"`
		Unit      string `json:"unit"`
	} `json:"progress"`
	Result *struct {
		Images []struct {
			B64 string `json:"b64_json"`
		} `json:"images"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (f *Facade) runNative(ctx context.Context, native map[string]any, j *job) ([]string, error) {
	code, body, err := f.do(ctx, http.MethodPost, "/sdcpp/v1/img_gen", native, 60*time.Second)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server unavailable: %v", err)
	}
	if code != http.StatusAccepted && code != http.StatusOK {
		return nil, submitError(code, body)
	}
	var sub nativeJob
	if err := json.Unmarshal(body, &sub); err != nil || sub.ID == "" {
		return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server img_gen: bad response: %s", truncate(body, 200))
	}
	fails := 0
	for {
		select {
		case <-ctx.Done():
			f.cancelNative(sub.ID)
			return nil, ctx.Err()
		case <-time.After(f.cfg.PollInterval):
		}
		code, body, err := f.do(ctx, http.MethodGet, "/sdcpp/v1/jobs/"+sub.ID, nil, 30*time.Second)
		if ctx.Err() != nil {
			f.cancelNative(sub.ID)
			return nil, ctx.Err()
		}
		if err != nil || code >= 500 {
			fails++
			if fails >= 5 {
				if err != nil {
					return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server unavailable while generating: %v", err)
				}
				return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server job HTTP %d: %s", code, truncate(body, 200))
			}
			continue
		}
		fails = 0
		if code != http.StatusOK {
			return nil, apiErr(http.StatusBadGateway, "sd_failed", "sd-server job %s HTTP %d: %s", sub.ID, code, truncate(body, 200))
		}
		var nj nativeJob
		if err := json.Unmarshal(body, &nj); err != nil {
			return nil, apiErr(http.StatusBadGateway, "sd_failed", "decode job: %v", err)
		}
		step, steps := 0, 0
		if nj.Progress.Unit == "steps" && nj.Progress.Total != nil && *nj.Progress.Total > 0 && nj.Progress.Completed != nil {
			step, steps = *nj.Progress.Completed, *nj.Progress.Total
		}
		f.jobs.markRunning(j, step, steps)
		switch nj.Status {
		case "completed":
			imgs := []string{}
			if nj.Result != nil {
				for _, im := range nj.Result.Images {
					imgs = append(imgs, im.B64)
				}
			}
			return imgs, nil
		case "failed":
			msg := "generation failed"
			if nj.Error != nil && nj.Error.Message != "" {
				msg = nj.Error.Message
			}
			return nil, apiErr(http.StatusInternalServerError, "sd_failed", "sd.cpp: %s", msg)
		case "cancelled":
			return nil, apiErr(http.StatusInternalServerError, "cancelled", "sd.cpp job cancelled")
		}
	}
}

func submitError(code int, body []byte) error {
	var e struct {
		Error any `json:"error"`
	}
	msg := truncate(body, 400)
	if json.Unmarshal(body, &e) == nil && e.Error != nil {
		switch v := e.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				msg = m
			}
		}
	}
	return apiErr(http.StatusBadGateway, "sd_failed", "sd.cpp HTTP %d: %s", code, msg)
}

func (f *Facade) cancelNative(id string) {
	if id == "" {
		return
	}
	if _, _, err := f.do(context.Background(), http.MethodPost, "/sdcpp/v1/jobs/"+id+"/cancel", nil, 10*time.Second); err != nil {
		log.Printf("[sdcpp] cancel %s: %v", id, err)
	}
}

func (f *Facade) state(w http.ResponseWriter) {
	respondJSON(w, http.StatusOK, map[string]string{
		"loaded_checkpoint": f.LoadedCheckpoint(),
		"running_job":       f.jobs.runningID(),
	})
}

func (f *Facade) unload(w http.ResponseWriter, r *http.Request) {
	select {
	case f.gen <- struct{}{}:
	default:
		writeErr(w, apiErr(http.StatusConflict, "not_ready", "generation in progress"))
		return
	}
	defer func() { <-f.gen }()
	code, body, err := f.do(r.Context(), http.MethodPost, "/sdcpp/v1/models/unload", map[string]any{}, 60*time.Second)
	if err != nil {
		writeErr(w, apiErr(http.StatusBadGateway, "sd_failed", "sd-server unavailable: %v", err))
		return
	}
	if code >= 300 && code != http.StatusServiceUnavailable {
		writeErr(w, apiErr(http.StatusBadGateway, "sd_failed", "sd-server unload HTTP %d: %s", code, truncate(body, 200)))
		return
	}
	f.mu.Lock()
	f.args = nil
	f.loadedName = ""
	f.pending = nil
	f.mu.Unlock()
	respondJSON(w, http.StatusOK, map[string]any{})
}

func (f *Facade) memory(w http.ResponseWriter) {
	if f.mem == nil {
		respondJSON(w, http.StatusOK, MemoryStats{})
		return
	}
	m, err := f.mem.Memory()
	if err != nil {
		writeErr(w, apiErr(http.StatusBadGateway, "sd_failed", "memory: %v", err))
		return
	}
	respondJSON(w, http.StatusOK, m)
}
