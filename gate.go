package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"imagecaster-server/api"
	"imagecaster-server/gpuqueue"
	"imagecaster-server/models"
	"imagecaster-server/sdcpp"
)

const (
	mbBytes      = 1024 * 1024
	maxClientTag = 32
)

type GateConfig struct {
	SDDefaultWeightMB int
	SDOverheadMB      int
	LLMWeightMB       int
	MaxWaitSeconds    int
}

type Gate struct {
	beforeSD       func(ctx context.Context, sdWeightMB int)
	q              *gpuqueue.Queue
	cfg            GateConfig
	sdw            *sdWeightTracker
	clampMu        sync.Mutex
	lastClampW     int
	lastClampTotal int
	sdLeases       sync.Map
	sdLeaseLost    func(jobID string)
}

func NewGate(q *gpuqueue.Queue, cfg GateConfig, sdw *sdWeightTracker) *Gate {
	return &Gate{q: q, cfg: cfg, sdw: sdw}
}

func (g *Gate) Enabled() bool {
	return g != nil && g.q != nil && g.q.Enabled()
}

func (g *Gate) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.Enabled() {
			next.ServeHTTP(w, r)
			return
		}
		weight := g.weight(r)
		if weight <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		lease, err := g.q.Acquire(r.Context(), gpuqueue.Spec{
			Kind:      gateKind(r.URL.Path),
			Client:    clientTag(r),
			WeightMB:  weight,
			AutoRenew: true,
		})
		if err != nil {
			if errors.Is(err, gpuqueue.ErrTimeout) {
				w.Header().Set("Retry-After", retryAfterSeconds(g.q))
				api.WriteError(w, gpuqueue.ErrTimeout.Error(), http.StatusServiceUnavailable)
				return
			}
			if errors.Is(err, gpuqueue.ErrCancelled) {
				if r.Context().Err() != nil {
					return
				}
				w.Header().Set("Retry-After", retryAfterSeconds(g.q))
				api.WriteError(w, gpuqueue.ErrTimeout.Error(), http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		defer lease.Release()
		next.ServeHTTP(w, r)
	})
}

func (g *Gate) SetBeforeSD(fn func(ctx context.Context, sdWeightMB int)) {
	g.beforeSD = fn
}

func (g *Gate) weight(r *http.Request) int {
	p := strings.TrimSuffix(r.URL.Path, "/")
	if strings.HasPrefix(p, "/api/llm/") {
		if r.Method != http.MethodPost {
			return 0
		}
		switch strings.TrimPrefix(p, "/api/llm/") {
		case "api/generate", "api/chat", "api/embed", "api/embeddings",
			"v1/chat/completions", "v1/completions", "v1/embeddings":
			return g.clampWeight(g.cfg.LLMWeightMB)
		}
	}
	return 0
}

func (g *Gate) clampWeight(weight int) int {
	total := g.q.Budget()
	if total <= 0 || weight <= total {
		return weight
	}
	g.noteClamp(weight, total)
	return total
}

func (g *Gate) noteClamp(weight, total int) {
	g.clampMu.Lock()
	defer g.clampMu.Unlock()
	if g.lastClampW == weight && g.lastClampTotal == total {
		return
	}
	g.lastClampW = weight
	g.lastClampTotal = total
	g.q.Warn(fmt.Sprintf("gate weight %dMB exceeds budget %dMB, clamped", weight, total))
}

func retryAfterSeconds(q *gpuqueue.Queue) string {
	return strconv.Itoa(int(q.MaxWait().Seconds()))
}

func gateKind(path string) gpuqueue.Kind {
	if strings.HasPrefix(path, "/api/llm/") {
		return gpuqueue.KindLLM
	}
	return gpuqueue.KindSD
}

func clientTag(r *http.Request) string {
	ua := gpuqueue.SanitizeClient(r.UserAgent())
	if ua == "" {
		return "proxy"
	}
	runes := []rune(ua)
	if len(runes) > maxClientTag {
		runes = runes[:maxClientTag]
	}
	return string(runes)
}

type sdWeightTracker struct {
	mu        sync.Mutex
	cfg       GateConfig
	mm        *models.ModelManager
	modelsDir string
	weights   map[string]int
}

func newSDWeight(cfg GateConfig, mm *models.ModelManager, modelsDir string) *sdWeightTracker {
	return &sdWeightTracker{
		cfg:       cfg,
		mm:        mm,
		modelsDir: modelsDir,
		weights:   make(map[string]int),
	}
}

func (t *sdWeightTracker) weightFor(name string) int {
	if name == "" {
		return t.cfg.SDDefaultWeightMB
	}
	t.mu.Lock()
	if w, ok := t.weights[name]; ok {
		t.mu.Unlock()
		return w
	}
	t.mu.Unlock()

	weight := t.cfg.SDDefaultWeightMB
	if t.mm != nil && t.modelsDir != "" {
		list, err := t.mm.ListModels(t.modelsDir)
		if err == nil {
			for _, m := range list {
				if m.Name == name {
					weight = int((m.Size+mbBytes-1)/mbBytes) + t.cfg.SDOverheadMB
					break
				}
			}
		}
	}

	t.mu.Lock()
	t.weights[name] = weight
	t.mu.Unlock()
	return weight
}

func cleanCheckpoint(name string) string {
	if i := strings.Index(name, " ["); i >= 0 {
		name = name[:i]
	}
	return strings.TrimSpace(name)
}

func (g *Gate) SetSDLeaseLost(fn func(jobID string)) {
	g.sdLeaseLost = fn
}

func (g *Gate) sdJobWeight(checkpoint string) int {
	w := g.cfg.SDDefaultWeightMB
	if g.sdw != nil {
		w = g.sdw.weightFor(cleanCheckpoint(checkpoint))
	}
	return g.clampWeight(w)
}

func sdJobClient(c string) string {
	runes := []rune(gpuqueue.SanitizeClient(c))
	if len(runes) == 0 {
		return "sd-job"
	}
	if len(runes) > maxClientTag {
		runes = runes[:maxClientTag]
	}
	return string(runes)
}

func (g *Gate) LeaseSD(ctx context.Context, req sdcpp.LeaseRequest) (func(), error) {
	if !g.Enabled() {
		return func() {}, nil
	}
	weight := g.sdJobWeight(req.Checkpoint)
	lease, err := g.q.Acquire(ctx, gpuqueue.Spec{
		Kind:         gpuqueue.KindSD,
		Client:       sdJobClient(req.Client),
		WeightMB:     weight,
		AutoRenew:    true,
		NoWaitLimit:  true,
		NoQueueLimit: true,
		Exclusive:    true,
		OnQueued:     func(id string) { g.sdLeases.Store(req.JobID, id) },
	})
	g.sdLeases.Delete(req.JobID)
	if err != nil {
		switch {
		case errors.Is(err, gpuqueue.ErrQueueFull):
			return nil, sdcpp.ErrQueueFull
		case errors.Is(err, gpuqueue.ErrCancelled), ctx.Err() != nil:
			return nil, context.Canceled
		}
		return nil, err
	}
	if g.beforeSD != nil {
		g.beforeSD(ctx, weight)
	}
	var released atomic.Bool
	stop := make(chan struct{})
	if done := lease.Done(); done != nil {
		go func() {
			select {
			case <-done:
				if !released.Load() && g.sdLeaseLost != nil {
					g.sdLeaseLost(req.JobID)
				}
			case <-stop:
			}
		}()
	}
	return func() {
		if released.CompareAndSwap(false, true) {
			close(stop)
			lease.Release()
		}
	}, nil
}

func (g *Gate) SDPosition(jobID string) int {
	if g == nil || g.q == nil {
		return 0
	}
	v, ok := g.sdLeases.Load(jobID)
	if !ok {
		return 0
	}
	j, ok := g.q.Get(v.(string))
	if !ok {
		return 0
	}
	return j.Position
}
