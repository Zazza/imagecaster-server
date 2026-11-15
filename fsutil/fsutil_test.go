package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomicReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(p, []byte("old"), 0o644))
	require.NoError(t, WriteFileAtomic(p, []byte("new"), 0o600))
	got, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))
	assert.NoFileExists(t, p+".tmp")
}

func TestWriteFileAtomicMissingDirFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "no", "f.txt")
	require.Error(t, WriteFileAtomic(p, []byte("x"), 0o644))
	assert.NoFileExists(t, p)
}

func TestWriteFileAtomicRenameFailureRemovesTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.MkdirAll(filepath.Join(target, "child"), 0o755))
	require.Error(t, WriteFileAtomic(target, []byte("x"), 0o644))
	assert.NoFileExists(t, target+".tmp")
}

func TestRenameWithRetry(t *testing.T) {
	orig, origDelay := rename, renameDelay
	t.Cleanup(func() { rename, renameDelay = orig, origDelay })
	renameDelay = time.Millisecond
	calls := 0
	rename = func(src, dst string) error {
		calls++
		if calls < 3 {
			return errors.New("locked")
		}
		return nil
	}
	require.NoError(t, renameWithRetry("a", "b", 5))
	assert.Equal(t, 3, calls)

	calls = 0
	rename = func(src, dst string) error {
		calls++
		return errors.New("locked")
	}
	require.EqualError(t, renameWithRetry("a", "b", 4), "locked")
	assert.Equal(t, 4, calls)
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(f, nil, 0o644))
	assert.True(t, FileExists(f))
	assert.True(t, FileExists(dir))
	assert.False(t, FileExists(""))
	assert.False(t, FileExists(filepath.Join(dir, "none")))
	assert.True(t, DirExists(dir))
	assert.False(t, DirExists(f))
	assert.False(t, DirExists(""))
}

func TestReplaceFileKeepsPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	require.NoError(t, ReplaceFile(p, []byte("a"), 0o640))
	st, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), st.Mode().Perm())

	require.NoError(t, os.Chmod(p, 0o600))
	require.NoError(t, ReplaceFile(p, []byte("b"), 0o644))
	st, err = os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	got, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "b", string(got))
}
