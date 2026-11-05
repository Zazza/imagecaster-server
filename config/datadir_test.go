package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDataDirIn(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, home string)
		want  string
	}{
		{
			name:  "neither exists",
			setup: func(t *testing.T, home string) {},
			want:  "imagecaster-server",
		},
		{
			name: "only legacy dir",
			setup: func(t *testing.T, home string) {
				require.NoError(t, os.Mkdir(filepath.Join(home, "sd-studio-server"), 0o755))
			},
			want: "sd-studio-server",
		},
		{
			name: "only new dir",
			setup: func(t *testing.T, home string) {
				require.NoError(t, os.Mkdir(filepath.Join(home, "imagecaster-server"), 0o755))
			},
			want: "imagecaster-server",
		},
		{
			name: "both exist",
			setup: func(t *testing.T, home string) {
				require.NoError(t, os.Mkdir(filepath.Join(home, "sd-studio-server"), 0o755))
				require.NoError(t, os.Mkdir(filepath.Join(home, "imagecaster-server"), 0o755))
			},
			want: "imagecaster-server",
		},
		{
			name: "legacy is a file",
			setup: func(t *testing.T, home string) {
				require.NoError(t, os.WriteFile(filepath.Join(home, "sd-studio-server"), []byte("x"), 0o644))
			},
			want: "imagecaster-server",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			tt.setup(t, home)
			before, err := os.ReadDir(home)
			require.NoError(t, err)

			got := dataDirIn(home)

			assert.Equal(t, filepath.Join(home, tt.want), got)
			after, err := os.ReadDir(home)
			require.NoError(t, err)
			assert.Len(t, after, len(before))
		})
	}
}
