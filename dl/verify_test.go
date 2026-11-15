package dl

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifySHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(p, []byte("payload"), 0o644))
	sum := sha256.Sum256([]byte("payload"))
	good := hex.EncodeToString(sum[:])

	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"match", good, false},
		{"match upper", strings.ToUpper(good), false},
		{"mismatch", strings.Repeat("0", 64), true},
		{"empty skips check", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyFile(p, tt.want)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"zero", 0, "0 B"},
		{"bytes", 512, "512 B"},
		{"kilobytes", 1024, "1.0 KB"},
		{"megabytes", 1048576, "1.0 MB"},
		{"gigabytes", 1073741824, "1.0 GB"},
		{"partial_KB", 1536, "1.5 KB"},
		{"partial_MB", 2621440, "2.5 MB"},
		{"partial_GB", 1610612736, "1.5 GB"},
		{"large_bytes", 999, "999 B"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatBytes(tt.bytes)
			if got != tt.want {
				t.Errorf("FormatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}
