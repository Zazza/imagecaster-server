package process

import (
	"errors"
	"fmt"
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

func (pm *ProcessManager) Get(name string) (*ManagedProcess, bool) {
	pm.Mu.RLock()
	defer pm.Mu.RUnlock()
	mp, ok := pm.processes[name]
	return mp, ok
}

var (
	ErrClosed         = errors.New("process manager is shut down")
	ErrNotFound       = errors.New("not found")
	ErrAlreadyRunning = errors.New("already running")
)

type alreadyError struct {
	name, status string
}

func (e *alreadyError) Error() string {
	return fmt.Sprintf("process %q is already %s", e.name, e.status)
}

func (e *alreadyError) Is(target error) bool {
	return target == ErrAlreadyRunning
}

func (pm *ProcessManager) notifyChange() {
	if pm.OnChange != nil {
		pm.OnChange()
	}
}
