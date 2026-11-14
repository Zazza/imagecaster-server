package gpu

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextInterval(t *testing.T) {
	cases := []struct {
		name string
		cur  time.Duration
		ok   bool
		want time.Duration
	}{
		{name: "success resets", cur: 40 * time.Second, ok: true, want: pollInterval},
		{name: "first failure doubles", cur: pollInterval, want: 10 * time.Second},
		{name: "failures grow", cur: 20 * time.Second, want: 40 * time.Second},
		{name: "capped", cur: 40 * time.Second, want: maxPollInterval},
		{name: "stays capped", cur: maxPollInterval, want: maxPollInterval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nextInterval(tc.cur, tc.ok))
		})
	}
}

func fakeSMI(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	p := filepath.Join(t.TempDir(), "nvidia-smi")
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	return p
}

func TestPoll(t *testing.T) {
	cases := []struct {
		name      string
		script    string
		wantOK    bool
		wantTotal int
	}{
		{name: "answer", script: `echo "RTX, 24576, 100, 24476, 3"`, wantOK: true, wantTotal: 24576},
		{name: "hung binary times out", script: `exec sleep 30`},
		{name: "missing driver fails", script: `exit 9`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gm := &GPUMonitor{binary: fakeSMI(t, tc.script), timeout: 200 * time.Millisecond}
			start := time.Now()
			ok := gm.poll(context.Background())
			assert.Less(t, time.Since(start), 3*time.Second)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantOK, gm.Info().Available)
			assert.Equal(t, tc.wantTotal, gm.Info().MemoryTotal)
		})
	}
}
