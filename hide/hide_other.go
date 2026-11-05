//go:build !windows

package hide

import "os/exec"

func Window(cmd *exec.Cmd) {}

func Command(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
