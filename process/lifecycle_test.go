//go:build !windows

package process

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"imagecaster-server/config"
)

func newFastPM(t *testing.T, procs map[string]config.ProcessConfig) *ProcessManager {
	t.Helper()
	pm := NewProcessManager(&config.Config{Processes: procs}, nil, t.TempDir())
	pm.watchInterval = 5 * time.Millisecond
	pm.restartBackoff = func(int) time.Duration { return 0 }
	pm.minUptime = 0
	pm.healthAttempts = 3
	pm.healthInterval = 5 * time.Millisecond
	pm.unhealthyInterval = 5 * time.Millisecond
	t.Cleanup(pm.Close)
	return pm
}

func snapshot(pm *ProcessManager, name string) ManagedProcess {
	pm.Mu.RLock()
	defer pm.Mu.RUnlock()
	mp := *pm.processes[name]
	if mp.Cmd != nil {
		args := slices.Clone(mp.Cmd.Args)
		mp.Config.Args = args[1:]
	}
	return mp
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func holds(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("%s no longer holds", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func pidAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func statusIs(pm *ProcessManager, name, want string) func() bool {
	return func() bool { return snapshot(pm, name).Status == want }
}

func sleeper(restart bool) config.ProcessConfig {
	return config.ProcessConfig{Name: "svc", Binary: "/bin/sleep", Args: []string{"30"}, Restart: restart, MaxRestart: 5}
}

func TestStopEndsStoppedAndWatchDoesNotRestart(t *testing.T) {
	pm := newFastPM(t, map[string]config.ProcessConfig{"svc": sleeper(true)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.Watch(ctx)

	if err := pm.Start("svc"); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := snapshot(pm, "svc").PID

	if err := pm.Stop("svc"); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if pidAlive(pid) {
		t.Fatalf("pid %d still alive after Stop", pid)
	}
	holds(t, "stopped status without restarts", 200*time.Millisecond, func() bool {
		mp := snapshot(pm, "svc")
		return mp.Status == StatusStopped && mp.Restarts == 0 && mp.PID == pid
	})
}

func TestStopUnknownAndIdle(t *testing.T) {
	pm := newFastPM(t, map[string]config.ProcessConfig{"svc": sleeper(false)})
	tests := []struct {
		name    string
		process string
		wantErr bool
	}{
		{"unknown process", "nope", true},
		{"never started", "svc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pm.Stop(tt.process)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Stop(%q) err = %v, wantErr %v", tt.process, err, tt.wantErr)
			}
		})
	}
	if st := snapshot(pm, "svc").Status; st != StatusStopped {
		t.Fatalf("idle process status = %q after Stop, want stopped", st)
	}
}

func TestRestartReplacesProcess(t *testing.T) {
	pm := newFastPM(t, map[string]config.ProcessConfig{"svc": sleeper(true)})
	if err := pm.Start("svc"); err != nil {
		t.Fatalf("start: %v", err)
	}
	pm.Mu.Lock()
	pm.processes["svc"].Restarts = 3
	pm.Mu.Unlock()
	oldPID := snapshot(pm, "svc").PID

	if err := pm.Restart("svc"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	mp := snapshot(pm, "svc")
	if mp.PID == oldPID || !pidAlive(mp.PID) {
		t.Fatalf("pid after restart = %d (old %d), want a new live process", mp.PID, oldPID)
	}
	if pidAlive(oldPID) {
		t.Fatalf("old pid %d still alive after Restart", oldPID)
	}
	if mp.Restarts != 0 {
		t.Fatalf("Restarts = %d after manual restart, want 0", mp.Restarts)
	}
	eventually(t, "running after restart", statusIs(pm, "svc", StatusRunning))
	holds(t, "old instance exit does not mark the new one crashed", 100*time.Millisecond, statusIs(pm, "svc", StatusRunning))

	if err := pm.Restart("nope"); err == nil {
		t.Fatal("Restart of an unknown process returned nil error")
	}
}

func TestCloseStopsProcessesAndRefusesStart(t *testing.T) {
	pm := newFastPM(t, map[string]config.ProcessConfig{"svc": sleeper(true)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.Watch(ctx)
	if err := pm.Start("svc"); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := snapshot(pm, "svc").PID

	pm.Close()

	if pidAlive(pid) {
		t.Fatalf("pid %d still alive after Close", pid)
	}
	holds(t, "stopped after Close", 100*time.Millisecond, statusIs(pm, "svc", StatusStopped))
	if err := pm.Start("svc"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close err = %v, want ErrClosed", err)
	}
}

func TestUpdateProcessConfigAppliesOnNextStart(t *testing.T) {
	pm := newFastPM(t, map[string]config.ProcessConfig{"svc": sleeper(true)})

	pm.UpdateProcessConfig("svc", config.ProcessConfig{Binary: "/bin/sleep", Args: []string{"31"}})
	pm.UpdateProcessConfig("nope", config.ProcessConfig{Binary: "/bin/true"})

	if err := pm.Start("svc"); err != nil {
		t.Fatalf("start: %v", err)
	}
	mp := snapshot(pm, "svc")
	if !slices.Equal(mp.Config.Args, []string{"31"}) {
		t.Fatalf("started with args %v, want [31]", mp.Config.Args)
	}
	if !mp.Config.Restart || mp.Config.MaxRestart != 5 {
		t.Fatalf("restart policy lost by config update: restart=%v max=%d", mp.Config.Restart, mp.Config.MaxRestart)
	}
	if _, ok := pm.Get("nope"); ok {
		t.Fatal("UpdateProcessConfig created an unknown process")
	}
}

func TestWatchRestartPolicy(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.ProcessConfig
		status       string
		wantRestarts int
	}{
		{"crashed with restart is restarted", sleeper(true), StatusCrashed, 1},
		{"crashed without restart stays", sleeper(false), StatusCrashed, 0},
		{"stopped is not restarted", sleeper(true), StatusStopped, 0},
		{"restart limit reached", config.ProcessConfig{Name: "svc", Binary: "/bin/sleep", Args: []string{"30"}, Restart: true, MaxRestart: 0}, StatusCrashed, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := newFastPM(t, map[string]config.ProcessConfig{"svc": tt.cfg})
			pm.Mu.Lock()
			pm.processes["svc"].Status = tt.status
			pm.Mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go pm.Watch(ctx)

			if tt.wantRestarts > 0 {
				eventually(t, "restarted process alive", func() bool {
					mp := snapshot(pm, "svc")
					return IsAlive(mp.Status) && pidAlive(mp.PID)
				})
			}
			holds(t, "restart count", 100*time.Millisecond, func() bool {
				return snapshot(pm, "svc").Restarts == tt.wantRestarts
			})
			if tt.wantRestarts == 0 && snapshot(pm, "svc").Status != tt.status {
				t.Fatalf("status changed to %q, want %q", snapshot(pm, "svc").Status, tt.status)
			}
		})
	}
}

func TestWatchRestartsRealCrashUpToLimit(t *testing.T) {
	pm := newFastPM(t, map[string]config.ProcessConfig{
		"svc": {Name: "svc", Binary: "/bin/false", Restart: true, MaxRestart: 2},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.Watch(ctx)
	if err := pm.Start("svc"); err != nil {
		t.Fatalf("start: %v", err)
	}

	eventually(t, "restart limit reached", func() bool {
		mp := snapshot(pm, "svc")
		return mp.Restarts == 2 && mp.Status == StatusCrashed
	})
	holds(t, "no restarts beyond the limit", 100*time.Millisecond, func() bool {
		return snapshot(pm, "svc").Restarts == 2
	})
}

func TestWatchSkipsRestartWhenNoLongerCrashed(t *testing.T) {
	pm := newFastPM(t, map[string]config.ProcessConfig{"svc": sleeper(true)})
	pm.restartBackoff = func(int) time.Duration { return 50 * time.Millisecond }
	pm.Mu.Lock()
	pm.processes["svc"].Status = StatusCrashed
	pm.Mu.Unlock()

	pm.scheduleRestarts()
	pm.Mu.Lock()
	pm.processes["svc"].Status = StatusRunning
	pm.Mu.Unlock()

	holds(t, "no restart after recovery", 100*time.Millisecond, func() bool {
		mp := snapshot(pm, "svc")
		return mp.Restarts == 0 && mp.PID == 0
	})
}

func TestHealthWaitOutcome(t *testing.T) {
	tests := []struct {
		name   string
		binary string
		args   []string
		health func(calls int64) int
		want   string
	}{
		{"health answers after start", "/bin/sleep", []string{"30"}, func(c int64) int {
			if c < 3 {
				return http.StatusServiceUnavailable
			}
			return http.StatusOK
		}, StatusRunning},
		{"health never answers", "/bin/sleep", []string{"30"}, func(int64) int { return http.StatusInternalServerError }, StatusUnhealthy},
		{"process dies before healthy", "/bin/false", nil, func(int64) int { return http.StatusInternalServerError }, StatusCrashed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.health(calls.Add(1)))
			}))
			t.Cleanup(srv.Close)
			pm := newFastPM(t, map[string]config.ProcessConfig{
				"svc": {Name: "svc", Binary: tt.binary, Args: tt.args, HealthURL: srv.URL},
			})
			if err := pm.Start("svc"); err != nil {
				t.Fatalf("start: %v", err)
			}

			eventually(t, "status "+tt.want, statusIs(pm, "svc", tt.want))
			holds(t, "final status "+tt.want, 100*time.Millisecond, statusIs(pm, "svc", tt.want))
		})
	}
}

func TestStopUnhealthyProcess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	pm := newFastPM(t, map[string]config.ProcessConfig{
		"svc": {Name: "svc", Binary: "/bin/sleep", Args: []string{"30"}, HealthURL: srv.URL},
	})
	if err := pm.Start("svc"); err != nil {
		t.Fatalf("start: %v", err)
	}
	eventually(t, "unhealthy", statusIs(pm, "svc", StatusUnhealthy))
	pid := snapshot(pm, "svc").PID

	if err := pm.Start("svc"); err == nil {
		t.Fatal("Start launched a second instance over a live unhealthy one")
	}
	if err := pm.Stop("svc"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if pidAlive(pid) {
		t.Fatalf("unhealthy pid %d still alive after Stop", pid)
	}
	holds(t, "stopped", 50*time.Millisecond, statusIs(pm, "svc", StatusStopped))
}
