package installer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
	"imagecaster-server/process"
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

func TestExtractZipFlat(t *testing.T) {
	zp := writeZipFile(t, map[string]string{
		"ollama.exe":       "bin",
		"lib/ollama/a.dll": "dll",
	})
	dst := t.TempDir()

	require.NoError(t, extractZipFlat(zp, dst))

	b, err := os.ReadFile(filepath.Join(dst, "ollama.exe"))
	require.NoError(t, err)
	assert.Equal(t, "bin", string(b))
	b, err = os.ReadFile(filepath.Join(dst, "lib", "ollama", "a.dll"))
	require.NoError(t, err)
	assert.Equal(t, "dll", string(b))
}

func TestExtractZipFlatKeepsTopFolder(t *testing.T) {
	zp := writeZipFile(t, map[string]string{"top/file.txt": "x"})
	dst := t.TempDir()

	require.NoError(t, extractZipFlat(zp, dst))

	assert.FileExists(t, filepath.Join(dst, "top", "file.txt"))
}

func TestExtractZipFlatRejectsZipSlip(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "target")
	require.NoError(t, os.MkdirAll(dst, 0o755))
	zp := writeZipFile(t, map[string]string{"../evil.txt": "pwn"})

	err := extractZipFlat(zp, dst)

	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(root, "evil.txt"))
}

func TestUvPath(t *testing.T) {
	assert.Equal(t, filepath.Join("/data", "bin", exe("uv")), UvPath("/data"))
}

func TestVerifySHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(p, []byte("payload"), 0o644))
	sum := sha256.Sum256([]byte("payload"))
	good := hex.EncodeToString(sum[:])

	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"match", good, false},
		{"match upper", strings.ToUpper(good), false},
		{"mismatch", strings.Repeat("0", 64), true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifySHA256(p, tt.want)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestExtractUv(t *testing.T) {
	uv := exe("uv")
	tests := []struct {
		name    string
		isZip   bool
		entries map[string]string
		wantErr bool
	}{
		{"zip root", true, map[string]string{uv: "UV", exe("uvx"): "UVX"}, false},
		{"zip nested", true, map[string]string{"uv-x86_64/" + uv: "UV", "uv-x86_64/" + exe("uvx"): "UVX"}, false},
		{"tgz nested", false, map[string]string{"uv-x86_64-unknown-linux-gnu/" + uv: "UV", "uv-x86_64-unknown-linux-gnu/uvx": "UVX"}, false},
		{"tgz root", false, map[string]string{uv: "UV"}, false},
		{"zip missing", true, map[string]string{"readme.txt": "x"}, true},
		{"tgz missing", false, map[string]string{"dir/uvx": "x"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var archive string
			if tt.isZip {
				archive = writeZipFile(t, tt.entries)
			} else {
				archive = writeTgzFile(t, tt.entries)
			}
			dst := filepath.Join(t.TempDir(), uv)

			err := extractUv(archive, tt.isZip, dst)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			b, err := os.ReadFile(dst)
			require.NoError(t, err)
			assert.Equal(t, "UV", string(b))
			if runtime.GOOS != "windows" {
				fi, err := os.Stat(dst)
				require.NoError(t, err)
				assert.NotZero(t, fi.Mode().Perm()&0o111)
			}
		})
	}
}

func TestVenvPython(t *testing.T) {
	want := filepath.Join("/w", "venv", "bin", "python")
	if runtime.GOOS == "windows" {
		want = filepath.Join("/w", "venv", "Scripts", "python.exe")
	}
	assert.Equal(t, want, VenvPython("/w"))
}

func TestSetupBackendSkipsExistingVenv(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.DataDir = dataDir
	inst := NewInstaller(&cfg)
	target := t.TempDir()
	touch(t, VenvPython(target))

	err := inst.setupBackend(target, process.NewRingBuffer(100))

	require.NoError(t, err)
	assert.NoFileExists(t, UvPath(dataDir))
}
