package process

import (
	"fmt"
	"imagecaster-server/probe"
	"log"
	"os/exec"
	"time"
)

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

func (pm *ProcessManager) stillIn(mp *ManagedProcess, cmd *exec.Cmd, status string) bool {
	pm.Mu.RLock()
	defer pm.Mu.RUnlock()
	return mp.Cmd == cmd && mp.Status == status
}

func probeHealth(url string) bool {
	_, err := probe.ProbeTimeout(url, 2*time.Second)
	return err == nil
}
