package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	assert.Equal(t, LlamaCppProcessKey, cfg.LLMProcessKey())
	assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
	assert.Equal(t, KindSDCpp, cfg.GetActiveBackend().Kind)

	cfg.ApplyLLMBackend()
	assert.True(t, cfg.Processes[LlamaCppProcessKey].AutoStart)
	assert.Equal(t, "/api/llm/", cfg.Processes[LlamaCppProcessKey].ProxyPath)
	assert.False(t, cfg.Processes["ollama"].AutoStart)
	assert.Empty(t, cfg.Processes["ollama"].ProxyPath)
	assert.Equal(t, []string{"serve"}, cfg.Processes["ollama"].Args, "ollama settings stay for rollback")
}

func TestOllamaChosenExplicitlyStays(t *testing.T) {
	cfg := loadText(t, "active_llm: ollama\n"+oldConfig184)
	assert.Equal(t, LLMOllama, cfg.ActiveLLM)
	cfg.ApplyLLMBackend()
	assert.True(t, cfg.Processes["ollama"].AutoStart)
	guards := cfg.ApplyPortGuard()
	assert.Equal(t, []string{"ollama", "sd"}, guardNames(guards))
	assert.Equal(t, "127.0.0.1:11435", cfg.Processes["ollama"].Env["OLLAMA_HOST"])
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
	ol := reloaded.Processes["ollama"]
	assert.False(t, ol.AutoStart)
	assert.Empty(t, ol.ProxyPath)

	guards := reloaded.ApplyPortGuard()
	assert.Contains(t, guards, PortGuard{Process: LlamaCppProcessKey, ListenAddr: ":11434", ProxyPath: "/api/llm/"})
	assert.NotContains(t, guardNames(guards), "ollama")
	llm = reloaded.Processes[LlamaCppProcessKey]
	assert.Equal(t, "http://127.0.0.1:11435", llm.TargetURL)
	assert.Equal(t, "http://127.0.0.1:11435/health", llm.HealthURL)
	assert.Equal(t, []string{"--models-preset", filepath.Join(dir, LlamaPresetName), "--models-max", "1", "--host", "127.0.0.1", "--port", "11435"}, llm.Args)
}

func TestSwitchBackendIsOneValue(t *testing.T) {
	cfg := loadText(t, "active_llm: llamacpp\n"+oldConfig184)
	cfg.ApplyLLMBackend()
	assert.True(t, cfg.Processes[LlamaCppProcessKey].AutoStart)
	assert.False(t, cfg.Processes["ollama"].AutoStart)

	cfg = loadText(t, "active_llm: ollama\n"+oldConfig184)
	cfg.ApplyLLMBackend()
	assert.True(t, cfg.Processes["ollama"].AutoStart)
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
	assert.Equal(t, LLMConfig{ContextSize: 8192, ModelsMax: 1}, c.LLMSettings())
	assert.Equal(t, filepath.Join("/d", "models", "ollama"), c.OllamaModelsDir())
	c.Processes["ollama"] = ProcessConfig{Env: map[string]string{"OLLAMA_MODELS": "/mnt/e/SDStudio/models/ollama"}}
	assert.Equal(t, "/mnt/e/SDStudio/models/ollama", c.OllamaModelsDir())
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
		name   string
		env    map[string]string
		vramMB int
		want   string
	}{
		{"ollama env on big card", map[string]string{"OLLAMA_KEEP_ALIVE": "24h"}, 24576, "24h"},
		{"16 GB card forces 5m like Ollama", map[string]string{"OLLAMA_KEEP_ALIVE": "24h"}, 16376, "5m"},
		{"unknown VRAM keeps env", map[string]string{"OLLAMA_KEEP_ALIVE": "1h"}, 0, "1h"},
		{"no env", nil, 24576, "5m"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Processes: map[string]ProcessConfig{"ollama": {Env: tt.env}}}
			assert.Equal(t, tt.want, c.LlamaKeepAlive(tt.vramMB))
		})
	}
	d := NewDefault()
	assert.Equal(t, "24h", d.LlamaKeepAlive(0), "default ollama env OLLAMA_KEEP_ALIVE is 24h")
}
