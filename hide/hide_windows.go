//go:build windows

package hide

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func Window(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	Window(cmd)
	return cmd
}
