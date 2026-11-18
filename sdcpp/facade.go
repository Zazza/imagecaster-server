package sdcpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

	jobs     *jobStore
	faceSem  chan struct{}
	faceSwap FaceSwapper
	identity func(person string) ([]float32, error)
	leaser   func(ctx context.Context, req LeaseRequest) (func(), error)
	posFn    func(jobID string) int
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
	f := &Facade{cfg: cfg, restart: r, mem: mem, hc: &http.Client{}, gen: make(chan struct{}, 1), faceSem: make(chan struct{}, 1)}
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
	if err := f.unloadModel(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{})
}

func (f *Facade) UnloadModel(ctx context.Context) error {
	select {
	case f.gen <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-f.gen }()
	return f.unloadModel(ctx)
}

func (f *Facade) unloadModel(ctx context.Context) error {
	code, body, err := f.do(ctx, http.MethodPost, "/sdcpp/v1/models/unload", map[string]any{}, 60*time.Second)
	if err != nil {
		return apiErr(http.StatusBadGateway, "sd_failed", "sd-server unavailable: %v", err)
	}
	if code >= 300 && code != http.StatusServiceUnavailable {
		return apiErr(http.StatusBadGateway, "sd_failed", "sd-server unload HTTP %d: %s", code, truncate(body, 200))
	}
	f.mu.Lock()
	f.args = nil
	f.loadedName = ""
	f.pending = nil
	f.mu.Unlock()
	return nil
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
