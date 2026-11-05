package main

import (
	"os"
	"path/filepath"
	"testing"

	"imagecaster-server/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirstRunConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfgFile func(dataDir string) string
	}{
		{"config inside data dir", func(dataDir string) string { return filepath.Join(dataDir, configName) }},
		{"config elsewhere", func(string) string { return filepath.Join(t.TempDir(), "custom.yaml") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dataDir := filepath.Join(t.TempDir(), "picked")
			require.NoError(t, os.MkdirAll(dataDir, 0o755))
			cfgFile := tt.cfgFile(dataDir)

			cfg, err := firstRunConfig(cfgFile, dataDir)

			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, dataDir, cfg.DataDir)
			_, err = os.Stat(cfgFile)
			require.NoError(t, err)
			loaded, err := config.Load(cfgFile)
			require.NoError(t, err)
			assert.Equal(t, dataDir, loaded.DataDir)
			assert.Equal(t, dataDir, config.SavedDataDir())
			assert.Equal(t, dataDir, config.DefaultDataDir())
			_, err = os.Stat(filepath.Join(home, "imagecaster-server", configName))
			assert.True(t, os.IsNotExist(err))
		})
	}
}
