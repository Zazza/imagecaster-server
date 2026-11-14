//go:build windows

package process

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"imagecaster-server/hide"
)

func SetPlatformProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	hide.Window(cmd)
}

func PlatformKill(pid int, cancel context.CancelFunc) error {
	if cancel != nil {
		cancel()
	}

	if pid > 0 {
		killProcessTree(pid)
		time.Sleep(1500 * time.Millisecond)
	}
	return nil
}

func killProcessTree(pid int) {
	run(exec.Command("taskkill", "/PID", itoa(pid), "/T", "/F"))

	wmic := exec.Command("wmic", "process", "where",
		"ParentProcessId="+itoa(pid),
		"get", "ProcessId", "/format:csv")
	hide.Window(wmic)
	out, _ := wmic.Output()
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Node,") {
			continue
		}
		fields := strings.Split(line, ",")
		childPID := strings.TrimSpace(fields[len(fields)-1])
		if childPID != "" && childPID != itoa(pid) {
			run(exec.Command("taskkill", "/PID", childPID, "/T", "/F"))
		}
	}
}

func run(cmd *exec.Cmd) {
	hide.Window(cmd)
	_ = cmd.Run()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
