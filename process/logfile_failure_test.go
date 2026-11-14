package process

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRotationFailureDoesNotDeadlockStdLogger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")
	require.NoError(t, os.MkdirAll(filepath.Join(path+".1", "keep"), 0o755))
	rf, err := OpenRotatingFile(path, 64)
	require.NoError(t, err)
	defer rf.Close()
	logger := log.New(rf, "", 0)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			logger.Printf("line %d %s", i, strings.Repeat("x", 40))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logger blocked after failed rotation")
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "line 9")
}

func TestRotatingFileReopensAfterFailedOpen(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "logs")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	path := filepath.Join(sub, "proc.log")
	rf, err := OpenRotatingFile(path, 1<<20)
	require.NoError(t, err)
	defer rf.Close()

	rf.mu.Lock()
	require.NoError(t, rf.f.Close())
	rf.f = nil
	rf.mu.Unlock()

	_, err = rf.Write([]byte("after reopen\n"))
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "after reopen")
}

func TestRotatingFileWriteAfterCloseIsErrClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proc.log")
	rf, err := OpenRotatingFile(path, 1<<20)
	require.NoError(t, err)
	require.NoError(t, rf.Close())
	_, err = rf.Write([]byte("x"))
	assert.ErrorIs(t, err, os.ErrClosed)
	_, statErr := os.Stat(path)
	require.NoError(t, statErr)
}
