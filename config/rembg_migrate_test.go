package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDropsLegacyRembg(t *testing.T) {
	body := `processes:
  rembg:
    name: Rembg
    binary: rembg
    args: ["s", "--port", "7000"]
    proxy_path: /api/rembg/
    target_url: http://localhost:7000
  sd:
    name: Stable Diffusion
    autostart: true
`
	for name, load := range loaders(t.TempDir()) {
		t.Run(name, func(t *testing.T) {
			cfg, err := load(writeYAML(t, body))
			require.NoError(t, err)
			assert.NotContains(t, cfg.Processes, "rembg")
			assert.Contains(t, cfg.Processes, "sd")
		})
	}
}
