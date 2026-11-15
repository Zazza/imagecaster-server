package process

import (
	"context"
	"log"
	"time"
)

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

func backoffDuration(restarts int) time.Duration {
	secs := 2 << uint(restarts)
	if secs > 32 {
		secs = 32
	}
	return time.Duration(secs) * time.Second
}
