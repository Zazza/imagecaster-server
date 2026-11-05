package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const legacyProcesses = `processes:
  python:
    name: Python
  sd:
    name: Stable Diffusion
    autostart: true
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

func TestLoadMigratesLegacyConfig(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantActive string
		wantKeep   string
	}{
		{
			name: "forge to neo",
			yaml: "active_sd: forge\n" + legacyProcesses + `backends:
  forge:
    name: Forge
    process_key: sd
    binary: forge/venv/bin/python
`,
			wantActive: NeoBackendKey,
		},
		{
			name:       "unknown active",
			yaml:       "active_sd: nope\n" + legacyProcesses,
			wantActive: NeoBackendKey,
		},
		{
			name: "custom backend kept",
			yaml: "active_sd: my-neo\n" + legacyProcesses + `backends:
  my-neo:
    name: My Neo
    process_key: sd
    binary: my-neo/venv/bin/python
    workdir: my-neo
`,
			wantActive: "my-neo",
			wantKeep:   "my-neo",
		},
	}
	for _, tt := range tests {
		for lname, load := range loaders(t.TempDir()) {
			t.Run(tt.name+"/"+lname, func(t *testing.T) {
				dataDir := t.TempDir()
				body := "data_dir: " + filepath.ToSlash(dataDir) + "\n" + tt.yaml
				cfg, err := load(writeYAML(t, body))
				require.NoError(t, err)

				assert.NotContains(t, cfg.Processes, "python")
				assert.NotContains(t, cfg.Backends, "forge")
				assert.Equal(t, tt.wantActive, cfg.ActiveSD)
				assert.Contains(t, cfg.Backends, cfg.ActiveSD)
				if tt.wantKeep != "" {
					assert.Equal(t, "My Neo", cfg.Backends[tt.wantKeep].Name)
				}
			})
		}
	}
}

func venvBinary(root string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(root, "venv", "bin", "python")
}

func neoCfgWithBinary(t *testing.T, binary string) Config {
	t.Helper()
	cfg := NewDefault()
	cfg.DataDir = t.TempDir()
	b := cfg.Backends[NeoBackendKey]
	b.Binary = binary
	b.AutoOptimize = false
	cfg.Backends[NeoBackendKey] = b
	return cfg
}

func TestApplyBackendToProcessUvEnv(t *testing.T) {
	root := filepath.Join(t.TempDir(), "neo")
	cfg := neoCfgWithBinary(t, venvBinary(root))

	cfg.ApplyBackendToProcess(fakeOptimizer{})

	env := cfg.Processes["sd"].Env
	assert.Equal(t, filepath.Join(root, "venv"), env["VIRTUAL_ENV"])
	assert.Equal(t, filepath.Join(cfg.DataDir, "uv-cache"), env["UV_CACHE_DIR"])
	assert.Equal(t, filepath.Join(cfg.DataDir, "python"), env["UV_PYTHON_INSTALL_DIR"])
}

func TestApplyBackendToProcessNoVenvForOtherBinary(t *testing.T) {
	tests := []struct {
		name   string
		binary string
	}{
		{"system python", filepath.Join(string(filepath.Separator), "usr", "bin", "python3")},
		{"relative venv", filepath.Join("neo", "venv", "bin", "python")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := neoCfgWithBinary(t, tt.binary)

			cfg.ApplyBackendToProcess(fakeOptimizer{})

			env := cfg.Processes["sd"].Env
			_, has := env["VIRTUAL_ENV"]
			assert.False(t, has)
			assert.Equal(t, filepath.Join(cfg.DataDir, "uv-cache"), env["UV_CACHE_DIR"])
			assert.Equal(t, filepath.Join(cfg.DataDir, "python"), env["UV_PYTHON_INSTALL_DIR"])
		})
	}
}

func TestApplyBackendToProcessKeepsExplicitEnv(t *testing.T) {
	cfg := neoCfgWithBinary(t, venvBinary(filepath.Join(t.TempDir(), "neo")))
	p := cfg.Processes["sd"]
	p.Env = map[string]string{
		"VIRTUAL_ENV":           "/custom/venv",
		"UV_CACHE_DIR":          "/custom/cache",
		"UV_PYTHON_INSTALL_DIR": "/custom/python",
	}
	cfg.Processes["sd"] = p

	cfg.ApplyBackendToProcess(fakeOptimizer{})

	env := cfg.Processes["sd"].Env
	assert.Equal(t, "/custom/venv", env["VIRTUAL_ENV"])
	assert.Equal(t, "/custom/cache", env["UV_CACHE_DIR"])
	assert.Equal(t, "/custom/python", env["UV_PYTHON_INSTALL_DIR"])
}

func TestApplyBackendToProcessDoesNotMutateSourceEnv(t *testing.T) {
	cfg := neoCfgWithBinary(t, venvBinary(filepath.Join(t.TempDir(), "neo")))
	src := cfg.Processes["sd"].Env
	before := map[string]string{}
	for k, v := range src {
		before[k] = v
	}

	cfg.ApplyBackendToProcess(fakeOptimizer{})

	assert.Equal(t, before, src)
	assert.Equal(t, NewDefault().Processes["sd"].Env, before)
}
