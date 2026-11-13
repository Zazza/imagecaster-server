package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOllamaDirCandidates(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "OLLAMA_MODELS" {
				return v
			}
			return ""
		}
	}
	tests := []struct {
		name string
		goos string
		env  string
		home string
		want []string
	}{
		{"linux env", "linux", "/data/ollama", "/home/u", []string{"/data/ollama", "/usr/share/ollama/.ollama/models"}},
		{"linux home", "linux", "", "/home/u", []string{filepath.Join("/home/u", ".ollama", "models"), "/usr/share/ollama/.ollama/models"}},
		{"windows home", "windows", "", `C:\Users\u`, []string{filepath.Join(`C:\Users\u`, ".ollama", "models")}},
		{"darwin home", "darwin", "", "/Users/u", []string{filepath.Join("/Users/u", ".ollama", "models")}},
		{"darwin env", "darwin", "/Volumes/x", "/Users/u", []string{"/Volumes/x"}},
		{"no home no env", "darwin", "", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, OllamaDirCandidates(tt.goos, env(tt.env), tt.home))
		})
	}
}

func TestFilterOllamaDirs(t *testing.T) {
	base := t.TempDir()
	own := filepath.Join(base, "own")
	a := filepath.Join(base, "a")
	require.NoError(t, os.MkdirAll(own, 0o755))
	require.NoError(t, os.MkdirAll(a, 0o755))
	link := filepath.Join(base, "own-link")
	require.NoError(t, os.Symlink(own, link))
	file := filepath.Join(base, "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	got := FilterOllamaDirs([]string{filepath.Join(base, "missing"), a, a + string(filepath.Separator), own, link, file}, own)
	assert.Equal(t, []string{a}, got)
}

func TestForeignOllamaDirsSwitchedOff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OLLAMA_MODELS", dir)
	cfg := NewDefault()
	cfg.DataDir = t.TempDir()
	assert.Contains(t, cfg.ForeignOllamaDirs(), dir)

	off := false
	cfg.LLM.ReadOllama = &off
	assert.Empty(t, cfg.ForeignOllamaDirs())
}

func TestForeignOllamaDirsSkipsOwn(t *testing.T) {
	cfg := NewDefault()
	cfg.DataDir = t.TempDir()
	require.NoError(t, os.MkdirAll(cfg.LLMModelsDir(), 0o755))
	t.Setenv("OLLAMA_MODELS", cfg.LLMModelsDir())
	assert.NotContains(t, cfg.ForeignOllamaDirs(), cfg.LLMModelsDir())
}

func TestReadOllamaModelsFromYAML(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "server-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("llm:\n  read_ollama_models: false\n"), 0o644))
	cfg, err := LoadWithDir(path, dataDir)
	require.NoError(t, err)
	require.NotNil(t, cfg.LLM.ReadOllama)
	assert.False(t, *cfg.LLM.ReadOllama)
}
