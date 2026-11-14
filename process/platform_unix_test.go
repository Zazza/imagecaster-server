//go:build !windows

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

func TestStopSendsTermBeforeKill(t *testing.T) {
	old := stopGrace
	stopGrace = 500 * time.Millisecond
	t.Cleanup(func() { stopGrace = old })

	cases := []struct {
		name      string
		script    string
		wantMark  bool
		minStopIn time.Duration
	}{
		{name: "TERM handler runs", script: `trap 'echo bye > "$MARK"; exit 0' TERM; while :; do sleep 0.05; done`, wantMark: true},
		{name: "TERM ignored is killed after grace", script: `trap '' TERM; while :; do sleep 0.05; done`, minStopIn: 400 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mark := filepath.Join(t.TempDir(), "mark")
			t.Setenv("MARK", mark)
			pm := newFastPM(t, map[string]config.ProcessConfig{"svc": {Name: "svc", Binary: "/bin/sh", Args: []string{"-c", tc.script}}})
			require.NoError(t, pm.Start("svc"))
			pid := snapshot(pm, "svc").PID
			time.Sleep(100 * time.Millisecond)

			start := time.Now()
			require.NoError(t, pm.Stop("svc"))
			took := time.Since(start)

			eventually(t, "process exit", func() bool { return !pidAlive(pid) })
			_, err := os.Stat(mark)
			assert.Equal(t, tc.wantMark, err == nil, "TERM handler ran")
			assert.GreaterOrEqual(t, took, tc.minStopIn)
			assert.Less(t, took, 3*time.Second)
			assert.Equal(t, StatusStopped, snapshot(pm, "svc").Status)
		})
	}
}

func TestPlatformKillLeavesTermToCancel(t *testing.T) {
	old := stopGrace
	stopGrace = 400 * time.Millisecond
	t.Cleanup(func() { stopGrace = old })

	cases := []struct {
		name       string
		withCancel bool
		wantTerms  string
	}{
		{name: "cancel owns SIGTERM", withCancel: true, wantTerms: ""},
		{name: "without cancel SIGTERM is sent once", wantTerms: "t\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mark := filepath.Join(t.TempDir(), "terms")
			cmd := exec.Command("/bin/sh", "-c", `trap 'echo t >> "$MARK"' TERM; while :; do sleep 0.02; done`)
			cmd.Env = append(os.Environ(), "MARK="+mark)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			require.NoError(t, cmd.Start())
			exited := make(chan struct{})
			go func() {
				cmd.Wait()
				close(exited)
			}()
			time.Sleep(100 * time.Millisecond)

			called := 0
			var cancel func()
			if tc.withCancel {
				cancel = func() { called++ }
			}
			start := time.Now()
			require.NoError(t, PlatformKill(cmd.Process.Pid, cancel))
			assert.GreaterOrEqual(t, time.Since(start), 350*time.Millisecond)
			select {
			case <-exited:
			case <-time.After(3 * time.Second):
				t.Fatal("process not killed after grace")
			}
			b, _ := os.ReadFile(mark)
			assert.Equal(t, tc.wantTerms, string(b))
			if tc.withCancel {
				assert.Equal(t, 1, called)
			}
		})
	}
}
