//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"imagecaster-server/hide"
)

var stopGrace = 10 * time.Second

func SetPlatformProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	hide.Window(cmd)
}

func PlatformKill(pid int, cancel context.CancelFunc) error {
	if pid <= 0 {
		if cancel != nil {
			cancel()
		}
		return nil
	}
	h, openErr := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	killErr := killProcessTree(pid)
	if cancel != nil {
		cancel()
	}
	if openErr != nil {
		return nil
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(stopGrace/time.Millisecond))
	if err != nil {
		return fmt.Errorf("wait for pid %d: %w", pid, err)
	}
	if ev == uint32(windows.WAIT_TIMEOUT) {
		return errors.Join(fmt.Errorf("pid %d still running after %s", pid, stopGrace), killErr)
	}
	return nil
}

func killProcessTree(pid int) error {
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	hide.Window(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("taskkill %d: %w: %s", pid, err, out)
	}
	return nil
}
