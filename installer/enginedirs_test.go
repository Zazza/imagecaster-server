package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"imagecaster-server/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestInstaller(t *testing.T, dir string) *Installer {
	t.Helper()
	cfg := config.NewDefault()
	cfg.DataDir = dir
	return NewInstaller(&cfg)
}

func TestMigrateEngineDirsMovesMatchingBuild(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, config.LlamaCppDirLegacy)
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "llama-server"+exeSuffixFor(runtime.GOOS)), []byte("x"), 0o755))

	newTestInstaller(t, dir).MigrateEngineDirs()

	assert.True(t, dirExists(filepath.Join(dir, config.LlamaCppDir)))
	assert.False(t, dirExists(legacy))
}

func TestMigrateEngineDirsLeavesForeignBuild(t *testing.T) {
	dir := t.TempDir()
	// Сборка чужой ОС: exe в линуксе или бинарник без суффикса в винде.
	legacy := filepath.Join(dir, config.LlamaCppDirLegacy)
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	foreign := "llama-server"
	if runtime.GOOS == "windows" {
		foreign = "llama-server"
	} else {
		foreign = "llama-server.exe"
	}
	require.NoError(t, os.WriteFile(filepath.Join(legacy, foreign), []byte("x"), 0o755))

	newTestInstaller(t, dir).MigrateEngineDirs()

	assert.True(t, dirExists(legacy), "foreign build dir left as is")
	assert.False(t, dirExists(filepath.Join(dir, config.LlamaCppDir)))
}

func TestMigrateEngineDirsSkipsWhenTargetExists(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, config.SDCppDirLegacy)
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "sd-server"+exeSuffixFor(runtime.GOOS)), []byte("x"), 0o755))
	next := filepath.Join(dir, config.SDCppDir)
	require.NoError(t, os.MkdirAll(next, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(next, "marker"), []byte("x"), 0o644))

	newTestInstaller(t, dir).MigrateEngineDirs()

	data, err := os.ReadFile(filepath.Join(next, "marker"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(data), "existing platform dir not overwritten")
	assert.True(t, dirExists(legacy), "legacy dir kept when target already exists")
}

func TestNewInstallerSeesMovedLegacyBuildAsInstalled(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewDefault()
	cfg.DataDir = dir
	cfg.ApplyInstallDefaults()
	cfg.ResolvePaths()
	cfg.ApplyLLMBackend()
	pc, ok := cfg.Process(config.LlamaCppProcessKey)
	require.True(t, ok)
	require.Equal(t, filepath.Join(dir, filepath.FromSlash(config.LlamaCppDir)), filepath.Clean(pc.Install.Target))

	legacy := filepath.Join(dir, filepath.FromSlash(config.LlamaCppDirLegacy))
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "llama-server"+exeSuffixFor(runtime.GOOS)), []byte("x"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, installMark), []byte(pc.Install.Version), 0o644))

	inst := NewInstaller(&cfg)

	assert.True(t, inst.IsInstalled(config.LlamaCppProcessKey), "status counted after the move, no reinstall")
	assert.False(t, dirExists(legacy))
}

func TestMigrateEngineDirsKeepsLegacyDirStillInConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewDefault()
	cfg.DataDir = dir
	for key, bc := range cfg.Backends {
		bc.Install.Target = config.SDCppDirLegacy
		cfg.Backends[key] = bc
	}
	legacy := filepath.Join(dir, filepath.FromSlash(config.SDCppDirLegacy))
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "sd-server"+exeSuffixFor(runtime.GOOS)), []byte("x"), 0o755))

	NewInstaller(&cfg)

	assert.True(t, dirExists(legacy), "config still points at the old dir")
}

func TestSDCppEngineDirFallbackHasPlatformSuffix(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewDefault()
	cfg.DataDir = dir
	for key, bc := range cfg.Backends {
		bc.Install.Target = ""
		cfg.Backends[key] = bc
	}

	assert.Equal(t, filepath.Join(dir, filepath.FromSlash(config.SDCppDir)), SDCppEngineDir(&cfg))
	assert.NotEqual(t, filepath.Join(dir, filepath.FromSlash(config.SDCppDirLegacy)), SDCppEngineDir(&cfg))
}
