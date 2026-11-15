package installer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeZipFile(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	p := filepath.Join(t.TempDir(), "a.zip")
	require.NoError(t, os.WriteFile(p, buf.Bytes(), 0o644))
	return p
}

func writeTgzFile(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	require.NoError(t, os.WriteFile(p, buf.Bytes(), 0o644))
	return p
}

func touch(t *testing.T, p string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o755))
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func TestExtractZipStripRejectsZipSlip(t *testing.T) {
	root := t.TempDir()
	zp := writeZipFile(t, map[string]string{"top/ok.txt": "x", "top/../../evil.txt": "pwn"})
	assert.Error(t, extractZipStrip(zp, filepath.Join(root, "dst"), 1))
	assert.NoFileExists(t, filepath.Join(root, "evil.txt"))
}
