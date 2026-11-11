package models

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

type tokEnv struct {
	mux    *http.ServeMux
	teDir  string
	tokDir string
}

func newTokEnv(t *testing.T) tokEnv {
	t.Helper()
	root := t.TempDir()
	b := config.BackendConfig{
		Name:           "sd.cpp",
		Kind:           config.KindSDCpp,
		ModelsDir:      filepath.Join(root, "models", "Stable-diffusion"),
		LoraDir:        filepath.Join(root, "models", "Lora"),
		VaeDir:         filepath.Join(root, "models", "VAE"),
		TextEncoderDir: filepath.Join(root, "models", "text_encoder"),
		TokenizerDir:   filepath.Join(root, "models", "tokenizer"),
	}
	for _, d := range []string{b.ModelsDir, b.LoraDir, b.VaeDir, b.TextEncoderDir, b.TokenizerDir} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	cfg := &config.Config{ActiveSD: "sdcpp", Backends: map[string]config.BackendConfig{"sdcpp": b}}
	mm := NewModelManager(cfg)
	mux := http.NewServeMux()
	mm.RegisterRoutes(mux)
	mm.RegisterDeleteRoutes(mux)
	return tokEnv{mux: mux, teDir: b.TextEncoderDir, tokDir: b.TokenizerDir}
}

func deleteTE(t *testing.T, mux *http.ServeMux, name string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/server/models/delete/text_encoder/"+name, nil))
	return rec
}

func TestTextEncoderListIncludesTokenizerDir(t *testing.T) {
	e := newTokEnv(t)
	writeFile(t, filepath.Join(e.tokDir, "moss.tokenizer.json"))
	writeFile(t, filepath.Join(e.tokDir, "other.txt"))
	writeFile(t, filepath.Join(e.teDir, "qwen.safetensors"))

	got := listNames(t, e.mux, "/api/server/models/text_encoder")
	assert.Equal(t, []string{"moss.tokenizer.json", "qwen.safetensors"}, got)
}

func TestTextEncoderListSameJSONInBothDirsOnce(t *testing.T) {
	e := newTokEnv(t)
	writeFile(t, filepath.Join(e.tokDir, "moss.tokenizer.json"))
	writeFile(t, filepath.Join(e.teDir, "moss.tokenizer.json"))
	writeFile(t, filepath.Join(e.teDir, "qwen.safetensors"))

	got := listNames(t, e.mux, "/api/server/models/text_encoder")
	assert.Equal(t, []string{"moss.tokenizer.json", "qwen.safetensors"}, got)
}

func TestTextEncoderListEmptyTokenizerDir(t *testing.T) {
	e := newTokEnv(t)
	writeFile(t, filepath.Join(e.teDir, "qwen.safetensors"))

	assert.Equal(t, []string{"qwen.safetensors"}, listNames(t, e.mux, "/api/server/models/text_encoder"))
}

func TestTextEncoderDeleteFromTokenizerDir(t *testing.T) {
	e := newTokEnv(t)
	tok := filepath.Join(e.tokDir, "moss.tokenizer.json")
	writeFile(t, tok)
	writeFile(t, filepath.Join(e.teDir, "qwen.safetensors"))

	rec := deleteTE(t, e.mux, "moss.tokenizer.json")
	require.Less(t, rec.Code, 300, rec.Body.String())

	assert.NoFileExists(t, tok)
	assert.FileExists(t, filepath.Join(e.teDir, "qwen.safetensors"))
	assert.Equal(t, []string{"qwen.safetensors"}, listNames(t, e.mux, "/api/server/models/text_encoder"))
}

func TestTextEncoderDeletePrefersTextEncoderDir(t *testing.T) {
	e := newTokEnv(t)
	inTE := filepath.Join(e.teDir, "moss.tokenizer.json")
	inTok := filepath.Join(e.tokDir, "moss.tokenizer.json")
	writeFile(t, inTE)
	writeFile(t, inTok)

	rec := deleteTE(t, e.mux, "moss.tokenizer.json")
	require.Less(t, rec.Code, 300, rec.Body.String())

	assert.NoFileExists(t, inTE)
	assert.FileExists(t, inTok)
}

func TestTextEncoderDeleteMissingInBothDirs(t *testing.T) {
	e := newTokEnv(t)
	other := filepath.Join(e.tokDir, "other.json")
	writeFile(t, other)

	rec := deleteTE(t, e.mux, "moss.tokenizer.json")
	assert.GreaterOrEqual(t, rec.Code, 400)
	assert.FileExists(t, other)
}
