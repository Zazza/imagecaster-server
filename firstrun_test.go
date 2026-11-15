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
			loaded, err := config.LoadWithDir(cfgFile, config.DefaultDataDir())
			require.NoError(t, err)
			assert.Equal(t, dataDir, loaded.DataDir)
			assert.Equal(t, dataDir, config.SavedDataDir())
			assert.Equal(t, dataDir, config.DefaultDataDir())
			_, err = os.Stat(filepath.Join(home, "imagecaster-server", configName))
			assert.True(t, os.IsNotExist(err))
		})
	}
}

func TestAbsDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	cwd, _ := os.Getwd()
	cases := map[string]string{
		"~":           home,
		"~/ai":        filepath.Join(home, "ai"),
		"imagecaster": filepath.Join(cwd, "imagecaster"),
	}
	for in, want := range cases {
		if got := absDir(in); got != want {
			t.Errorf("absDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveConfigAtomicKeepsPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), configName)
	require.NoError(t, os.WriteFile(p, []byte("old"), 0o600))
	cfg := config.NewDefault()
	require.NoError(t, saveConfig(p, &cfg))
	st, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	assert.NoFileExists(t, p+".tmp")
	loaded, err := config.LoadWithDir(p, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, cfg.Port, loaded.Port)
}
