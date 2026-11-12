package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

func TestRootReportsFeatures(t *testing.T) {
	cases := []struct {
		name     string
		activeSD string
		kind     string
		llm      string
		want     []string
	}{
		{"new engines", "sdcpp", config.KindSDCpp, config.LLMLlamaCpp, []string{"sdcpp", "llamacpp", "download_stream"}},
		{"forge and ollama rollback", "forge-neo", "", config.LLMOllama, []string{"forge", "ollama", "download_stream"}},
		{"sd.cpp with ollama", "sdcpp", config.KindSDCpp, config.LLMOllama, []string{"sdcpp", "ollama", "download_stream"}},
		{"unknown active backend", "nope", "", config.LLMLlamaCpp, []string{"forge", "llamacpp", "download_stream"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandlers()
			h.config.ActiveSD = tc.activeSD
			h.config.ActiveLLM = tc.llm
			h.config.Backends = map[string]config.BackendConfig{"sdcpp": {Kind: config.KindSDCpp}, "forge-neo": {}}
			rec := httptest.NewRecorder()
			h.handleRoot(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				Name     string   `json:"name"`
				Version  string   `json:"version"`
				Features []string `json:"features"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, "Imagecaster Server", body.Name)
			assert.Equal(t, version, body.Version)
			assert.Equal(t, tc.want, body.Features)
		})
	}
}

func TestRootUnknownPathNotFound(t *testing.T) {
	h := newTestHandlers()
	rec := httptest.NewRecorder()
	h.handleRoot(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
