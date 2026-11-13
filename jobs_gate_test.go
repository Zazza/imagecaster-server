package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

func TestGateTakesNoLeaseForSDPaths(t *testing.T) {
	cases := []struct {
		name      string
		method    string
		path      string
		wantLease bool
	}{
		{"submit job", http.MethodPost, "/api/sd/jobs", false},
		{"list jobs", http.MethodGet, "/api/sd/jobs", false},
		{"cancel job", http.MethodPost, "/api/sd/jobs/abc/cancel", false},
		{"delete job", http.MethodDelete, "/api/sd/jobs/abc", false},
		{"unload", http.MethodPost, "/api/sd/unload", false},
		{"controlnet detect", http.MethodPost, "/api/sd/controlnet/detect", false},
		{"old txt2img path", http.MethodPost, "/api/sd/sdapi/v1/txt2img", false},
		{"old img2img path", http.MethodPost, "/api/sd/sdapi/v1/img2img", false},
		{"llm chat still leased", http.MethodPost, "/api/llm/api/chat", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate, q := newTestGate(20000, 5, GateConfig{SDDefaultWeightMB: 1000, LLMWeightMB: 1000}, "")
			evictions := 0
			gate.SetBeforeSD(func(context.Context, int) { evictions++ })
			called := false
			leased := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				st := q.Status()
				leased = len(st.Running) + len(st.Queue)
				w.WriteHeader(http.StatusAccepted)
			})
			body := `{"kind":"txt2img","client":"imagecaster","model":{"checkpoint":"sdxl_base.safetensors"},"params":{"prompt":"a"}}`
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			gate.Middleware(next).ServeHTTP(rec, req)
			require.True(t, called)
			assert.Equal(t, http.StatusAccepted, rec.Code)
			if tc.wantLease {
				assert.Equal(t, 1, leased)
			} else {
				assert.Zero(t, leased)
				assert.Zero(t, evictions)
			}
		})
	}
}

func TestRootFeaturesSDJobs(t *testing.T) {
	cases := []struct {
		name     string
		activeSD string
		want     bool
	}{
		{"sdcpp backend", "sdcpp", true},
		{"forge backend", "forge-neo", false},
		{"unknown backend", "nope", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandlers()
			h.config.ActiveSD = tc.activeSD
			h.config.ActiveLLM = config.LLMLlamaCpp
			h.config.Backends = map[string]config.BackendConfig{"sdcpp": {Kind: config.KindSDCpp}, "forge-neo": {}}
			rec := httptest.NewRecorder()
			h.handleRoot(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				Features []string `json:"features"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			if tc.want {
				assert.Contains(t, body.Features, "sd_jobs")
				assert.Contains(t, body.Features, "sdcpp")
			} else {
				assert.NotContains(t, body.Features, "sd_jobs")
			}
		})
	}
}
