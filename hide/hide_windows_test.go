//go:build windows

package hide

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandHidesConsoleWindow(t *testing.T) {
	cmd := Command("cmd.exe", "/c", "exit", "0")
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.HideWindow)
	assert.NotZero(t, cmd.SysProcAttr.CreationFlags&createNoWindow)
	assert.Equal(t, []string{"cmd.exe", "/c", "exit", "0"}, cmd.Args)
}

func TestWindowKeepsExistingCreationFlags(t *testing.T) {
	const newProcessGroup = 0x00000200
	tests := []struct {
		name string
		attr *syscall.SysProcAttr
		want uint32
	}{
		{name: "no attributes", attr: nil, want: createNoWindow},
		{name: "existing flag kept", attr: &syscall.SysProcAttr{CreationFlags: newProcessGroup}, want: createNoWindow | newProcessGroup},
		{name: "already hidden", attr: &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}, want: createNoWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("cmd.exe")
			cmd.SysProcAttr = tt.attr
			Window(cmd)
			require.NotNil(t, cmd.SysProcAttr)
			assert.True(t, cmd.SysProcAttr.HideWindow)
			assert.Equal(t, tt.want, cmd.SysProcAttr.CreationFlags)
		})
	}
}
