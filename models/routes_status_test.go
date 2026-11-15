package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

func TestModelRoutesStatusCodes(t *testing.T) {
	env := newTEEnv(t, false)
	writeFile(t, filepath.Join(env.sdDir, "keep.safetensors"))
	writeFile(t, filepath.Join(env.loraDir, "gone.safetensors"))

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"delete existing", http.MethodDelete, "/api/server/models/delete/lora/gone.safetensors", http.StatusOK},
		{"delete missing file", http.MethodDelete, "/api/server/models/delete/sd/missing.safetensors", http.StatusNotFound},
		{"delete unknown type", http.MethodDelete, "/api/server/models/delete/foo/keep.safetensors", http.StatusNotFound},
		{"delete wrong method", http.MethodGet, "/api/server/models/delete/sd/keep.safetensors", http.StatusMethodNotAllowed},
		{"list unknown type", http.MethodGet, "/api/server/models/foo", http.StatusNotFound},
		{"list wrong method", http.MethodPut, "/api/server/models/sd", http.StatusMethodNotAllowed},
		{"list sd", http.MethodGet, "/api/server/models/sd", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			env.mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			if tt.want != http.StatusOK {
				var body map[string]string
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.NotEmpty(t, body["error"])
			}
		})
	}
	assert.NoFileExists(t, filepath.Join(env.loraDir, "gone.safetensors"))
	assert.FileExists(t, filepath.Join(env.sdDir, "keep.safetensors"))
}

func TestModelRoutesNoActiveBackend(t *testing.T) {
	mm := NewModelManager(&config.Config{})
	mux := http.NewServeMux()
	mm.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/server/models/vae", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDirFor(t *testing.T) {
	b := &config.BackendConfig{ModelsDir: "/m/ckpt", LoraDir: "/m/lora", VaeDir: "/m/vae"}
	tests := []struct {
		typ    string
		want   string
		wantOK bool
	}{
		{"sd", "/m/ckpt", true},
		{"lora", "/m/lora", true},
		{"vae", "/m/vae", true},
		{"text_encoder", filepath.Join("/m", "text_encoder"), true},
		{"llm", "", false},
	}
	for _, tt := range tests {
		got, ok := dirFor(b, tt.typ)
		assert.Equal(t, tt.wantOK, ok, tt.typ)
		assert.Equal(t, tt.want, got, tt.typ)
	}
}
