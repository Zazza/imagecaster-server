package tui

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type procCalls struct {
	calls []string
	err   error
}

func procTestModel(status string, pc *procCalls) DashboardModel {
	deps := ServerDeps{
		ProcStatus: func() map[string]ServiceInfo {
			return map[string]ServiceInfo{"sd": {Name: "sd", Status: status}}
		},
		HealthResults: func() map[string]HealthResult { return nil },
		InstallStatus: func() map[string]ComponentInstallStatus { return nil },
		StartProc:     func(n string) error { pc.calls = append(pc.calls, "start "+n); return pc.err },
		StopProc:      func(n string) error { pc.calls = append(pc.calls, "stop "+n); return pc.err },
		RestartProc:   func(n string) error { pc.calls = append(pc.calls, "restart "+n); return pc.err },
	}
	m := queueTestModel(deps)
	m.refreshServices()
	return m
}

func TestProcActions(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		key      string
		err      error
		wantCall string
	}{
		{name: "starting is stopped", status: "starting", key: "s", wantCall: "stop sd"},
		{name: "running is stopped", status: "running", key: "s", wantCall: "stop sd"},
		{name: "unhealthy is stopped", status: "unhealthy", key: "s", wantCall: "stop sd"},
		{name: "stopped is started", status: "stopped", key: "s", wantCall: "start sd"},
		{name: "crashed is started", status: "crashed", key: "s", wantCall: "start sd"},
		{name: "restart", status: "running", key: "r", wantCall: "restart sd"},
		{name: "error is shown", status: "stopped", key: "s", err: errors.New("binary missing"), wantCall: "start sd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := &procCalls{err: tc.err}
			m := procTestModel(tc.status, pc)
			next, cmd := m.Update(runeKey(tc.key))
			require.NotNil(t, cmd)
			assert.Empty(t, pc.calls, "action must run as a command, not inline")
			msg, ok := cmd().(procResultMsg)
			require.True(t, ok)
			assert.Equal(t, []string{tc.wantCall}, pc.calls)

			next, _ = next.(DashboardModel).Update(msg)
			d := next.(DashboardModel)
			if tc.err != nil {
				assert.Equal(t, "sd: binary missing", d.procFlash)
				assert.Contains(t, d.View(), "sd: binary missing")
				next, _ = d.Update(procResultMsg{name: "sd"})
				assert.Empty(t, next.(DashboardModel).procFlash)
				return
			}
			assert.Empty(t, d.procFlash)
		})
	}
}
