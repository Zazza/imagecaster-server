package process

import (
	"context"
	"fmt"
	"imagecaster-server/config"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func (pm *ProcessManager) StartAll() {
	pm.Mu.RLock()
	defer pm.Mu.RUnlock()
	for name, mp := range pm.processes {
		if mp.Config.AutoStart {
			go pm.start(name)
		}
	}
}

func (pm *ProcessManager) Start(name string) error {
	pm.Mu.RLock()
	_, ok := pm.processes[name]
	pm.Mu.RUnlock()
	if !ok {
		return fmt.Errorf("process %q %w", name, ErrNotFound)
	}
	return pm.start(name)
}

func (pm *ProcessManager) start(name string) error {
	pm.Mu.Lock()
	if pm.closed {
		pm.Mu.Unlock()
		return fmt.Errorf("process %q: %w", name, ErrClosed)
	}
	mp, ok := pm.processes[name]
	if !ok {
		pm.Mu.Unlock()
		return fmt.Errorf("process %q %w", name, ErrNotFound)
	}
	if status := mp.Status; IsAlive(status) {
		pm.Mu.Unlock()
		return &alreadyError{name: name, status: status}
	}

	mp.Status = StatusStarting
	mp.stopRequested = false
	mp.Cmd = nil
	mp.PID = 0
	mp.CancelFunc = nil
	pc := mp.Config
	pc.Env = maps.Clone(pc.Env)
	installFailed := mp.InstallFailed
	pm.Mu.Unlock()

	if pc.HealthURL != "" && probeHealth(pc.HealthURL) {
		pm.Mu.Lock()
		if mp.stopRequested {
			pm.Mu.Unlock()
			return pm.startAborted(name)
		}
		mp.Status = StatusRunning
		mp.StartedAt = time.Now()
		mp.LogBuf.Write("already running (health check passed, skipped start)")
		pm.Mu.Unlock()
		log.Printf("[%s] already running (health check passed)", name)
		pm.notifyChange()
		return nil
	}

	if pm.installer != nil && !installFailed {
		if err := pm.installer.EnsureInstalled(name, pc.Binary); err != nil {
			pm.Mu.Lock()
			if !mp.stopRequested {
				mp.Status = StatusCrashed
			}
			mp.InstallFailed = true
			mp.LogBuf.Write(fmt.Sprintf("auto-install failed: %v", err))
			pm.Mu.Unlock()
			log.Printf("[%s] auto-install failed: %v", name, err)
			return fmt.Errorf("auto-install %q: %w", name, err)
		}
	}

	if pc.Binary != "" && !strings.ContainsRune(pc.Binary, '/') && !strings.ContainsRune(pc.Binary, os.PathSeparator) {
		resolved := FindBinary(pm.dataDir, pc.Binary)
		if resolved != "" {
			pc.Binary = resolved
		}
	}

	env := pc.Env
	if env == nil {
		env = make(map[string]string)
	}
	for _, dir := range pm.toolPathDirs() {
		existing := os.Getenv("PATH")
		if e, ok := env["PATH"]; ok {
			existing = e
		}
		env["PATH"] = dir + string(os.PathListSeparator) + existing
	}

	if runtime.GOOS == "windows" && pc.Binary != "" && !strings.HasSuffix(pc.Binary, ".exe") {
		exePath := pc.Binary + ".exe"
		if _, err := os.Stat(exePath); err == nil {
			pc.Binary = exePath
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, pc.Binary, pc.Args...)

	if pc.WorkDir != "" {
		cmd.Dir = pc.WorkDir
	}

	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), envSlice(env)...)
	}

	SetPlatformProcAttr(cmd)

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	mp.LogBuf.Write(fmt.Sprintf("--- process restarted at %s ---", time.Now().Format("2006-01-02 15:04:05")))

	pm.Mu.Lock()
	if pm.closed {
		mp.Status = StatusStopped
		pm.Mu.Unlock()
		cancel()
		return fmt.Errorf("process %q: %w", name, ErrClosed)
	}
	if mp.stopRequested || mp.Status != StatusStarting {
		pm.Mu.Unlock()
		cancel()
		return pm.startAborted(name)
	}
	if pm.installer != nil && pm.dataDir != "" {
		pm.openLogFile(name, mp)
	}
	logFile := mp.LogFile
	mp.Cmd = cmd
	mp.CancelFunc = cancel
	if err := cmd.Start(); err != nil {
		mp.Status = StatusCrashed
		mp.LogBuf.Write(fmt.Sprintf("failed to start: %v (binary=%s workdir=%s)", err, pc.Binary, pc.WorkDir))
		pm.Mu.Unlock()
		cancel()
		return fmt.Errorf("start %q: %w (binary=%s)", name, err, pc.Binary)
	}
	mp.PID = cmd.Process.Pid
	mp.Config.Binary = pc.Binary
	mp.StartedAt = time.Now()
	mp.Status = StatusStarting
	mp.Managed = true
	pm.Mu.Unlock()

	log.Printf("[%s] started (pid=%d binary=%s workdir=%s)", name, cmd.Process.Pid, pc.Binary, pc.WorkDir)
	pm.notifyChange()

	go pipeLogs(stdout, mp.LogBuf, logFile, name)
	go pipeLogs(stderr, mp.LogBuf, logFile, name)

	go pm.awaitHealthy(name, mp, cmd, pc.HealthURL)

	go func() {
		err := cmd.Wait()
		pm.Mu.Lock()
		if mp.Cmd != cmd {
			pm.Mu.Unlock()
			log.Printf("[%s] previous instance exited: %v", name, err)
			return
		}
		if mp.LogFile != nil {
			mp.LogFile.Close()
			mp.LogFile = nil
		}
		if mp.stopRequested {
			mp.Status = StatusStopped
		} else {
			mp.Status = StatusCrashed
		}
		if err != nil {
			mp.LogBuf.Write(fmt.Sprintf("process exited: %v", err))
		}
		status := mp.Status
		pm.Mu.Unlock()
		log.Printf("[%s] exited (%s): %v", name, status, err)
		pm.notifyChange()
	}()

	return nil
}

func (pm *ProcessManager) startAborted(name string) error {
	log.Printf("[%s] start cancelled: stop requested", name)
	pm.notifyChange()
	return fmt.Errorf("process %q: start cancelled by stop", name)
}

func (pm *ProcessManager) Stop(name string) error {
	pm.Mu.Lock()
	mp, ok := pm.processes[name]
	if !ok {
		pm.Mu.Unlock()
		return fmt.Errorf("process %q %w", name, ErrNotFound)
	}
	if !IsAlive(mp.Status) {
		pm.Mu.Unlock()
		return nil
	}
	pid, cancel, cmd := pm.requestStop(mp)
	pm.Mu.Unlock()

	err := PlatformKill(pid, cancel)
	pm.markStopped(mp, cmd)
	pm.notifyChange()
	return err
}

func (pm *ProcessManager) requestStop(mp *ManagedProcess) (int, context.CancelFunc, *exec.Cmd) {
	mp.stopRequested = true
	return mp.PID, mp.CancelFunc, mp.Cmd
}

func (pm *ProcessManager) markStopped(mp *ManagedProcess, cmd *exec.Cmd) {
	pm.Mu.Lock()
	defer pm.Mu.Unlock()
	if mp.Cmd == cmd {
		mp.Status = StatusStopped
	}
}

func (pm *ProcessManager) Restart(name string) error {
	if err := pm.Stop(name); err != nil {
		return err
	}

	pm.Mu.Lock()
	mp := pm.processes[name]
	mp.Restarts = 0
	pm.Mu.Unlock()

	return pm.Start(name)
}

func (pm *ProcessManager) Close() {
	pm.Mu.Lock()
	pm.closed = true
	pm.Mu.Unlock()
	pm.StopAll()
}

func (pm *ProcessManager) StopAll() {
	type target struct {
		name   string
		mp     *ManagedProcess
		pid    int
		cancel context.CancelFunc
		cmd    *exec.Cmd
	}
	var targets []target
	pm.Mu.Lock()
	for name, mp := range pm.processes {
		if !IsAlive(mp.Status) {
			continue
		}
		if !pm.isOurProcess(mp) {
			log.Printf("[%s] skipping stop — system-managed process", name)
			continue
		}
		pid, cancel, cmd := pm.requestStop(mp)
		targets = append(targets, target{name, mp, pid, cancel, cmd})
	}
	pm.Mu.Unlock()

	for _, t := range targets {
		if err := PlatformKill(t.pid, t.cancel); err != nil {
			log.Printf("[%s] stop: %v", t.name, err)
		}
		pm.markStopped(t.mp, t.cmd)
	}
	if len(targets) > 0 {
		pm.notifyChange()
	}
}

func (pm *ProcessManager) isOurProcess(mp *ManagedProcess) bool {
	return mp.PID != 0 && mp.CancelFunc != nil
}

func (pm *ProcessManager) UpdateProcessConfig(name string, pc config.ProcessConfig) {
	pm.Mu.Lock()
	defer pm.Mu.Unlock()
	mp, ok := pm.processes[name]
	if !ok {
		return
	}
	mp.Config.Binary = pc.Binary
	mp.Config.Args = pc.Args
	mp.Config.WorkDir = pc.WorkDir
	mp.Config.Env = pc.Env
}

func envSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

func (pm *ProcessManager) toolPathDirs() []string {
	var dirs []string
	binDir := filepath.Join(pm.dataDir, "bin")
	if _, err := os.Stat(binDir); err == nil {
		dirs = append(dirs, binDir)
	}
	return dirs
}
