package llamacpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	OllamaCompatVersion = "0.34.2"
	maxRequestBody      = 64 << 20
	maxResponseBody     = 32 << 20
	loadTimeout         = 5 * time.Minute
)

type Config struct {
	Store       *Store
	RouterURL   string
	PresetPath  string
	ContextSize int
	KeepAlive   time.Duration
	Reload      func(ctx context.Context) error
	HTTP        *http.Client
	Puller      *Puller
	Now         func() time.Time
	Poll        time.Duration
}

type Facade struct {
	cfg    Config
	store  *Store
	router *Router
	http   *http.Client
	now    func() time.Time

	mu      sync.Mutex
	ids     map[string]string
	names   map[string]string
	active  map[string]int
	expires map[string]time.Time
	pending map[string]bool
	serving sync.RWMutex
}

func New(cfg Config) *Facade {
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 5 * time.Minute
	}
	return &Facade{
		cfg:     cfg,
		store:   cfg.Store,
		router:  &Router{BaseURL: cfg.RouterURL, HTTP: hc, Poll: cfg.Poll},
		http:    hc,
		now:     now,
		ids:     map[string]string{},
		names:   map[string]string{},
		active:  map[string]int{},
		expires: map[string]time.Time{},
		pending: map[string]bool{},
	}
}

func (f *Facade) IDs() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.ids))
	for k, v := range f.ids {
		out[k] = v
	}
	return out
}

func (f *Facade) WritePreset() error {
	ids, err := f.store.WritePreset(f.cfg.PresetPath, PresetOptions{ContextSize: f.cfg.ContextSize})
	if err != nil {
		return err
	}
	names := make(map[string]string, len(ids))
	for n, id := range ids {
		names[id] = n
	}
	f.mu.Lock()
	f.ids, f.names = ids, names
	f.mu.Unlock()
	return nil
}

func (f *Facade) Refresh(ctx context.Context) error {
	f.serving.Lock()
	defer f.serving.Unlock()
	if err := f.WritePreset(); err != nil {
		return err
	}
	if f.cfg.Reload != nil {
		return f.cfg.Reload(ctx)
	}
	return nil
}

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func errCode(err error) int {
	var he *httpError
	switch {
	case errors.As(err, &he):
		return he.code
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrBadName), errors.Is(err, ErrUnsupported):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	writeJSON(w, errCode(err), map[string]string{"error": err.Error()})
}

func (f *Facade) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := "/" + strings.Trim(r.URL.Path, "/")
	switch {
	case p == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "Ollama is running")
	case p == "/api/version":
		writeJSON(w, 200, map[string]string{"version": OllamaCompatVersion})
	case p == "/api/tags" && r.Method == http.MethodGet:
		f.handleTags(w)
	case p == "/api/ps" && r.Method == http.MethodGet:
		f.handlePs(w, r)
	case p == "/api/show" && r.Method == http.MethodPost:
		f.handleShow(w, r)
	case p == "/api/chat" && r.Method == http.MethodPost:
		f.handleOllama(w, r, false)
	case p == "/api/generate" && r.Method == http.MethodPost:
		f.handleOllama(w, r, true)
	case p == "/api/pull" && r.Method == http.MethodPost:
		f.handlePull(w, r)
	case p == "/api/delete" && r.Method == http.MethodDelete:
		f.handleDelete(w, r)
	case p == "/api/embed" || p == "/api/embeddings" || p == "/v1/embeddings":
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "embeddings are not supported by the llama.cpp backend"})
	case p == "/v1/models" && r.Method == http.MethodGet:
		f.handleV1Models(w)
	case (p == "/v1/chat/completions" || p == "/v1/completions") && r.Method == http.MethodPost:
		f.handleV1(w, r, p)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "404 page not found"})
	}
}

func (f *Facade) handleTags(w http.ResponseWriter) {
	list, err := f.store.List()
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, m := range list {
		out = append(out, map[string]any{
			"name":        m.Name.String(),
			"model":       m.Name.String(),
			"modified_at": m.Modified.Format(time.RFC3339Nano),
			"size":        m.Size,
			"digest":      m.Digest,
			"details":     details(m),
		})
	}
	writeJSON(w, 200, map[string]any{"models": out})
}

func details(m Model) map[string]any {
	fam := m.Config.ModelFamilies
	if fam == nil && m.Config.ModelFamily != "" {
		fam = []string{m.Config.ModelFamily}
	}
	return map[string]any{
		"parent_model":       "",
		"format":             orStr(m.Config.ModelFormat, "gguf"),
		"family":             m.Config.ModelFamily,
		"families":           fam,
		"parameter_size":     m.Config.ModelType,
		"quantization_level": m.Config.FileType,
	}
}

func orStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (f *Facade) handleV1Models(w http.ResponseWriter) {
	list, err := f.store.List()
	if err != nil {
		writeErr(w, err)
		return
	}
	data := make([]map[string]any, 0, len(list))
	for _, m := range list {
		data = append(data, map[string]any{"id": m.Name.String(), "object": "model", "created": m.Modified.Unix(), "owned_by": m.Name.Namespace})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func (f *Facade) handlePs(w http.ResponseWriter, r *http.Request) {
	loaded, err := f.router.Loaded(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(loaded))
	for _, id := range loaded {
		f.mu.Lock()
		name, ok := f.names[id]
		exp, hasExp := f.expires[id]
		f.mu.Unlock()
		if !ok {
			continue
		}
		m, err := f.store.Resolve(name)
		if err != nil {
			continue
		}
		if !hasExp {
			exp = f.now().Add(f.cfg.KeepAlive)
		}
		if exp.IsZero() {
			exp = time.Date(2318, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		out = append(out, map[string]any{
			"name":       name,
			"model":      name,
			"size":       m.Size,
			"size_vram":  m.Size,
			"digest":     m.Digest,
			"details":    details(m),
			"expires_at": exp.Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, 200, map[string]any{"models": out})
}

func readBody(r *http.Request) (map[string]any, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxRequestBody {
		return nil, &httpError{http.StatusRequestEntityTooLarge, "request body too large"}
	}
	body := map[string]any{}
	if len(bytes.TrimSpace(b)) == 0 {
		return body, nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&body); err != nil {
		return nil, &httpError{http.StatusBadRequest, "invalid JSON: " + err.Error()}
	}
	return body, nil
}

func modelField(body map[string]any) string {
	if s, _ := body["model"].(string); s != "" {
		return s
	}
	s, _ := body["name"].(string)
	return s
}

func (f *Facade) handleShow(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	m, err := f.store.Resolve(modelField(body))
	if err != nil {
		writeErr(w, err)
		return
	}
	caps := []string{"completion"}
	if m.Vision() {
		caps = append(caps, "vision")
	}
	if m.Embedding() {
		caps = []string{"embedding"}
	}
	var params strings.Builder
	for k, v := range f.store.Params(m) {
		if arr, ok := v.([]any); ok {
			for _, a := range arr {
				fmt.Fprintf(&params, "%s %v\n", k, a)
			}
			continue
		}
		fmt.Fprintf(&params, "%s %v\n", k, v)
	}
	writeJSON(w, 200, map[string]any{
		"modelfile":    "FROM " + m.ModelPath + "\n",
		"parameters":   params.String(),
		"template":     f.store.Text(m, MediaTemplate),
		"system":       f.store.Text(m, MediaSystem),
		"details":      details(m),
		"model_info":   map[string]any{"general.architecture": m.Config.ModelFamily},
		"capabilities": caps,
		"modified_at":  m.Modified.Format(time.RFC3339Nano),
	})
}

func (f *Facade) handleDelete(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := f.Delete(r.Context(), modelField(body)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (f *Facade) Delete(ctx context.Context, name string) error {
	m, err := f.store.Resolve(name)
	if err != nil {
		return err
	}
	f.mu.Lock()
	id, ok := f.ids[m.Name.String()]
	f.mu.Unlock()
	if ok {
		if err := f.router.Unload(ctx, id); err != nil {
			log.Printf("[llamacpp] unload before delete %s: %v", m.Name, err)
		}
	}
	if err := f.store.Delete(m.Name.String()); err != nil {
		return err
	}
	return f.Refresh(ctx)
}

func (f *Facade) resolve(ctx context.Context, name string) (Model, string, error) {
	if name == "" {
		return Model{}, "", &httpError{http.StatusBadRequest, "model is required"}
	}
	m, err := f.store.Resolve(name)
	if err != nil {
		return Model{}, "", err
	}
	if err := m.Supported(); err != nil {
		return Model{}, "", err
	}
	f.mu.Lock()
	id, ok := f.ids[m.Name.String()]
	f.mu.Unlock()
	if !ok {
		if err := f.Refresh(ctx); err != nil {
			return Model{}, "", err
		}
		f.mu.Lock()
		id, ok = f.ids[m.Name.String()]
		f.mu.Unlock()
		if !ok {
			return Model{}, "", fmt.Errorf("%w: %s", ErrNotFound, m.Name)
		}
	}
	return m, id, nil
}

func (f *Facade) ensureLoaded(ctx context.Context, m Model, id string) error {
	lctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	err := f.router.EnsureLoaded(lctx, id)
	if errors.Is(err, ErrLoadFailed) {
		return fmt.Errorf("%s: %w (family %s): not enough video memory or a format llama.cpp cannot open; see the llm process log", m.Name, err, m.Family())
	}
	return err
}

func (f *Facade) KeepAlive() time.Duration {
	return f.cfg.KeepAlive
}

func (f *Facade) begin(id string) {
	f.serving.RLock()
	f.mu.Lock()
	f.active[id]++
	f.mu.Unlock()
}

func (f *Facade) end(ctx context.Context, id string, ka time.Duration) {
	f.mu.Lock()
	f.active[id]--
	idle := f.active[id] <= 0
	if idle {
		delete(f.active, id)
		if f.pending[id] {
			delete(f.pending, id)
			ka = 0
		}
	}
	switch {
	case ka < 0:
		f.expires[id] = time.Time{}
	default:
		f.expires[id] = f.now().Add(ka)
	}
	f.mu.Unlock()
	f.serving.RUnlock()
	if ka == 0 && idle {
		f.unload(ctx, id)
	}
}

func (f *Facade) unload(ctx context.Context, id string) {
	if err := f.router.Unload(context.WithoutCancel(ctx), id); err != nil {
		log.Printf("[llamacpp] unload %s: %v", id, err)
		return
	}
	f.mu.Lock()
	delete(f.expires, id)
	f.mu.Unlock()
}

func (f *Facade) keepAlive(body map[string]any) (time.Duration, error) {
	v, ok := body["keep_alive"]
	if !ok || v == nil {
		return f.cfg.KeepAlive, nil
	}
	return ParseKeepAlive(v)
}

func ParseKeepAlive(v any) (time.Duration, error) {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Float64(); err == nil {
			return secs(n), nil
		}
	case float64:
		return secs(t), nil
	case int:
		return secs(float64(t)), nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			break
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return secs(n), nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, &httpError{http.StatusBadRequest, "invalid keep_alive: " + s}
		}
		if d < 0 {
			return -1, nil
		}
		return d, nil
	}
	return 0, &httpError{http.StatusBadRequest, fmt.Sprintf("invalid keep_alive: %v", v)}
}

func secs(n float64) time.Duration {
	if n < 0 {
		return -1
	}
	return time.Duration(n * float64(time.Second))
}

func (f *Facade) Sweep(ctx context.Context) {
	now := f.now()
	f.mu.Lock()
	var due []string
	for id, exp := range f.expires {
		if !exp.IsZero() && !now.Before(exp) && f.active[id] == 0 {
			due = append(due, id)
		}
	}
	f.mu.Unlock()
	if len(due) == 0 {
		return
	}
	loaded, err := f.router.Loaded(ctx)
	if err != nil {
		return
	}
	isLoaded := map[string]bool{}
	for _, id := range loaded {
		isLoaded[id] = true
	}
	for _, id := range due {
		if isLoaded[id] {
			f.unload(ctx, id)
			continue
		}
		f.mu.Lock()
		delete(f.expires, id)
		f.mu.Unlock()
	}
}

func (f *Facade) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				f.Sweep(ctx)
			}
		}
	}()
}

func (f *Facade) UnloadIdle(ctx context.Context) error {
	loaded, err := f.router.Loaded(ctx)
	if err != nil {
		return err
	}
	for _, id := range loaded {
		f.mu.Lock()
		busy := f.active[id] > 0
		f.mu.Unlock()
		if busy {
			continue
		}
		if err := f.router.Unload(ctx, id); err != nil {
			return err
		}
		f.mu.Lock()
		delete(f.expires, id)
		f.mu.Unlock()
	}
	return nil
}

func (f *Facade) RunningNames() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	loaded, err := f.router.Loaded(ctx)
	if err != nil {
		return nil
	}
	var out []string
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range loaded {
		if n, ok := f.names[id]; ok {
			out = append(out, n)
		}
	}
	return out
}
