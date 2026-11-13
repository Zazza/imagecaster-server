package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oldConfig184 = `port: 8080
mdns: true
active_sd: forge
processes:
  sd:
    name: "Stable Diffusion"
    health_url: "http://localhost:7860/sdapi/v1/options"
    target_url: "http://localhost:7860"
    proxy_path: "/api/sd/"
    autostart: true
    restart: true
    max_restart: 5
  ollama:
    name: "Ollama"
    binary: "ollama"
    args: ["serve"]
    env:
      OLLAMA_CONTEXT_LENGTH: "4096"
    health_url: "http://localhost:11434/api/tags"
    target_url: "http://localhost:11434"
    proxy_path: "/api/llm/"
    autostart: true
    restart: true
    max_restart: 5
backends:
  forge:
    name: "Stable Diffusion Forge"
    process_key: "sd"
    binary: "./stable-diffusion-webui-forge/webui.sh"
`

func loadText(t *testing.T, text string) *Config {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "server-config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(text), 0o644))
	cfg, err := LoadWithDir(p, dir)
	require.NoError(t, err)
	return cfg
}

func guardNames(gs []PortGuard) []string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Process)
	}
	return out
}

func TestOldConfigMovesToNewEngines(t *testing.T) {
	cfg := loadText(t, oldConfig184)
	assert.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)
	assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
	assert.Equal(t, KindSDCpp, cfg.GetActiveBackend().Kind)
	_, hasOllama := cfg.Processes["ollama"]
	assert.False(t, hasOllama, "ollama process is gone")

	cfg.ApplyLLMBackend()
	assert.True(t, cfg.Processes[LlamaCppProcessKey].AutoStart)
	assert.Equal(t, "/api/llm/", cfg.Processes[LlamaCppProcessKey].ProxyPath)
	assert.Equal(t, filepath.Join(cfg.DataDir, "models", "ollama"), cfg.LLMModelsDir())
}

func TestOldOllamaConfigAdopted(t *testing.T) {
	withModels := func(env string) string {
		return strings.Replace(oldConfig184, "      OLLAMA_CONTEXT_LENGTH: \"4096\"\n", "      OLLAMA_CONTEXT_LENGTH: \"4096\"\n"+env, 1)
	}
	cases := []struct {
		name          string
		text          string
		wantModelsDir func(dataDir string) string
		wantKeepAlive string
	}{
		{"absolute models dir", "active_llm: ollama\n" + withModels("      OLLAMA_MODELS: /mnt/e/x\n"), func(string) string { return "/mnt/e/x" }, "24h"},
		{"relative models dir", withModels("      OLLAMA_MODELS: models/ollama\n"), func(d string) string { return filepath.Join(d, "models", "ollama") }, "24h"},
		{"windows relative models dir", withModels("      OLLAMA_MODELS: models\\ollama\n"), func(d string) string { return filepath.Join(d, `models\ollama`) }, "24h"},
		{"keep alive moves", withModels("      OLLAMA_KEEP_ALIVE: 1h\n"), func(d string) string { return filepath.Join(d, "models", "ollama") }, "1h"},
		{"explicit llm.models_dir wins", "llm:\n  models_dir: /srv/own\n" + withModels("      OLLAMA_MODELS: /mnt/e/x\n"), func(string) string { return "/srv/own" }, "24h"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadText(t, tt.text)
			assert.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)
			_, hasOllama := cfg.Processes["ollama"]
			assert.False(t, hasOllama)
			assert.Equal(t, tt.wantModelsDir(cfg.DataDir), cfg.LLMModelsDir())
			assert.Equal(t, tt.wantKeepAlive, cfg.LLMSettings().KeepAlive)
		})
	}
}

func TestOldConfigFileNotRewrittenByAdoption(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "server-config.yaml")
	noForge := strings.Replace(oldConfig184[:strings.Index(oldConfig184, "backends:")], "active_sd: forge", "active_sd: sdcpp", 1)
	noForge = strings.Replace(noForge, "/sdapi/v1/options", "/sdcpp/v1/capabilities", 1)
	text := "active_llm: ollama\n" + noForge
	require.NoError(t, os.WriteFile(p, []byte(text), 0o644))
	_, err := LoadWithDir(p, dir)
	require.NoError(t, err)
	got, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, text, string(got))
}

func TestNewInstallUsesLlamaCpp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "server-config.yaml")
	cfg, err := LoadWithDir(p, dir)
	require.NoError(t, err)
	assert.Equal(t, LLMLlamaCpp, cfg.ActiveLLM)

	reloaded, err := LoadWithDir(p, dir)
	require.NoError(t, err)
	assert.Equal(t, LLMLlamaCpp, reloaded.ActiveLLM, "template written for a new install keeps llamacpp")

	reloaded.ApplyLLMBackend()
	llm := reloaded.Processes[LlamaCppProcessKey]
	assert.True(t, llm.AutoStart)
	assert.Equal(t, filepath.Join(dir, "engines", "llama-cpp-"+runtime.GOOS, "llama-server"+exeSuffix()), llm.Binary)
	assert.Equal(t, InstallRelease, llm.Install.Method)
	assert.Equal(t, LlamaCppVersion, llm.Install.Version)
	assert.Equal(t, filepath.Join(dir, "engines", "llama-cpp-"+runtime.GOOS), llm.Install.Target)
	_, hasOllama := reloaded.Processes["ollama"]
	assert.False(t, hasOllama)

	guards := reloaded.ApplyPortGuard()
	assert.Contains(t, guards, PortGuard{Process: LlamaCppProcessKey, ListenAddr: ":11434", ProxyPath: "/api/llm/"})
	assert.NotContains(t, guardNames(guards), "ollama")
	llm = reloaded.Processes[LlamaCppProcessKey]
	assert.Equal(t, "http://127.0.0.1:11435", llm.TargetURL)
	assert.Equal(t, "http://127.0.0.1:11435/health", llm.HealthURL)
	assert.Equal(t, []string{"--models-preset", filepath.Join(dir, LlamaPresetName), "--models-max", "1", "--host", "127.0.0.1", "--port", "11435"}, llm.Args)
}

func TestLLMOffWithoutLlamaBuild(t *testing.T) {
	prev := llamaCppOnThisPlatform
	llamaCppOnThisPlatform = func() bool { return false }
	t.Cleanup(func() { llamaCppOnThisPlatform = prev })

	c := NewDefault()
	c.DataDir = "/data"
	c.ApplyLLMBackend()
	llm := c.Processes[LlamaCppProcessKey]
	assert.False(t, llm.AutoStart)
	assert.Empty(t, llm.ProxyPath)
	assert.Equal(t, InstallConfig{}, llm.Install)
	assert.NotContains(t, guardNames(c.ApplyPortGuard()), LlamaCppProcessKey)
}

func TestLlamaArgsWithoutGuard(t *testing.T) {
	c := NewDefault()
	c.DataDir = "/data"
	c.GPU.GuardPorts = false
	c.LLM.ModelsMax = 2
	pc := c.Processes[LlamaCppProcessKey]
	pc.Args = []string{"--models-max", "9", "--port", "1", "-ngl", "99"}
	c.Processes[LlamaCppProcessKey] = pc
	c.ApplyLLMBackend()
	assert.Empty(t, c.ApplyPortGuard())
	args := c.Processes[LlamaCppProcessKey].Args
	assert.Equal(t, []string{"-ngl", "99", "--models-preset", filepath.Join("/data", LlamaPresetName), "--models-max", "2", "--host", "127.0.0.1", "--port", "11435"}, args)
	assert.False(t, slices.Contains(args, "9"))
	c.PinLlamaInternal()
	assert.Equal(t, "http://127.0.0.1:11435", c.Processes[LlamaCppProcessKey].TargetURL)
}

func TestLLMSettingsDefaultsAndStore(t *testing.T) {
	c := Config{DataDir: "/d", Processes: map[string]ProcessConfig{}}
	assert.Equal(t, LLMConfig{ContextSize: 8192, ModelsMax: 1, KeepAlive: "24h"}, c.LLMSettings())
	assert.Equal(t, filepath.Join("/d", "models", "ollama"), c.LLMModelsDir())
	c.LLM.ModelsDir = "/mnt/e/SDStudio/models/ollama"
	assert.Equal(t, "/mnt/e/SDStudio/models/ollama", c.LLMModelsDir())
	c.LLM.ModelsDir = "llm-models"
	assert.Equal(t, filepath.Join("/d", "llm-models"), c.LLMModelsDir())
}

func TestLlamaInternalWhen11434Busy(t *testing.T) {
	c := NewDefault()
	c.DataDir = "/data"
	c.ApplyLLMBackend()
	guards := c.ApplyPortGuardIf(func(g PortGuard) bool { return g.Process != LlamaCppProcessKey })
	assert.Equal(t, []string{"sd"}, guardNames(guards))
	c.PinLlamaInternal()
	llm := c.Processes[LlamaCppProcessKey]
	assert.Equal(t, "http://127.0.0.1:11435", llm.TargetURL)
	assert.Equal(t, "http://127.0.0.1:11435/health", llm.HealthURL)
	assert.Equal(t, []string{"--host", "127.0.0.1", "--port", "11435"}, llm.Args[len(llm.Args)-4:])
	assert.NotContains(t, llm.Args, "11434")
}

func TestLlamaKeepAliveDefault(t *testing.T) {
	cases := []struct {
		name      string
		keepAlive string
		vramMB    int
		want      string
	}{
		{"default on big card", "", 24576, "24h"},
		{"16 GB card forces 5m", "24h", 16376, "5m"},
		{"unknown VRAM keeps setting", "1h", 0, "1h"},
		{"own setting on big card", "2h", 24576, "2h"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{LLM: LLMConfig{KeepAlive: tt.keepAlive}}
			assert.Equal(t, tt.want, c.LlamaKeepAlive(tt.vramMB))
		})
	}
	d := NewDefault()
	assert.Equal(t, "24h", d.LlamaKeepAlive(0))
}
