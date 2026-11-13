package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const legacyProcesses = `processes:
  python:
    name: Python
  sd:
    name: Stable Diffusion
    autostart: true
`

const forgeBackends = `  forge:
    name: Forge
    process_key: sd
    binary: forge/venv/bin/python
  forge-neo:
    name: Forge Neo
    process_key: sd
    binary: sd-webui-forge-neo/venv/bin/python
    models_dir: sd-webui-forge-neo/models/Stable-diffusion
`

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

func loaders(dataDir string) map[string]func(string) (*Config, error) {
	return map[string]func(string) (*Config, error){
		"Load":        Load,
		"LoadWithDir": func(p string) (*Config, error) { return LoadWithDir(p, dataDir) },
	}
}

func TestLoadDropsForgeBackendsKeepsExplicitSDCpp(t *testing.T) {
	for lname, load := range loaders(t.TempDir()) {
		t.Run(lname, func(t *testing.T) {
			dataDir := t.TempDir()
			mine := filepath.ToSlash(t.TempDir())
			body := "data_dir: " + filepath.ToSlash(dataDir) + "\nactive_sd: forge-neo\nactive_llm: llamacpp\n# keep me\n" + legacyProcesses +
				"backends:\n" + forgeBackends +
				"  sdcpp:\n    name: sd.cpp\n    kind: sdcpp\n    process_key: sd\n    models_dir: " + mine + "/ckpt\n    lora_dir: " + mine + "/lora\n"
			path := writeYAML(t, body)

			cfg, err := load(path)
			require.NoError(t, err)
			assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
			assert.Equal(t, []string{SDCppBackendKey}, keys(cfg.Backends))
			assert.Equal(t, filepath.FromSlash(mine+"/ckpt"), cfg.GetActiveBackend().ModelsDir)
			assert.Equal(t, filepath.FromSlash(mine+"/lora"), cfg.GetActiveBackend().LoraDir)
			assert.NotContains(t, cfg.Processes, "python")

			backup, err := os.ReadFile(path + ForgeRemovalBackupSuffix)
			require.NoError(t, err)
			assert.Equal(t, body, string(backup))
			saved, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Contains(t, string(saved), "active_sd: sdcpp")
			assert.Contains(t, string(saved), "# keep me")
			assert.NotContains(t, string(saved), "forge")
			assert.Contains(t, string(saved), mine+"/ckpt")

			again, err := load(path)
			require.NoError(t, err)
			assert.Equal(t, SDCppBackendKey, again.ActiveSD)
			savedAgain, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(saved), string(savedAgain))
		})
	}
}

func TestLoadDropsForgeBackendsAdoptsModels(t *testing.T) {
	for lname, load := range loaders(t.TempDir()) {
		t.Run(lname, func(t *testing.T) {
			dataDir := t.TempDir()
			models := filepath.Join(dataDir, "sd-webui-forge-neo", "models", "Stable-diffusion")
			require.NoError(t, os.MkdirAll(models, 0o755))
			body := "data_dir: " + filepath.ToSlash(dataDir) + "\nactive_sd: forge-neo\nactive_llm: llamacpp\n" + legacyProcesses + "backends:\n" + forgeBackends
			path := writeYAML(t, body)

			cfg, err := load(path)
			require.NoError(t, err)
			assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
			assert.Equal(t, []string{SDCppBackendKey}, keys(cfg.Backends))
			b := cfg.GetActiveBackend()
			assert.Equal(t, KindSDCpp, b.Kind)
			assert.Equal(t, models, b.ModelsDir)
			assert.Equal(t, filepath.Join(filepath.Dir(models), "Lora"), b.LoraDir)
			assert.Equal(t, filepath.Join(filepath.Dir(models), "VAE"), b.VaeDir)

			again, err := load(path)
			require.NoError(t, err)
			assert.Equal(t, models, again.GetActiveBackend().ModelsDir)
		})
	}
}

func TestLoadDropsForgeBackendsWithoutModelsUsesDefaults(t *testing.T) {
	dataDir := t.TempDir()
	body := "data_dir: " + filepath.ToSlash(dataDir) + "\nactive_sd: forge\nactive_llm: llamacpp\n" + legacyProcesses + "backends:\n" + forgeBackends
	path := writeYAML(t, body)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
	assert.Equal(t, filepath.Join(dataDir, "models", "checkpoints"), cfg.GetActiveBackend().ModelsDir)
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(saved), "forge")
}

func TestLoadUnknownActiveSwitchesToSDCpp(t *testing.T) {
	path := writeYAML(t, "active_sd: nope\nactive_llm: llamacpp\n")
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
	assert.Contains(t, cfg.Backends, SDCppBackendKey)
}

func TestLoadKeepsCustomSDCppBackend(t *testing.T) {
	body := "active_sd: my-sd\nactive_llm: llamacpp\nbackends:\n  my-sd:\n    name: Mine\n    kind: sdcpp\n    process_key: sd\n"
	path := writeYAML(t, body)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "my-sd", cfg.ActiveSD)
	assert.Equal(t, "Mine", cfg.Backends["my-sd"].Name)
	saved, _ := os.ReadFile(path)
	assert.Equal(t, body, string(saved))
	_, err = os.Stat(path + ForgeRemovalBackupSuffix)
	assert.True(t, os.IsNotExist(err))
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

const forgeLeftoverProcesses = `processes:
  sd:
    name: Stable Diffusion
    binary: /mnt/e/SDStudio/python/bin/python3
    args: [launch.py, --listen, --api, --medvram-sdxl]
    env:
      PYTORCH_CUDA_ALLOC_CONF: expandable_segments:True
    workdir: /mnt/e/SDStudio/stable-diffusion-webui-forge
    health_url: http://localhost:7860/sdapi/v1/options
    target_url: http://localhost:7860
    proxy_path: /api/sd/
    autostart: true
    restart: true
    max_restart: 5
    install:
      method: zip
      url: https://github.com/lllyasviel/stable-diffusion-webui-forge/archive/refs/heads/main.zip
      target: stable-diffusion-webui-forge
  python:
    name: Python 3.10
    category: utility
    install:
      method: tgz
      url: https://github.com/indygreg/python-build-standalone/releases/download/x/cpython-3.10.tar.gz
      target: /mnt/e/SDStudio/python
  ollama:
    name: Ollama
    binary: ollama
    args: [serve]
    env:
      OLLAMA_MODELS: /mnt/e/x
    health_url: http://localhost:11434/api/tags
    target_url: http://localhost:11434
    proxy_path: /api/llm/
    autostart: true
`

func TestLoadCleansForgeProcessLeftovers(t *testing.T) {
	for lname, load := range loaders(t.TempDir()) {
		t.Run(lname, func(t *testing.T) {
			dataDir := t.TempDir()
			body := "data_dir: " + filepath.ToSlash(dataDir) + "\nactive_sd: sdcpp\nactive_llm: llamacpp\n" + forgeLeftoverProcesses +
				"backends:\n  sdcpp:\n    name: sd.cpp\n    kind: sdcpp\n    process_key: sd\n    binary: engines/sd-cpp-linux/sd-server\n    workdir: engines/sd-cpp-linux\n    models_dir: /mnt/e/SDStudio/stable-diffusion-webui-forge/models/Stable-diffusion\n    lora_dir: /mnt/e/SDStudio/stable-diffusion-webui-forge/models/Lora\n"
			path := writeYAML(t, body)

			cfg, err := load(path)
			require.NoError(t, err)
			assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
			assert.NotContains(t, cfg.Processes, "python")
			sd := cfg.Processes["sd"]
			assert.NotContains(t, sd.Args, "launch.py")
			assert.Contains(t, sd.HealthURL, "/sdcpp/v1/capabilities")
			assert.NotEqual(t, InstallMethod("zip"), sd.Install.Method)

			_, err = os.Stat(path + ForgeRemovalBackupSuffix)
			assert.True(t, os.IsNotExist(err))
			backup, err := os.ReadFile(path + ForgeCleanupBackupSuffix)
			require.NoError(t, err)
			assert.Equal(t, body, string(backup))

			saved, err := os.ReadFile(path)
			require.NoError(t, err)
			var doc struct {
				Processes map[string]ProcessConfig `yaml:"processes"`
			}
			require.NoError(t, yaml.Unmarshal(saved, &doc))
			assert.NotContains(t, doc.Processes, "python")
			fsd := doc.Processes["sd"]
			assert.Equal(t, "engines/sd-cpp-linux/sd-server", fsd.Binary)
			assert.Equal(t, "engines/sd-cpp-linux", fsd.WorkDir)
			assert.Equal(t, []string{"--lora-model-dir", "/mnt/e/SDStudio/stable-diffusion-webui-forge/models/Lora", "--listen-ip", "127.0.0.1", "--listen-port", "7860"}, fsd.Args)
			assert.Equal(t, "http://localhost:7860/sdcpp/v1/capabilities", fsd.HealthURL)
			assert.Empty(t, fsd.Env)
			assert.Empty(t, fsd.Install.Method)
			assert.Equal(t, "/api/sd/", fsd.ProxyPath)
			assert.True(t, fsd.AutoStart)
			for _, bad := range []string{"python", "launch.py", "sdapi", "PYTORCH", "webui-forge/archive", "zip"} {
				assert.NotContains(t, sectionOf(string(saved), "  sd:", "  ollama:"), bad)
			}
			assert.Contains(t, string(saved), "OLLAMA_MODELS: /mnt/e/x")
			assert.Contains(t, string(saved), "http://localhost:11434/api/tags")

			again, err := load(path)
			require.NoError(t, err)
			assert.Equal(t, SDCppBackendKey, again.ActiveSD)
			require.NotEmpty(t, sectionOf(string(saved), "  sd:", "  ollama:"))
			savedAgain, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(saved), string(savedAgain))
		})
	}
}

func TestLoadKeepsCleanSDProcess(t *testing.T) {
	body := "active_sd: sdcpp\nactive_llm: llamacpp\nprocesses:\n  sd:\n    name: SD\n    env:\n      CUDA_VISIBLE_DEVICES: \"1\"\n    health_url: http://localhost:7860/sdcpp/v1/capabilities\n    target_url: http://localhost:7860\n    proxy_path: /api/sd/\n    autostart: true\n"
	path := writeYAML(t, body)
	_, err := Load(path)
	require.NoError(t, err)
	saved, _ := os.ReadFile(path)
	assert.Equal(t, body, string(saved))
	_, err = os.Stat(path + ForgeCleanupBackupSuffix)
	assert.True(t, os.IsNotExist(err))
}

func sectionOf(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	if j := strings.Index(rest, to); j >= 0 {
		return rest[:j]
	}
	return rest
}
