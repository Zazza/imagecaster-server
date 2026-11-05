package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dataDirEnv struct {
	home    string
	cfgHome string
}

func isolateDataDirEnv(t *testing.T) dataDirEnv {
	t.Helper()
	e := dataDirEnv{home: t.TempDir(), cfgHome: t.TempDir()}
	t.Setenv("HOME", e.home)
	t.Setenv("XDG_CONFIG_HOME", e.cfgHome)
	return e
}

func (e dataDirEnv) pointer() string {
	return filepath.Join(e.cfgHome, "imagecaster-server", "data-dir")
}

func (e dataDirEnv) writePointer(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(e.pointer()), 0o755))
	require.NoError(t, os.WriteFile(e.pointer(), []byte(content), 0o644))
}

func TestSaveDataDirWritesPointerInUserConfigDir(t *testing.T) {
	e := isolateDataDirEnv(t)
	dir := filepath.Join(t.TempDir(), "picked")
	require.NoError(t, os.Mkdir(dir, 0o755))

	require.NoError(t, SaveDataDir(dir))

	_, err := os.Stat(e.pointer())
	require.NoError(t, err)
	assert.Equal(t, dir, SavedDataDir())
	assert.Equal(t, dir, DefaultDataDir())
}

func TestSaveDataDirOverwritesPrevious(t *testing.T) {
	isolateDataDirEnv(t)
	first := t.TempDir()
	second := t.TempDir()

	require.NoError(t, SaveDataDir(first))
	require.NoError(t, SaveDataDir(second))

	assert.Equal(t, second, DefaultDataDir())
}

func TestDefaultDataDirFallsBackToLegacyRule(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, e dataDirEnv)
		want  string
	}{
		{
			name:  "no pointer",
			setup: func(t *testing.T, e dataDirEnv) {},
			want:  "imagecaster-server",
		},
		{
			name:  "no pointer with legacy dir",
			setup: func(t *testing.T, e dataDirEnv) { mkdir(t, e.home, "sd-studio-server") },
			want:  "sd-studio-server",
		},
		{
			name:  "empty pointer",
			setup: func(t *testing.T, e dataDirEnv) { e.writePointer(t, "") },
			want:  "imagecaster-server",
		},
		{
			name:  "whitespace pointer",
			setup: func(t *testing.T, e dataDirEnv) { e.writePointer(t, "  \n\t\n") },
			want:  "imagecaster-server",
		},
		{
			name: "empty pointer with legacy dir",
			setup: func(t *testing.T, e dataDirEnv) {
				mkdir(t, e.home, "sd-studio-server")
				e.writePointer(t, "")
			},
			want: "sd-studio-server",
		},
		{
			name:  "pointer to missing dir",
			setup: func(t *testing.T, e dataDirEnv) { e.writePointer(t, filepath.Join(e.home, "gone")) },
			want:  "imagecaster-server",
		},
		{
			name: "pointer to missing dir with legacy dir",
			setup: func(t *testing.T, e dataDirEnv) {
				mkdir(t, e.home, "sd-studio-server")
				e.writePointer(t, filepath.Join(e.home, "gone"))
			},
			want: "sd-studio-server",
		},
		{
			name: "pointer to a file",
			setup: func(t *testing.T, e dataDirEnv) {
				f := filepath.Join(e.home, "afile")
				require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
				e.writePointer(t, f)
			},
			want: "imagecaster-server",
		},
		{
			name:  "garbage pointer",
			setup: func(t *testing.T, e dataDirEnv) { e.writePointer(t, "\x00\x01garbage\x00") },
			want:  "imagecaster-server",
		},
		{
			name: "pointer is a directory",
			setup: func(t *testing.T, e dataDirEnv) {
				require.NoError(t, os.MkdirAll(e.pointer(), 0o755))
			},
			want: "imagecaster-server",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := isolateDataDirEnv(t)
			tt.setup(t, e)

			assert.Equal(t, filepath.Join(e.home, tt.want), DefaultDataDir())
		})
	}
}

func TestSavedDataDirEmptyWithoutPointer(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, e dataDirEnv)
	}{
		{"no pointer", func(t *testing.T, e dataDirEnv) {}},
		{"empty pointer", func(t *testing.T, e dataDirEnv) { e.writePointer(t, "") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := isolateDataDirEnv(t)
			tt.setup(t, e)

			assert.Empty(t, SavedDataDir())
		})
	}
}

func TestDefaultDataDirPrefersSavedOverExistingDirs(t *testing.T) {
	e := isolateDataDirEnv(t)
	mkdir(t, e.home, "imagecaster-server")
	mkdir(t, e.home, "sd-studio-server")
	picked := t.TempDir()
	e.writePointer(t, picked)

	assert.Equal(t, picked, DefaultDataDir())
}

func mkdir(t *testing.T, parent, name string) {
	t.Helper()
	require.NoError(t, os.Mkdir(filepath.Join(parent, name), 0o755))
}
