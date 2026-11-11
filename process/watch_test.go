package process

import (
	"context"
	"testing"
	"time"

	"imagecaster-server/config"
)

func TestWatchSkipsRestartWhenNoLongerCrashed(t *testing.T) {
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{
		"sd": {Name: "sd", Binary: "/nonexistent/sd", Restart: true, MaxRestart: 5},
	}}
	pm := NewProcessManager(cfg, nil, t.TempDir())
	pm.Mu.Lock()
	pm.processes["sd"].Status = "crashed"
	pm.Mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.Watch(ctx)
	time.Sleep(2500 * time.Millisecond)
	pm.Mu.Lock()
	pm.processes["sd"].Status = "running"
	pm.Mu.Unlock()
	time.Sleep(2500 * time.Millisecond)

	pm.Mu.RLock()
	defer pm.Mu.RUnlock()
	if r := pm.processes["sd"].Restarts; r != 0 {
		t.Fatalf("Restarts = %d after the process recovered before the watchdog fired, want 0", r)
	}
}
