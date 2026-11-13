package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXReviewForgeMigrationAdoptsOriginallyActiveModelsWithoutActiveLLM(t *testing.T) {
	cases := []struct {
		name   string
		active string
		want   string
	}{
		{"forge-neo active", "forge-neo", "neo"},
		{"forge active", "forge", "classic"},
	}
	for _, tc := range cases {
		for lname, load := range loaders(t.TempDir()) {
			t.Run(tc.name+"/"+lname, func(t *testing.T) {
				dataDir := t.TempDir()
				dirs := map[string]string{
					"classic": filepath.Join(dataDir, "forge", "models", "Stable-diffusion"),
					"neo":     filepath.Join(dataDir, "sd-webui-forge-neo", "models", "Stable-diffusion"),
				}
				for _, d := range dirs {
					require.NoError(t, os.MkdirAll(d, 0o755))
				}
				body := "data_dir: " + filepath.ToSlash(dataDir) + "\nactive_sd: " + tc.active + "\n" + legacyProcesses +
					"backends:\n" +
					"  forge:\n    name: Forge\n    process_key: sd\n    binary: forge/venv/bin/python\n    models_dir: " + filepath.ToSlash(dirs["classic"]) + "\n" +
					"  forge-neo:\n    name: Forge Neo\n    process_key: sd\n    binary: sd-webui-forge-neo/venv/bin/python\n    models_dir: " + filepath.ToSlash(dirs["neo"]) + "\n"
				path := writeYAML(t, body)

				cfg, err := load(path)
				require.NoError(t, err)
				assert.Equal(t, SDCppBackendKey, cfg.ActiveSD)
				require.Contains(t, cfg.Backends, SDCppBackendKey)
				assert.Equal(t, dirs[tc.want], cfg.Backends[SDCppBackendKey].ModelsDir)
				assert.Equal(t, dirs[tc.want], cfg.GetActiveBackend().ModelsDir)

				again, err := load(path)
				require.NoError(t, err)
				assert.Equal(t, dirs[tc.want], again.GetActiveBackend().ModelsDir)
			})
		}
	}
}
