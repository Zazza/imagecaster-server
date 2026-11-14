package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"imagecaster-server/config"
)

type Installer interface {
	EnsureInstalled(key, binary string) error
}

type ProcessManager struct {
	Mu                sync.RWMutex
	processes         map[string]*ManagedProcess
	installer         Installer
	dataDir           string
	closed            bool
	OnChange          func()
	watchInterval     time.Duration
	restartBackoff    func(restarts int) time.Duration
	minUptime         time.Duration
	healthAttempts    int
	healthInterval    time.Duration
	unhealthyInterval time.Duration
}

const (
	StatusStopped   = "stopped"
	StatusStarting  = "starting"
	StatusRunning   = "running"
	StatusUnhealthy = "unhealthy"
	StatusCrashed   = "crashed"
)

func IsAlive(status string) bool {
	return status == StatusRunning || status == StatusStarting || status == StatusUnhealthy
}

func NewProcessManager(cfg *config.Config, inst Installer, dataDir string) *ProcessManager {
	pm := &ProcessManager{
		processes:         make(map[string]*ManagedProcess),
		installer:         inst,
		dataDir:           dataDir,
		watchInterval:     2 * time.Second,
		restartBackoff:    backoffDuration,
		minUptime:         5 * time.Second,
		healthAttempts:    120,
		healthInterval:    2 * time.Second,
		unhealthyInterval: 10 * time.Second,
	}
	for key, pc := range cfg.Processes {
		pm.processes[key] = &ManagedProcess{
			Config: pc,
			Status: "stopped",
			LogBuf: NewRingBuffer(RingBufferSize),
		}
	}
	return pm
}

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
		return fmt.Errorf("process %q not found", name)
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
		return fmt.Errorf("process %q not found", name)
	}
	if status := mp.Status; IsAlive(status) {
		pm.Mu.Unlock()
		return fmt.Errorf("process %q is already %s", name, status)
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

	if pc.HealthURL != "" {
		hctx, hcancel := context.WithTimeout(context.Background(), 2*time.Second)
		hreq, _ := http.NewRequestWithContext(hctx, http.MethodGet, pc.HealthURL, nil)
		hresp, herr := http.DefaultClient.Do(hreq)
		hcancel()
		if herr == nil {
			hresp.Body.Close()
			if hresp.StatusCode == http.StatusOK {
				pm.Mu.Lock()
				if mp.stopRequested {
					pm.Mu.Unlock()
					return pm.startAborted(name)
				}
				mp.Status = StatusRunning
				mp.StartedAt = time.Now()
				mp.LogBuf.Write("already running (health check passed, skipped start)")
				pm.processes[name] = mp
				pm.Mu.Unlock()
				log.Printf("[%s] already running (health check passed)", name)
				pm.notifyChange()
				return nil
			}
		}
	}

	if pm.installer != nil && !installFailed {
		if err := pm.installer.EnsureInstalled(name, pc.Binary); err != nil {
			pm.Mu.Lock()
			if !mp.stopRequested {
				mp.Status = StatusCrashed
			}
			mp.InstallFailed = true
			mp.LogBuf.Write(fmt.Sprintf("auto-install failed: %v", err))
			pm.processes[name] = mp
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
		pm.processes[name] = mp
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
		pm.processes[name] = mp
		pm.Mu.Unlock()
		cancel()
		return fmt.Errorf("start %q: %w (binary=%s)", name, err, pc.Binary)
	}
	mp.PID = cmd.Process.Pid
	mp.Config.Binary = pc.Binary
	mp.StartedAt = time.Now()
	mp.Status = StatusStarting
	mp.Managed = true
	pm.processes[name] = mp
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

func (pm *ProcessManager) awaitHealthy(name string, mp *ManagedProcess, cmd *exec.Cmd, healthURL string) {
	healthy := healthURL == ""
	if !healthy {
		log.Printf("[%s] waiting for healthy response from %s ...", name, healthURL)
	}
	for i := 0; !healthy && i < pm.healthAttempts; i++ {
		if !pm.stillIn(mp, cmd, StatusStarting) {
			return
		}
		healthy = probeHealth(healthURL)
		if !healthy {
			time.Sleep(pm.healthInterval)
		}
	}

	pm.Mu.Lock()
	if mp.Cmd != cmd || mp.Status != StatusStarting {
		pm.Mu.Unlock()
		return
	}
	if healthy {
		mp.Status = StatusRunning
	} else {
		mp.Status = StatusUnhealthy
		mp.LogBuf.Write(fmt.Sprintf("no healthy response from %s after %d attempts", healthURL, pm.healthAttempts))
	}
	status := mp.Status
	pm.Mu.Unlock()
	log.Printf("[%s] %s", name, status)
	pm.notifyChange()
	if !healthy {
		pm.recheckUnhealthy(name, mp, cmd, healthURL)
	}
}

func (pm *ProcessManager) recheckUnhealthy(name string, mp *ManagedProcess, cmd *exec.Cmd, healthURL string) {
	for {
		time.Sleep(pm.unhealthyInterval)
		if !pm.stillIn(mp, cmd, StatusUnhealthy) {
			return
		}
		if !probeHealth(healthURL) {
			continue
		}
		pm.Mu.Lock()
		if mp.Cmd != cmd || mp.Status != StatusUnhealthy {
			pm.Mu.Unlock()
			return
		}
		mp.Status = StatusRunning
		mp.LogBuf.Write(fmt.Sprintf("healthy response from %s after being unhealthy", healthURL))
		pm.Mu.Unlock()
		log.Printf("[%s] running (health recovered)", name)
		pm.notifyChange()
		return
	}
}

func (pm *ProcessManager) startAborted(name string) error {
	log.Printf("[%s] start cancelled: stop requested", name)
	pm.notifyChange()
	return fmt.Errorf("process %q: start cancelled by stop", name)
}

func (pm *ProcessManager) openLogFile(name string, mp *ManagedProcess) {
	if mp.LogFile != nil {
		mp.LogFile.Close()
		mp.LogFile = nil
	}
	logsDir := filepath.Join(pm.dataDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		log.Printf("[%s] log dir: %v", name, err)
		return
	}
	f, err := os.OpenFile(filepath.Join(logsDir, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[%s] log file: %v", name, err)
		return
	}
	mp.LogFile = f
}

func (pm *ProcessManager) stillIn(mp *ManagedProcess, cmd *exec.Cmd, status string) bool {
	pm.Mu.RLock()
	defer pm.Mu.RUnlock()
	return mp.Cmd == cmd && mp.Status == status
}

func probeHealth(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Printf("health request %s: %v", url, err)
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func pipeLogs(r io.Reader, buf *RingBuffer, logFile *os.File, name string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		buf.Write(line)
		log.Printf("[%s] %s", name, line)
		if logFile != nil {
			logFile.WriteString(time.Now().Format("2006-01-02 15:04:05") + " " + line + "\n")
		}
	}
}

func (pm *ProcessManager) Stop(name string) error {
	pm.Mu.Lock()
	mp, ok := pm.processes[name]
	if !ok {
		pm.Mu.Unlock()
		return fmt.Errorf("process %q not found", name)
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
	pm.processes[name] = mp
	pm.Mu.Unlock()

	return pm.Start(name)
}

func (pm *ProcessManager) Status() map[string]ProcessStatus {
	pm.Mu.RLock()
	defer pm.Mu.RUnlock()

	result := make(map[string]ProcessStatus, len(pm.processes))
	for name, mp := range pm.processes {
		ps := ProcessStatus{
			Name:      mp.Config.Name,
			Status:    mp.Status,
			PID:       mp.PID,
			StartedAt: mp.StartedAt,
			Restarts:  mp.Restarts,
			Category:  mp.Config.Category,
		}
		if !mp.StartedAt.IsZero() && mp.Status == StatusRunning {
			ps.Uptime = time.Since(mp.StartedAt).Truncate(time.Second).String()
		}
		result[name] = ps
	}
	return result
}

func (pm *ProcessManager) Logs(name string, lines int) ([]string, error) {
	pm.Mu.RLock()
	mp, ok := pm.processes[name]
	pm.Mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("process %q not found", name)
	}
	return mp.LogBuf.Lines(lines), nil
}

func (pm *ProcessManager) Get(name string) (*ManagedProcess, bool) {
	pm.Mu.RLock()
	defer pm.Mu.RUnlock()
	mp, ok := pm.processes[name]
	return mp, ok
}

func (pm *ProcessManager) Watch(ctx context.Context) {
	ticker := time.NewTicker(pm.watchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pm.scheduleRestarts()
		}
	}
}

func (pm *ProcessManager) scheduleRestarts() {
	pm.Mu.Lock()
	defer pm.Mu.Unlock()
	for name, mp := range pm.processes {
		if mp.Status != StatusCrashed || !mp.Config.Restart || mp.Restarts >= mp.Config.MaxRestart || mp.restartPending {
			continue
		}
		if !mp.StartedAt.IsZero() && time.Since(mp.StartedAt) < pm.minUptime {
			log.Printf("[%s] crashed too fast (%v), skipping restart", name, time.Since(mp.StartedAt))
			continue
		}
		mp.restartPending = true
		go pm.restartAfter(name, mp, pm.restartBackoff(mp.Restarts))
	}
}

func (pm *ProcessManager) restartAfter(name string, mp *ManagedProcess, delay time.Duration) {
	time.Sleep(delay)
	pm.Mu.Lock()
	mp.restartPending = false
	if mp.Status != StatusCrashed {
		pm.Mu.Unlock()
		return
	}
	mp.Restarts++
	pm.Mu.Unlock()
	if err := pm.start(name); err != nil {
		log.Printf("[%s] restart failed: %v", name, err)
	}
	pm.notifyChange()
}

var ErrClosed = errors.New("process manager is shut down")

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
	pm.processes[name] = mp
}

func backoffDuration(restarts int) time.Duration {
	secs := 2 << uint(restarts)
	if secs > 32 {
		secs = 32
	}
	return time.Duration(secs) * time.Second
}

func envSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

func (pm *ProcessManager) notifyChange() {
	if pm.OnChange != nil {
		pm.OnChange()
	}
}

func (pm *ProcessManager) toolPathDirs() []string {
	var dirs []string
	binDir := filepath.Join(pm.dataDir, "bin")
	if _, err := os.Stat(binDir); err == nil {
		dirs = append(dirs, binDir)
	}
	return dirs
}

func FindBinary(dataDir, name string) string {
	if runtime.GOOS == "windows" {
		for _, dir := range []string{
			filepath.Join(dataDir, "bin"),
		} {
			candidate := filepath.Join(dir, name+".exe")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	} else {
		for _, dir := range []string{
			filepath.Join(dataDir, "bin"),
		} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	path, err := exec.LookPath(name)
	if err == nil {
		return path
	}
	return ""
}
