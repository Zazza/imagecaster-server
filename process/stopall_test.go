//go:build !windows

package process

import (
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"imagecaster-server/config"
)

func TestStopAllStopsOwnChildWithSystemBinary(t *testing.T) {
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{
		"ollama": {Name: "ollama", Binary: "/bin/sleep", Args: []string{"30"}},
	}}
	pm := NewProcessManager(cfg, nil, t.TempDir())
	if err := pm.Start("ollama"); err != nil {
		t.Fatalf("start: %v", err)
	}
	pm.Mu.RLock()
	pid := pm.processes["ollama"].PID
	pm.Mu.RUnlock()

	pm.StopAll()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("child %d with a binary outside the data dir still runs after StopAll", pid)
}

func TestStartOnAlreadyHealthyServiceDropsOldPID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{
		"ollama": {Name: "ollama", Binary: "/bin/sleep", HealthURL: srv.URL},
	}}
	pm := NewProcessManager(cfg, nil, t.TempDir())
	pm.Mu.Lock()
	pm.processes["ollama"].PID = 999999
	pm.processes["ollama"].CancelFunc = func() {}
	pm.processes["ollama"].Status = "crashed"
	pm.Mu.Unlock()

	if err := pm.Start("ollama"); err != nil {
		t.Fatalf("start: %v", err)
	}

	pm.Mu.RLock()
	defer pm.Mu.RUnlock()
	if mp := pm.processes["ollama"]; mp.Status != "running" || pm.isOurProcess(mp) {
		t.Fatalf("status=%s pid=%d: a service we did not start must not be ours", mp.Status, mp.PID)
	}
}
