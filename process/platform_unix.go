//go:build !windows

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var stopGrace = 10 * time.Second

func SetPlatformProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = stopGrace
}

func PlatformKill(pid int, cancel context.CancelFunc) error {
	if pid <= 0 {
		if cancel != nil {
			cancel()
		}
		return nil
	}
	if cancel != nil {
		cancel()
	} else if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
