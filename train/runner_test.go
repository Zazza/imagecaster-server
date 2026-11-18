//go:build unix

package train

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type progressRecorder struct {
	mu    sync.Mutex
	calls [][2]int
}

func (p *progressRecorder) record(step, steps int) {
	p.mu.Lock()
	p.calls = append(p.calls, [2]int{step, steps})
	p.mu.Unlock()
}

func (p *progressRecorder) has(step, steps int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.calls {
		if c == [2]int{step, steps} {
			return true
		}
	}
	return false
}

type processFixture struct {
	python  string
	toolkit string
	argsLog string
	pidFile string
	spec    RunSpec
}

func newProcessFixture(t *testing.T, body string) processFixture {
	t.Helper()
	dir := t.TempDir()
	toolkit := filepath.Join(dir, "ai-toolkit")
	require.NoError(t, os.MkdirAll(toolkit, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(toolkit, "run.py"), []byte("print('x')\n"), 0o644))
	argsLog := filepath.Join(dir, "args.log")
	pidFile := filepath.Join(dir, "child.pid")
	script := fmt.Sprintf("#!/bin/sh\nARGS_LOG=%q\nPID_FILE=%q\npwd > \"$ARGS_LOG\"\nfor a in \"$@\"; do echo \"$a\" >> \"$ARGS_LOG\"; done\n%s\n", argsLog, pidFile, body)
	python := filepath.Join(dir, "python")
	require.NoError(t, os.WriteFile(python, []byte(script), 0o755))
	work := filepath.Join(dir, "job")
	require.NoError(t, os.MkdirAll(filepath.Join(work, "dataset"), 0o755))
	configPath := filepath.Join(work, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("job: extension\n"), 0o644))
	localPath := filepath.Join(work, "local.json")
	require.NoError(t, os.WriteFile(localPath, []byte("{}"), 0o644))
	return processFixture{
		python:  python,
		toolkit: toolkit,
		argsLog: argsLog,
		pidFile: pidFile,
		spec: RunSpec{
			WorkDir:    work,
			ConfigPath: configPath,
			LocalPath:  localPath,
			DatasetDir: filepath.Join(work, "dataset"),
			OutputPath: filepath.Join(work, "output", "lora.safetensors"),
			LogPath:    filepath.Join(work, "train.log"),
			Steps:      10,
		},
	}
}

func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	fields := strings.Fields(string(stat))
	return len(fields) < 3 || fields[2] != "Z"
}

func TestProcessRunner_ExitCode(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"exit 0", "exit 0", false},
		{"exit 3", "echo 'Traceback: CUDA error' >&2\nexit 3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newProcessFixture(t, tt.body)
			r := NewProcessRunner(f.python, f.toolkit)

			err := r.Run(context.Background(), f.spec, func(step, steps int) {})
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestProcessRunner_CommandLine(t *testing.T) {
	f := newProcessFixture(t, "exit 0")
	r := NewProcessRunner(f.python, f.toolkit)
	script := filepath.Join(f.spec.WorkDir, "aitk_local.py")
	require.NoFileExists(t, script)

	require.NoError(t, r.Run(context.Background(), f.spec, func(step, steps int) {}))

	data, err := os.ReadFile(f.argsLog)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantDir, err := filepath.EvalSymlinks(f.toolkit)
	require.NoError(t, err)
	require.Len(t, lines, 5, string(data))
	gotDir, err := filepath.EvalSymlinks(lines[0])
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir)
	assert.Equal(t, []string{script, f.toolkit, f.spec.ConfigPath, f.spec.LocalPath}, lines[1:])

	body, err := os.ReadFile(script)
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(string(body)), "aitk_local.py must contain the embedded script")
}

func TestProcessRunner_ScriptWrittenBeforeStart(t *testing.T) {
	copyTo := filepath.Join(t.TempDir(), "seen.py")
	f := newProcessFixture(t, fmt.Sprintf("cp \"$1\" %q || exit 7\nexit 0", copyTo))
	script := filepath.Join(f.spec.WorkDir, "aitk_local.py")
	r := NewProcessRunner(f.python, f.toolkit)

	require.NoError(t, r.Run(context.Background(), f.spec, func(step, steps int) {}))

	seen, err := os.ReadFile(copyTo)
	require.NoError(t, err)
	onDisk, err := os.ReadFile(script)
	require.NoError(t, err)
	assert.Equal(t, onDisk, seen)
	assert.NotEmpty(t, seen)
}

func TestProcessRunner_Progress(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"stdout line", "echo 'x:  50%| 5/10 [00:05<00:05, 1.00it/s]'\nexit 0"},
		{"stderr carriage return", "printf 'x:  40%%| 4/10 [00:04<00:06]\\rx:  50%%| 5/10 [00:05<00:05]\\r' >&2\nexit 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newProcessFixture(t, tt.body)
			r := NewProcessRunner(f.python, f.toolkit)
			rec := &progressRecorder{}

			require.NoError(t, r.Run(context.Background(), f.spec, rec.record))
			assert.True(t, rec.has(5, 10), "progress calls: %v", rec.calls)
		})
	}
}

func TestProcessRunner_ProgressWhileRunning(t *testing.T) {
	f := newProcessFixture(t, "printf 'x:  50%%| 5/10 [00:05<00:05]\\r' >&2\nsleep 30\nexit 0")
	r := NewProcessRunner(f.python, f.toolkit)
	rec := &progressRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- r.Run(ctx, f.spec, rec.record) }()

	assert.Eventually(t, func() bool { return rec.has(5, 10) }, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestProcessRunner_CancelKillsChildren(t *testing.T) {
	body := "sleep 30 &\necho $! > \"$PID_FILE\"\necho 'x:  50%| 5/10 [00:05<00:05]'\nwait"
	f := newProcessFixture(t, body)
	r := NewProcessRunner(f.python, f.toolkit)
	rec := &progressRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- r.Run(ctx, f.spec, rec.record) }()

	require.Eventually(t, func() bool { return rec.has(5, 10) }, 5*time.Second, 10*time.Millisecond)
	data, err := os.ReadFile(f.pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	require.True(t, processAlive(pid))
	t.Cleanup(func() {
		if processAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	start := time.Now()
	cancel()
	select {
	case err := <-errCh:
		assert.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	assert.Less(t, time.Since(start), 10*time.Second)
	assert.Eventually(t, func() bool { return !processAlive(pid) }, 5*time.Second, 20*time.Millisecond, "child sleep %d survived cancel", pid)
}

func TestProcessRunner_LogKeptAcrossResume(t *testing.T) {
	f := newProcessFixture(t, "echo run-output\nexit 0")
	r := NewProcessRunner(f.python, f.toolkit)
	require.NoError(t, r.Run(context.Background(), f.spec, func(step, steps int) {}))
	require.NoError(t, r.Run(context.Background(), f.spec, func(step, steps int) {}))
	data, err := os.ReadFile(f.spec.LogPath)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(data), "run-output"))
}
