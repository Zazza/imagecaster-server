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
	"imagecaster-server/sdcpp"
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
	gate.SetBeforeSD(llmEvictor(u, q.Budget, cfg.LLMWeightMB))

	for i, ck := range []string{"a.safetensors", "b.safetensors"} {
		release, err := gate.LeaseSD(context.Background(), sdcpp.LeaseRequest{JobID: ck, Client: "test", Checkpoint: ck})
		require.NoError(t, err)
		assert.Equal(t, int32(i+1), u.n.Load())
		release()
	}

	h := gate.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, path := range []string{"/api/sd/jobs", "/api/llm/api/chat", "/api/llm/v1/chat/completions"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}")))
		assert.Equal(t, int32(2), u.n.Load(), path)
	}
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
	for _, path := range []string{"/api/server/models/llm", "/api/server/models/llm/pull", "/api/server/models/delete/llm/qwen3:8b", "/api/llm/api/tags"} {
		rec := httptest.NewRecorder()
		withLLMServerRoutes(nil, next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, path)
		assert.Contains(t, rec.Body.String(), "llama.cpp", path)
	}
	rec := httptest.NewRecorder()
	withLLMServerRoutes(nil, next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/server/models/sd", nil))
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
		name      string
		keepAlive string
		vramMB    int
		want      time.Duration
	}{
		{"16 GB card: 5m", "24h", 16376, 5 * time.Minute},
		{"24 GB card: llm.keep_alive", "2h", 24576, 2 * time.Hour},
		{"default", "", 24576, 24 * time.Hour},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.DataDir = t.TempDir()
			cfg.DetectedVRAMMB = tt.vramMB
			cfg.LLM.KeepAlive = tt.keepAlive
			cfg.ApplyLLMBackend()
			cfg.PinLlamaInternal()
			f := newLlamaFacade(&cfg, nil)
			require.NotNil(t, f)
			assert.Equal(t, tt.want, f.KeepAlive())
		})
	}
}

func TestNoLlamaFacadeWhenLLMOff(t *testing.T) {
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	p := cfg.Processes[config.LlamaCppProcessKey]
	p.AutoStart, p.ProxyPath, p.Install = false, "", config.InstallConfig{}
	cfg.Processes[config.LlamaCppProcessKey] = p
	assert.Nil(t, newLlamaFacade(&cfg, nil))
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
