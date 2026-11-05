package installer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
	"imagecaster-server/process"
)

func TestExtractZipStripsTopFolder(t *testing.T) {
	zp := writeZipFile(t, map[string]string{"forge-2.31/webui.py": "py", "forge-2.31/a/b.txt": "b"})
	dst := filepath.Join(t.TempDir(), "forge")

	require.NoError(t, extractZip(zp, dst))

	assert.FileExists(t, filepath.Join(dst, "webui.py"))
	assert.FileExists(t, filepath.Join(dst, "a", "b.txt"))
	left, _ := filepath.Glob(dst + ".part-*")
	assert.Empty(t, left)
}

func TestExtractZipRejectsZipSlipAndLeavesNoTarget(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "forge")
	zp := writeZipFile(t, map[string]string{"top/ok.txt": "x", "top/../../evil.txt": "pwn"})

	require.Error(t, extractZip(zp, dst))

	assert.NoFileExists(t, filepath.Join(root, "evil.txt"))
	assert.NoDirExists(t, dst)
	left, _ := filepath.Glob(dst + ".part-*")
	assert.Empty(t, left)
}

func TestExtractZipRemovesStaleUnfinishedExtract(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "forge")
	touch(t, filepath.Join(root, "forge.part-123", "half.txt"))
	zp := writeZipFile(t, map[string]string{"top/ok.txt": "x"})

	require.NoError(t, extractZip(zp, dst))

	assert.NoDirExists(t, filepath.Join(root, "forge.part-123"))
	assert.FileExists(t, filepath.Join(dst, "ok.txt"))
}

func TestInstallZipKeepsExistingNonNeoFolder(t *testing.T) {
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	inst := NewInstaller(&cfg)
	target := filepath.Join(cfg.DataDir, "my-forge")
	touch(t, filepath.Join(target, "models", "Stable-diffusion", "mine.safetensors"))

	err := inst.installZip("my-forge", config.InstallConfig{Method: config.InstallZip, URL: "http://127.0.0.1:1/never.zip", Target: "my-forge"}, process.NewRingBuffer(10))

	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(target, "models", "Stable-diffusion", "mine.safetensors"))
}

func TestInstallZipNeoFolderWithOnlyModelsGetsCodeAndKeepsModels(t *testing.T) {
	zp := writeZipFile(t, map[string]string{"forge-2.31/launch.py": "py", "forge-2.31/models/Stable-diffusion/put.txt": "x"})
	zipBytes, err := os.ReadFile(zp)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(zipBytes) }))
	t.Cleanup(srv.Close)

	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	inst := NewInstaller(&cfg)
	setupCalls := 0
	inst.backendSetup = func(string, *process.RingBuffer) error { setupCalls++; return nil }
	target := filepath.Join(cfg.DataDir, "neo")
	touch(t, filepath.Join(target, "models", "Stable-diffusion", "mine.safetensors"))

	err = inst.installZip(config.NeoBackendKey, config.InstallConfig{Method: config.InstallZip, URL: srv.URL, Target: "neo"}, process.NewRingBuffer(10))

	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(target, "launch.py"))
	assert.FileExists(t, filepath.Join(target, "models", "Stable-diffusion", "mine.safetensors"))
	assert.FileExists(t, filepath.Join(target, "models", "Stable-diffusion", "put.txt"))
	assert.Equal(t, 1, setupCalls)
}
