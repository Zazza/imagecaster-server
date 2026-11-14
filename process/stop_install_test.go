//go:build !windows

package process

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"imagecaster-server/config"
)

type blockingInstaller struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingInstaller) EnsureInstalled(key, binary string) error {
	close(b.entered)
	<-b.release
	return nil
}

func TestStopDuringInstallCancelsStart(t *testing.T) {
	stranger := exec.Command("/bin/sleep", "30")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := stranger.Start(); err != nil {
		t.Fatalf("start stranger: %v", err)
	}
	t.Cleanup(func() {
		if err := stranger.Process.Kill(); err != nil {
			t.Logf("kill stranger: %v", err)
		}
		if err := stranger.Wait(); err != nil {
			t.Logf("wait stranger: %v", err)
		}
	})

	pm := newFastPM(t, map[string]config.ProcessConfig{"svc": sleeper(true)})
	inst := &blockingInstaller{entered: make(chan struct{}), release: make(chan struct{})}
	pm.installer = inst
	pm.Mu.Lock()
	pm.processes["svc"].PID = stranger.Process.Pid
	pm.processes["svc"].CancelFunc = func() {}
	pm.processes["svc"].Status = StatusCrashed
	pm.Mu.Unlock()

	startErr := make(chan error, 1)
	go func() { startErr <- pm.Start("svc") }()
	<-inst.entered
	if st := snapshot(pm, "svc").Status; st != StatusStarting {
		t.Fatalf("status during install = %q, want starting", st)
	}

	if err := pm.Stop("svc"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !pidAlive(stranger.Process.Pid) {
		t.Fatal("Stop during install signalled the stale PID of a previous run")
	}
	close(inst.release)

	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("Start returned nil after Stop during install")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after install finished")
	}
	mp := snapshot(pm, "svc")
	if mp.Status != StatusStopped || mp.PID != 0 || mp.Cmd != nil {
		t.Fatalf("after cancelled start: status=%q pid=%d cmd=%v, want stopped with no process", mp.Status, mp.PID, mp.Cmd != nil)
	}
}

func TestUnhealthyRecoversWhenHealthAnswersLate(t *testing.T) {
	var answering atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !answering.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	pm := newFastPM(t, map[string]config.ProcessConfig{
		"svc": {Name: "svc", Binary: "/bin/sleep", Args: []string{"30"}, HealthURL: srv.URL},
	})
	if err := pm.Start("svc"); err != nil {
		t.Fatalf("start: %v", err)
	}
	eventually(t, "unhealthy", statusIs(pm, "svc", StatusUnhealthy))
	holds(t, "unhealthy while health is silent", 50*time.Millisecond, statusIs(pm, "svc", StatusUnhealthy))
	pid := snapshot(pm, "svc").PID

	answering.Store(true)

	eventually(t, "running after health recovered", statusIs(pm, "svc", StatusRunning))
	if got := snapshot(pm, "svc").PID; got != pid {
		t.Fatalf("pid changed %d -> %d, recovery must keep the same instance", pid, got)
	}
}

func TestUnhealthyRecheckStopsAfterStop(t *testing.T) {
	var answering atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !answering.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	pm := newFastPM(t, map[string]config.ProcessConfig{
		"svc": {Name: "svc", Binary: "/bin/sleep", Args: []string{"30"}, HealthURL: srv.URL},
	})
	if err := pm.Start("svc"); err != nil {
		t.Fatalf("start: %v", err)
	}
	eventually(t, "unhealthy", statusIs(pm, "svc", StatusUnhealthy))
	if err := pm.Stop("svc"); err != nil {
		t.Fatalf("stop: %v", err)
	}

	answering.Store(true)

	holds(t, "stopped despite health answering", 100*time.Millisecond, statusIs(pm, "svc", StatusStopped))
}
