package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
	"imagecaster-server/llamacpp"
)

type countUnloader struct{ n atomic.Int32 }

func (c *countUnloader) UnloadIdle(context.Context) error {
	c.n.Add(1)
	return nil
}

func TestGateUnloadsLLMBeforeSDJob(t *testing.T) {
	cfg := GateConfig{SDDefaultWeightMB: 9000, LLMWeightMB: 11000, MaxWaitSeconds: 5}
	gate, q := newTestGate(15876, 5, cfg, "")
	u := &countUnloader{}
	var unloadedFirst atomic.Bool
	gate.SetBeforeSD(llmEvictor(u, q.Budget, cfg.LLMWeightMB))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unloadedFirst.Store(u.n.Load() > 0)
	})
	h := gate.Middleware(next)

	cases := []struct {
		method, path string
		want         int32
	}{
		{http.MethodPost, "/api/sd/sdapi/v1/txt2img", 1},
		{http.MethodPost, "/api/sd/sdapi/v1/img2img", 2},
		{http.MethodGet, "/api/sd/sdapi/v1/progress", 2},
		{http.MethodPost, "/api/llm/api/chat", 2},
		{http.MethodPost, "/api/llm/v1/chat/completions", 2},
	}
	for _, c := range cases {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, c.path, strings.NewReader("{}")))
		assert.Equal(t, c.want, u.n.Load(), c.path)
	}
	assert.True(t, unloadedFirst.Load())
}

func TestEvictorSkipsWhenBothFit(t *testing.T) {
	u := &countUnloader{}
	llmEvictor(u, func() int { return 40000 }, 11000)(context.Background(), 9000)
	assert.Zero(t, u.n.Load())
	llmEvictor(u, func() int { return 15876 }, 11000)(context.Background(), 9000)
	assert.Equal(t, int32(1), u.n.Load())
	llmEvictor(u, func() int { return 0 }, 11000)(context.Background(), 9000)
	assert.Equal(t, int32(2), u.n.Load())
}

func TestLLMServerRoutesOnlyWithFacade(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	rec := httptest.NewRecorder()
	withLLMServerRoutes(nil, next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/server/models/llm", nil))
	assert.Equal(t, 299, rec.Code)

	f := llamacpp.New(llamacpp.Config{Store: &llamacpp.Store{Root: t.TempDir()}, PresetPath: t.TempDir() + "/p.ini"})
	h := withLLMServerRoutes(f, next)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/server/models/llm", nil))
	require.Equal(t, 200, rec.Code)
	assert.JSONEq(t, `[]`, rec.Body.String())
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/server/models/sd", nil))
	assert.Equal(t, 299, rec.Code)
}

func TestLlamaFacadeKeepAliveWiring(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		vramMB int
		want   time.Duration
	}{
		{"16 GB card: 5m like Ollama", "24h", 16376, 5 * time.Minute},
		{"24 GB card: OLLAMA_KEEP_ALIVE", "24h", 24576, 24 * time.Hour},
		{"no env", "", 24576, 5 * time.Minute},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.DataDir = t.TempDir()
			cfg.DetectedVRAMMB = tt.vramMB
			o := cfg.Processes["ollama"]
			o.Env = map[string]string{"OLLAMA_KEEP_ALIVE": tt.env}
			cfg.Processes["ollama"] = o
			cfg.ApplyLLMBackend()
			cfg.PinLlamaInternal()
			f := newLlamaFacade(&cfg, nil)
			require.NotNil(t, f)
			assert.Equal(t, tt.want, f.KeepAlive())
		})
	}
}

func TestWaitHealthy(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	require.NoError(t, waitHealthy(context.Background(), srv.URL, 5*time.Second))
	assert.GreaterOrEqual(t, n.Load(), int32(3))
	assert.Error(t, waitHealthy(context.Background(), "http://127.0.0.1:1/health", 300*time.Millisecond))
}
