package sdcpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	StatePath      string
	PollInterval   time.Duration
	ReadyTimeout   time.Duration
}

type state struct {
	Checkpoint string   `json:"sd_model_checkpoint"`
	Modules    []string `json:"forge_additional_modules"`
	KleinRef   bool     `json:"klein_do_reference"`
	MossRef    string   `json:"moss_do_reference"`
	VAE        string   `json:"sd_vae"`
}

type jobInfo struct {
	id        string
	total     int
	no        int
	frac      float64
	step      int
	steps     int
	cancelled bool
}

type Facade struct {
	cfg     Config
	restart Restarter
	mem     MemoryReader
	hc      *http.Client

	mu   sync.Mutex
	st   state
	args []string
	job  *jobInfo

	gen   sync.Mutex
	optMu sync.Mutex

	pending   []string
	pendingAt time.Time
}

func New(cfg Config, r Restarter, mem MemoryReader) *Facade {
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 200 * time.Millisecond
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 60 * time.Second
	}
	f := &Facade{cfg: cfg, restart: r, mem: mem, hc: &http.Client{}, st: state{MossRef: "Disable", VAE: "Automatic"}}
	if cfg.StatePath != "" {
		if b, err := os.ReadFile(cfg.StatePath); err == nil {
			var s state
			if json.Unmarshal(b, &s) == nil {
				if s.MossRef == "" {
					s.MossRef = "Disable"
				}
				if s.VAE == "" {
					s.VAE = "Automatic"
				}
				f.st = s
			}
		}
	}
	return f
}

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func errf(code int, format string, a ...any) error {
	return &httpError{code: code, msg: fmt.Sprintf(format, a...)}
}

func respondJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	var he *httpError
	if errors.As(err, &he) {
		code = he.code
	}
	respondJSON(w, code, map[string]string{"error": err.Error(), "detail": err.Error()})
}

func (f *Facade) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := "/" + strings.Trim(r.URL.Path, "/")
	switch {
	case p == "/sdapi/v1/options" && r.Method == http.MethodGet:
		f.getOptions(w)
	case p == "/sdapi/v1/options" && r.Method == http.MethodPost:
		f.postOptions(w, r)
	case p == "/sdapi/v1/txt2img" && r.Method == http.MethodPost:
		f.generate(w, r, false)
	case p == "/sdapi/v1/img2img" && r.Method == http.MethodPost:
		f.generate(w, r, true)
	case p == "/sdapi/v1/progress" && r.Method == http.MethodGet:
		f.progress(w)
	case p == "/sdapi/v1/interrupt" && r.Method == http.MethodPost:
		f.interrupt(w)
	case p == "/sdapi/v1/sd-models" && r.Method == http.MethodGet:
		f.sdModels(w)
	case p == "/sdapi/v1/sd-vae" && r.Method == http.MethodGet:
		f.sdVAE(w)
	case p == "/sdapi/v1/loras" && r.Method == http.MethodGet:
		f.loras(w, r)
	case p == "/sdapi/v1/samplers" && r.Method == http.MethodGet:
		f.samplers(w)
	case p == "/sdapi/v1/schedulers" && r.Method == http.MethodGet:
		f.schedulers(w)
	case p == "/sdapi/v1/upscalers" && r.Method == http.MethodGet:
		f.upscalers(w)
	case p == "/sdapi/v1/extra-single-image" && r.Method == http.MethodPost:
		f.extraSingle(w, r)
	case p == "/sdapi/v1/memory" && r.Method == http.MethodGet:
		f.memory(w)
	case p == "/sdapi/v1/unload-checkpoint" && r.Method == http.MethodPost:
		f.unload(w, r)
	default:
		respondJSON(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
	}
}

func (f *Facade) getOptions(w http.ResponseWriter) {
	f.mu.Lock()
	s := f.st
	f.mu.Unlock()
	mods := s.Modules
	if mods == nil {
		mods = []string{}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"sd_model_checkpoint":      s.Checkpoint,
		"forge_additional_modules": mods,
		"klein_do_reference":       s.KleinRef,
		"moss_do_reference":        s.MossRef,
		"sd_vae":                   s.VAE,
		"samples_format":           "png",
	})
}

func (f *Facade) postOptions(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeErr(w, errf(http.StatusBadRequest, "invalid options JSON: %v", err))
		return
	}
	f.optMu.Lock()
	defer f.optMu.Unlock()
	f.mu.Lock()
	next := f.st
	next.Modules = slices.Clone(f.st.Modules)
	f.mu.Unlock()
	modelTouched := false
	if v, ok := raw["sd_model_checkpoint"]; ok {
		if err := json.Unmarshal(v, &next.Checkpoint); err != nil {
			writeErr(w, errf(http.StatusBadRequest, "sd_model_checkpoint: %v", err))
			return
		}
		modelTouched = true
	}
	if v, ok := raw["forge_additional_modules"]; ok {
		var mods []string
		if err := json.Unmarshal(v, &mods); err != nil {
			writeErr(w, errf(http.StatusBadRequest, "forge_additional_modules: %v", err))
			return
		}
		next.Modules = mods
		modelTouched = true
	}
	if v, ok := raw["sd_vae"]; ok {
		if err := json.Unmarshal(v, &next.VAE); err != nil {
			writeErr(w, errf(http.StatusBadRequest, "sd_vae: %v", err))
			return
		}
		modelTouched = true
	}
	if v, ok := raw["klein_do_reference"]; ok {
		b, err := boolValue(v)
		if err != nil {
			writeErr(w, errf(http.StatusBadRequest, "klein_do_reference: %v", err))
			return
		}
		next.KleinRef = b
	}
	if v, ok := raw["moss_do_reference"]; ok {
		s, err := mossValue(v)
		if err != nil {
			writeErr(w, errf(http.StatusBadRequest, "moss_do_reference: %v", err))
			return
		}
		next.MossRef = s
	}
	if modelTouched && next.Checkpoint != "" {
		args, err := f.modelArgs(next)
		if err != nil {
			writeErr(w, errf(http.StatusBadRequest, "%v", err))
			return
		}
		f.mu.Lock()
		same := slices.Equal(args, f.args)
		f.mu.Unlock()
		if !same {
			if !f.gen.TryLock() {
				writeErr(w, errf(http.StatusConflict, "generation in progress, model change refused"))
				return
			}
			err := f.load(args)
			f.gen.Unlock()
			if err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	f.mu.Lock()
	f.st = next
	f.mu.Unlock()
	f.saveState(next)
	respondJSON(w, http.StatusOK, nil)
}

func boolValue(v json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(v, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return false, err
	}
	return strconv.ParseBool(s)
}

func mossValue(v json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s, nil
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		return "", err
	}
	if b {
		return "Enable", nil
	}
	return "Disable", nil
}

func (f *Facade) modelArgs(s state) ([]string, error) {
	args, err := ModelArgs(f.cfg, s.Checkpoint, s.Modules)
	if err != nil {
		return nil, err
	}
	if usesSingleFile(args) {
		switch v := strings.TrimSpace(s.VAE); v {
		case "", "Automatic", "None":
		default:
			p := v
			if !filepath.IsAbs(p) {
				p = filepath.Join(f.cfg.VAEDir, v)
			}
			if !within(p, f.cfg.VAEDir, f.cfg.ModelsDir) {
				return nil, fmt.Errorf("sd_vae %q is outside %s", v, f.cfg.VAEDir)
			}
			args = append(args, "--vae", p)
		}
	}
	return args, nil
}

func (f *Facade) saveState(s state) {
	if f.cfg.StatePath == "" {
		return
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	tmp := f.cfg.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, f.cfg.StatePath); err != nil {
		os.Remove(tmp)
	}
}

func (f *Facade) load(args []string) error {
	full := append(slices.Clone(f.cfg.BaseArgs), args...)
	f.mu.Lock()
	f.args = nil
	f.mu.Unlock()
	if f.restart == nil {
		return errf(http.StatusInternalServerError, "sd-server restart is not configured")
	}
	f.mu.Lock()
	resume := slices.Equal(f.pending, args) && time.Since(f.pendingAt) < loadGrace
	f.mu.Unlock()
	if ac, ok := f.restart.(aliveChecker); ok && resume && !ac.Alive() {
		resume = false
	}
	if !resume {
		if err := f.restart.RestartWithArgs(full); err != nil {
			return errf(http.StatusBadGateway, "restart sd-server: %v", err)
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
		code, body, err := f.do(context.Background(), http.MethodGet, "/sdapi/v1/options", nil, 5*time.Second)
		switch {
		case err != nil:
			last = err.Error()
		case code == http.StatusOK:
			return nil
		default:
			last = fmt.Sprintf("options HTTP %d: %s", code, truncate(body, 200))
		}
		if time.Now().After(deadline) {
			return errf(http.StatusBadGateway, "sd-server not ready after %s: %s", f.cfg.ReadyTimeout, last)
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

func (f *Facade) ensureModel() error {
	f.mu.Lock()
	loaded := f.args != nil
	s := f.st
	f.mu.Unlock()
	if loaded || s.Checkpoint == "" {
		return nil
	}
	args, err := f.modelArgs(s)
	if err != nil {
		return errf(http.StatusBadRequest, "%v", err)
	}
	return f.load(args)
}

type forgeReq struct {
	Prompt            string         `json:"prompt"`
	NegativePrompt    string         `json:"negative_prompt"`
	SamplerName       string         `json:"sampler_name"`
	Scheduler         string         `json:"scheduler"`
	Steps             int            `json:"steps"`
	CfgScale          *float64       `json:"cfg_scale"`
	DistilledCfg      *float64       `json:"distilled_cfg_scale"`
	Width             int            `json:"width"`
	Height            int            `json:"height"`
	Seed              *int64         `json:"seed"`
	DenoisingStrength *float64       `json:"denoising_strength"`
	ClipSkip          *int           `json:"clip_skip"`
	BatchSize         int            `json:"batch_size"`
	NIter             int            `json:"n_iter"`
	EnableHR          bool           `json:"enable_hr"`
	HRScale           *float64       `json:"hr_scale"`
	HRUpscaler        string         `json:"hr_upscaler"`
	HRDenoising       *float64       `json:"hr_denoising_strength"`
	HRSteps           int            `json:"hr_second_pass_steps"`
	HRResizeX         int            `json:"hr_resize_x"`
	HRResizeY         int            `json:"hr_resize_y"`
	InitImages        []string       `json:"init_images"`
	Mask              string         `json:"mask"`
	AlwaysOn          map[string]any `json:"alwayson_scripts"`
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
	code, body, err := f.do(ctx, http.MethodGet, "/sdapi/v1/loras", nil, 30*time.Second)
	if err != nil {
		return nil, errf(http.StatusBadGateway, "sd-server unavailable: %v", err)
	}
	if code != http.StatusOK {
		return nil, errf(http.StatusBadGateway, "sd-server loras HTTP %d: %s", code, truncate(body, 200))
	}
	var list []loraEntry
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, errf(http.StatusBadGateway, "decode loras: %v", err)
	}
	return list, nil
}

func (f *Facade) extractLoras(ctx context.Context, prompt string) (string, []map[string]any, error) {
	matches := loraTag.FindAllStringSubmatch(prompt, -1)
	out := []map[string]any{}
	if len(matches) == 0 {
		return prompt, out, nil
	}
	list, err := f.fetchLoras(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		mult := 1.0
		if s := strings.TrimSpace(m[2]); s != "" {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return "", nil, errf(http.StatusBadRequest, "LoRA %q: bad weight %q", name, s)
			}
			mult = v
		}
		path := ""
		for _, l := range list {
			if l.Name == name || stem(l.Path) == name || l.Name == stem(name) {
				path = l.Path
				break
			}
		}
		if path == "" {
			return "", nil, errf(http.StatusBadRequest, "LoRA %q not found", name)
		}
		out = append(out, map[string]any{"path": path, "multiplier": mult})
	}
	clean := strings.Join(strings.Fields(loraTag.ReplaceAllString(prompt, " ")), " ")
	return clean, out, nil
}

func (f *Facade) resolveUpscaler(name string, hires bool) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		if hires {
			return "Latent", nil
		}
		return "None", nil
	}
	if isBuiltinUpscaler(n) {
		return n, nil
	}
	if hires && strings.HasPrefix(n, "Latent") {
		return n, nil
	}
	if s, ok := upscalerAliases[n]; ok {
		n = s
	}
	for _, u := range f.upscalerFiles() {
		base := filepath.Base(u)
		s := strings.TrimSuffix(base, filepath.Ext(base))
		if s == n || base == n {
			return s, nil
		}
	}
	return "", errf(http.StatusBadRequest, "upscaler %q is not available on sd.cpp", name)
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

func (f *Facade) buildNative(ctx context.Context, req forgeReq, img2img bool, s state) (map[string]any, error) {
	if len(req.AlwaysOn) > 0 {
		names := make([]string, 0, len(req.AlwaysOn))
		for k := range req.AlwaysOn {
			names = append(names, k)
		}
		slices.Sort(names)
		return nil, errf(http.StatusBadRequest, "alwayson_scripts (%s) are not supported on sd.cpp", strings.Join(names, ", "))
	}
	if img2img {
		if m := strings.TrimSpace(s.MossRef); m != "" && !strings.EqualFold(m, "Disable") {
			return nil, errf(http.StatusBadRequest, "правка Moss недоступна на sd.cpp — используйте Klein (Moss edit is not available on sd.cpp, use Klein)")
		}
		if len(req.InitImages) == 0 {
			return nil, errf(http.StatusBadRequest, "img2img needs init_images")
		}
	}
	sampler, suffixScheduler, ok := SplitSampler(req.SamplerName)
	if !ok {
		return nil, errf(http.StatusBadRequest, "sampler %q is not available on sd.cpp", req.SamplerName)
	}
	scheduler, ok := SchedulerName(req.Scheduler)
	if !ok {
		return nil, errf(http.StatusBadRequest, "scheduler %q is not available on sd.cpp", req.Scheduler)
	}
	if scheduler == "" {
		scheduler = suffixScheduler
	}
	prompt, loras, err := f.extractLoras(ctx, req.Prompt)
	if err != nil {
		return nil, err
	}
	steps := req.Steps
	if steps <= 0 {
		steps = 20
	}
	cfg := 7.0
	if req.CfgScale != nil {
		cfg = *req.CfgScale
	}
	guidance := map[string]any{"txt_cfg": cfg}
	if req.DistilledCfg != nil {
		guidance["distilled_guidance"] = *req.DistilledCfg
	}
	sp := map[string]any{"sample_steps": steps, "guidance": guidance}
	if sampler != "" {
		sp["sample_method"] = sampler
	}
	if scheduler != "" {
		sp["scheduler"] = scheduler
	}
	n := map[string]any{
		"prompt":          prompt,
		"negative_prompt": loraTag.ReplaceAllString(req.NegativePrompt, ""),
		"sample_params":   sp,
		"lora":            loras,
		"output_format":   "png",
		"preview":         "none",
	}
	if req.Width > 0 {
		n["width"] = req.Width
	}
	if req.Height > 0 {
		n["height"] = req.Height
	}
	if req.ClipSkip != nil {
		n["clip_skip"] = *req.ClipSkip
	}
	if img2img {
		n["init_image"] = req.InitImages[0]
		strength := 0.75
		if req.DenoisingStrength != nil {
			strength = *req.DenoisingStrength
		}
		n["strength"] = strength
		if s.KleinRef {
			n["ref_images"] = []string{req.InitImages[0]}
		}
		if req.Mask != "" {
			n["mask_image"] = req.Mask
		}
	} else if req.EnableHR {
		up, err := f.resolveUpscaler(req.HRUpscaler, true)
		if err != nil {
			return nil, err
		}
		h := map[string]any{"enabled": true, "upscaler": up, "target_width": req.HRResizeX, "target_height": req.HRResizeY, "steps": req.HRSteps}
		if req.HRScale != nil {
			h["scale"] = *req.HRScale
		}
		if req.HRDenoising != nil {
			h["denoising_strength"] = *req.HRDenoising
		}
		n["hires"] = h
	}
	return n, nil
}

type jobResp struct {
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

func (f *Facade) generate(w http.ResponseWriter, r *http.Request, img2img bool) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, errf(http.StatusBadRequest, "read body: %v", err))
		return
	}
	var req forgeReq
	if err := json.Unmarshal(raw, &req); err != nil {
		writeErr(w, errf(http.StatusBadRequest, "invalid JSON: %v", err))
		return
	}
	f.mu.Lock()
	s := f.st
	f.mu.Unlock()
	native, err := f.buildNative(r.Context(), req, img2img, s)
	if err != nil {
		writeErr(w, err)
		return
	}
	batch := req.BatchSize
	if batch <= 0 {
		batch = 1
	}
	iters := req.NIter
	if iters <= 0 {
		iters = 1
	}
	native["batch_count"] = batch
	seed := int64(-1)
	if req.Seed != nil {
		seed = *req.Seed
	}
	if seed < 0 {
		seed = rand.Int64N(1 << 32)
	}

	f.gen.Lock()
	defer f.gen.Unlock()
	job := &jobInfo{total: iters}
	f.mu.Lock()
	f.job = job
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.job = nil
		f.mu.Unlock()
	}()
	if err := f.ensureModel(); err != nil {
		writeErr(w, err)
		return
	}

	images := []string{}
	seeds := []int64{}
	for i := 0; i < iters; i++ {
		f.mu.Lock()
		stop := job.cancelled
		job.no = i
		job.frac, job.step, job.steps = 0, 0, 0
		f.mu.Unlock()
		if stop {
			break
		}
		iterSeed := seed + int64(i*batch)
		native["seed"] = iterSeed
		imgs, cancelled, err := f.runJob(r.Context(), native, job)
		if err != nil {
			writeErr(w, err)
			return
		}
		images = append(images, imgs...)
		for j := range imgs {
			seeds = append(seeds, iterSeed+int64(j))
		}
		if cancelled {
			break
		}
	}
	f.mu.Lock()
	if job.cancelled {
		images, seeds = []string{}, []int64{}
	}
	f.mu.Unlock()
	info := map[string]any{
		"prompt":          req.Prompt,
		"negative_prompt": req.NegativePrompt,
		"seed":            seed,
		"all_seeds":       seeds,
		"width":           req.Width,
		"height":          req.Height,
		"sampler_name":    req.SamplerName,
		"scheduler":       req.Scheduler,
		"steps":           req.Steps,
		"cfg_scale":       req.CfgScale,
		"batch_size":      batch,
		"n_iter":          iters,
		"sd_model_name":   stem(cleanName(s.Checkpoint)),
		"backend":         "sd.cpp",
	}
	if len(images) == 0 {
		info = map[string]any{}
	}
	infoJSON, _ := json.Marshal(info)
	respondJSON(w, http.StatusOK, map[string]any{
		"images":     images,
		"parameters": json.RawMessage(raw),
		"info":       string(infoJSON),
	})
}

func (f *Facade) runJob(ctx context.Context, native map[string]any, job *jobInfo) ([]string, bool, error) {
	code, body, err := f.do(ctx, http.MethodPost, "/sdcpp/v1/img_gen", native, 60*time.Second)
	if err != nil {
		return nil, false, errf(http.StatusBadGateway, "sd-server unavailable: %v", err)
	}
	if code != http.StatusAccepted && code != http.StatusOK {
		return nil, false, submitError(code, body)
	}
	var sub jobResp
	if err := json.Unmarshal(body, &sub); err != nil || sub.ID == "" {
		return nil, false, errf(http.StatusBadGateway, "sd-server img_gen: bad response: %s", truncate(body, 200))
	}
	f.mu.Lock()
	job.id = sub.ID
	cancelNow := job.cancelled
	f.mu.Unlock()
	if cancelNow {
		f.cancelJob(sub.ID)
	}
	fails := 0
	for {
		select {
		case <-ctx.Done():
			f.cancelJob(sub.ID)
			return nil, true, errf(499, "client gone: %v", ctx.Err())
		case <-time.After(f.cfg.PollInterval):
		}
		code, body, err := f.do(ctx, http.MethodGet, "/sdcpp/v1/jobs/"+sub.ID, nil, 30*time.Second)
		if err != nil || code >= 500 {
			fails++
			if fails >= 5 {
				if err != nil {
					return nil, false, errf(http.StatusBadGateway, "sd-server unavailable while generating: %v", err)
				}
				return nil, false, errf(http.StatusBadGateway, "sd-server job HTTP %d: %s", code, truncate(body, 200))
			}
			continue
		}
		fails = 0
		if code != http.StatusOK {
			return nil, false, errf(http.StatusBadGateway, "sd-server job %s HTTP %d: %s", sub.ID, code, truncate(body, 200))
		}
		var jr jobResp
		if err := json.Unmarshal(body, &jr); err != nil {
			return nil, false, errf(http.StatusBadGateway, "decode job: %v", err)
		}
		f.mu.Lock()
		if jr.Progress.Unit == "steps" && jr.Progress.Total != nil && *jr.Progress.Total > 0 && jr.Progress.Completed != nil {
			job.step, job.steps = *jr.Progress.Completed, *jr.Progress.Total
			job.frac = float64(job.step) / float64(job.steps)
		}
		f.mu.Unlock()
		switch jr.Status {
		case "completed":
			var imgs []string
			if jr.Result != nil {
				for _, im := range jr.Result.Images {
					imgs = append(imgs, im.B64)
				}
			}
			return imgs, false, nil
		case "failed":
			msg := "generation failed"
			if jr.Error != nil && jr.Error.Message != "" {
				msg = jr.Error.Message
			}
			return nil, false, errf(http.StatusInternalServerError, "sd.cpp: %s", msg)
		case "cancelled":
			return nil, true, nil
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
	switch code {
	case http.StatusBadRequest, http.StatusServiceUnavailable, http.StatusTooManyRequests:
		return errf(code, "sd.cpp: %s", msg)
	}
	return errf(http.StatusBadGateway, "sd.cpp img_gen HTTP %d: %s", code, msg)
}

func (f *Facade) cancelJob(id string) {
	if id == "" {
		return
	}
	if _, _, err := f.do(context.Background(), http.MethodPost, "/sdcpp/v1/jobs/"+id+"/cancel", nil, 10*time.Second); err != nil {
		return
	}
}

func (f *Facade) progress(w http.ResponseWriter) {
	f.mu.Lock()
	j := f.job
	var p float64
	count, no, step, steps := 0, 0, 0, 0
	jobName := ""
	if j != nil {
		p = (float64(j.no) + j.frac) / float64(j.total)
		count = j.total - j.no
		if count < 1 {
			count = 1
		}
		no, step, steps = j.no, j.step, j.steps
		jobName = j.id
	}
	f.mu.Unlock()
	respondJSON(w, http.StatusOK, map[string]any{
		"progress":     p,
		"eta_relative": 0,
		"state": map[string]any{
			"skipped":        false,
			"interrupted":    false,
			"job":            jobName,
			"job_count":      count,
			"job_no":         no,
			"sampling_step":  step,
			"sampling_steps": steps,
		},
		"current_image": nil,
		"textinfo":      nil,
	})
}

func (f *Facade) interrupt(w http.ResponseWriter) {
	f.mu.Lock()
	id := ""
	if f.job != nil {
		f.job.cancelled = true
		id = f.job.id
	}
	f.mu.Unlock()
	f.cancelJob(id)
	respondJSON(w, http.StatusOK, map[string]any{})
}

func (f *Facade) unload(w http.ResponseWriter, r *http.Request) {
	if !f.gen.TryLock() {
		writeErr(w, errf(http.StatusConflict, "generation in progress"))
		return
	}
	defer f.gen.Unlock()
	code, body, err := f.do(r.Context(), http.MethodPost, "/sdcpp/v1/models/unload", map[string]any{}, 60*time.Second)
	if err != nil {
		writeErr(w, errf(http.StatusBadGateway, "sd-server unavailable: %v", err))
		return
	}
	if code >= 300 && code != http.StatusServiceUnavailable {
		writeErr(w, errf(http.StatusBadGateway, "sd-server unload HTTP %d: %s", code, truncate(body, 200)))
		return
	}
	f.mu.Lock()
	f.args = nil
	f.pending = nil
	f.mu.Unlock()
	respondJSON(w, http.StatusOK, nil)
}

func (f *Facade) memory(w http.ResponseWriter) {
	if f.mem == nil {
		respondJSON(w, http.StatusOK, MemoryStats{})
		return
	}
	m, err := f.mem.Memory()
	if err != nil {
		writeErr(w, errf(http.StatusBadGateway, "memory: %v", err))
		return
	}
	respondJSON(w, http.StatusOK, m)
}
