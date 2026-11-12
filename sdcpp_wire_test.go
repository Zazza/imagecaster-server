package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"imagecaster-server/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxySetHandlerKeepsGateAndStripsPrefix(t *testing.T) {
	cfg := config.NewDefault()
	gate, _ := newTestGate(20000, 5, GateConfig{SDDefaultWeightMB: 1000, LLMWeightMB: 1000}, "")
	ph := NewProxyHandler(nil, &cfg, gate)
	cfg.ActiveSD = "sdcpp"
	cfg.Backends["sdcpp"] = config.BackendConfig{Kind: config.KindSDCpp, ProcessKey: "sd"}
	ph.SetHandler("/api/sd/", newSDCppFacade(&cfg, nil, nil))
	require.NotNil(t, ph.routes["/api/sd/"].gate)
	assert.Same(t, gate, ph.routes["/api/sd/"].gate)

	rec := httptest.NewRecorder()
	ph.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sd/sdapi/v1/samplers", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"Euler a"`)

	rec = httptest.NewRecorder()
	ph.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sd/sdapi/v1/nope", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNewSDCppFacadeOnlyForSDCppBackend(t *testing.T) {
	cfg := config.NewDefault()
	cfg.ActiveSD = config.NeoBackendKey
	assert.Nil(t, newSDCppFacade(&cfg, nil, nil))
	cfg.ActiveSD = "sdcpp"
	cfg.Backends["sdcpp"] = config.BackendConfig{Kind: config.KindSDCpp, ProcessKey: "sd"}
	assert.NotNil(t, newSDCppFacade(&cfg, nil, nil))
}

func TestSDCppRoutesDetect(t *testing.T) {
	hit := func(name string, got *string) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { *got = name })
	}
	for _, tc := range []struct{ path, want string }{
		{"controlnet/detect", "detect"},
		{"/controlnet/detect", "detect"},
		{"/sdapi/v1/txt2img", "facade"},
		{"controlnet/model_list", "facade"},
	} {
		var got string
		h := &sdcppRoutes{facade: hit("facade", &got), detect: hit("detect", &got)}
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://x/"+strings.TrimPrefix(tc.path, "/"), nil))
		if got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.path, got, tc.want)
		}
	}
}

func TestPoseModelsDir(t *testing.T) {
	m := filepath.Join("srv", "forge", "models", "Stable-diffusion")
	if got, want := poseModelsDir(&config.BackendConfig{ModelsDir: m}), filepath.Join("srv", "forge", "models", "ControlNetPreprocessor", "openpose"); got != want {
		t.Errorf("default: %s, want %s", got, want)
	}
	if got := poseModelsDir(&config.BackendConfig{ModelsDir: m, PoseModelsDir: "/p"}); got != "/p" {
		t.Errorf("explicit: %s", got)
	}
	if got := poseModelsDir(&config.BackendConfig{}); got != "" {
		t.Errorf("empty: %s", got)
	}
}

func TestEffectiveDir(t *testing.T) {
	assert.Equal(t, "/cfg", effectiveDir([]string{"--x", "1"}, "/w", "--lora-model-dir", "/cfg"))
	assert.Equal(t, "/arg", effectiveDir([]string{"--lora-model-dir", "/arg"}, "/w", "--lora-model-dir", "/cfg"))
	assert.Equal(t, "/eq", effectiveDir([]string{"--lora-model-dir=/eq"}, "/w", "--lora-model-dir", "/cfg"))
	assert.Equal(t, filepath.Join("/w", "models", "Lora"), effectiveDir([]string{"--lora-model-dir", "models/Lora"}, "/w", "--lora-model-dir", "/cfg"))
	assert.Equal(t, "models/Lora", effectiveDir([]string{"--lora-model-dir", "models/Lora"}, "", "--lora-model-dir", "/cfg"))
}

func TestSDCppBaseArgsVRAMTier(t *testing.T) {
	base := []string{"--listen-ip", "127.0.0.1"}
	assert.Equal(t, base, sdcppBaseArgs(base, 0))
	assert.Equal(t, base, sdcppBaseArgs(base, 16376))
	assert.Equal(t, []string{"--listen-ip", "127.0.0.1", "--max-vram", "-3"}, sdcppBaseArgs(base, 8192))
	assert.Equal(t, []string{"--max-vram", "4"}, sdcppBaseArgs([]string{"--max-vram", "4"}, 8192))
	assert.Equal(t, []string{"--listen-ip", "127.0.0.1"}, base)
}
