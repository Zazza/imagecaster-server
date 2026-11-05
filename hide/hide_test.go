//go:build !windows

package hide

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandMatchesExecCommand(t *testing.T) {
	tests := []struct {
		name string
		bin  string
		args []string
	}{
		{"no args", "go", nil},
		{"with args", "go", []string{"version", "-m"}},
		{"missing binary", "imagecaster-no-such-binary", []string{"x"}},
		{"relative path", "./tool", []string{"a b", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := exec.Command(tt.bin, tt.args...)
			got := Command(tt.bin, tt.args...)
			require.NotNil(t, got)
			assert.Equal(t, want.Path, got.Path)
			assert.Equal(t, want.Args, got.Args)
			assert.Equal(t, want.Dir, got.Dir)
			assert.Equal(t, want.Env, got.Env)
			assert.Equal(t, want.SysProcAttr, got.SysProcAttr)
		})
	}
}
