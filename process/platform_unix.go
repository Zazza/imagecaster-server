//go:build !windows

package process

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

func SetPlatformProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func PlatformKill(pid int, cancel context.CancelFunc) error {
	if cancel != nil {
		cancel()
	}

	if pid > 0 {
		syscall.Kill(-pid, syscall.SIGTERM)

		done := make(chan struct{})
		go func() {
			for {
				if syscall.Kill(-pid, 0) != nil {
					close(done)
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
	return nil
}
