package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
	"imagecaster-server/installer"
	"imagecaster-server/models"
)

func TestApplyWizardComponents(t *testing.T) {
	tests := []struct {
		name       string
		components map[string]bool
		wantSD     bool
		wantOllama bool
	}{
		{"all true", map[string]bool{"forge": true, "ollama": true}, true, true},
		{"forge off", map[string]bool{"forge": false, "ollama": true}, false, true},
		{"ollama off", map[string]bool{"forge": true, "ollama": false}, true, false},
		{"both off", map[string]bool{"forge": false, "ollama": false}, false, false},
		{"empty", map[string]bool{}, true, true},
		{"unknown key", map[string]bool{"python": false, "xyz": false}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.NewDefault()
			sdInstall := cfg.Processes["sd"].Install
			olInstall := cfg.Processes["ollama"].Install
			bInstall := cfg.Backends[cfg.ActiveSD].Install
			applyWizardComponents(&cfg, tt.components)
			assert.Equal(t, tt.wantSD, cfg.Processes["sd"].AutoStart)
			assert.Equal(t, tt.wantOllama, cfg.Processes["ollama"].AutoStart)
			assert.Equal(t, sdInstall, cfg.Processes["sd"].Install)
			assert.Equal(t, olInstall, cfg.Processes["ollama"].Install)
			assert.Equal(t, bInstall, cfg.Backends[cfg.ActiveSD].Install)
		})
	}
}

func TestApplyWizardComponentsMissingBackend(t *testing.T) {
	cfg := config.NewDefault()
	cfg.ActiveSD = "nope"
	assert.NotPanics(t, func() {
		applyWizardComponents(&cfg, map[string]bool{"forge": false, "ollama": false})
	})
	assert.False(t, cfg.Processes["ollama"].AutoStart)
}

func TestRootRouteVersion(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v9.9.9-test"
	cfg := &config.Config{}
	mux := setupMux(newTestHandlers(), installer.NewInstaller(cfg), models.NewModelManager(cfg), NewBackendManager(cfg), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "Imagecaster Server", body["name"])
	assert.Equal(t, "v9.9.9-test", body["version"])
}

func TestVersionDefault(t *testing.T) {
	assert.Equal(t, "dev", version)
}

func TestNewMDNSVersion(t *testing.T) {
	m := NewMDNS(8080, false, "v1.2.3")
	assert.Equal(t, "v1.2.3", m.version)
	assert.NoError(t, m.Register())
}
