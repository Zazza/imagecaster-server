package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEngineDirSuffix(t *testing.T) {
	assert.Equal(t, "engines/sd-cpp-linux", EngineDirFor(SDCppDirLegacy, "linux"))
	assert.Equal(t, "engines/llama-cpp-windows", EngineDirFor(LlamaCppDirLegacy, "windows"))
}

func TestPinEngineDirsRewritesLegacyPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := NewDefault()
	cfg.DataDir = dir

	// Старый конфиг: пути движков без суффикса платформы (относительные и абсолютные).
	legacyBackends := cfg.Backends[SDCppBackendKey]
	legacyBackends.Binary = SDCppDirLegacy + "/sd-server" + map[bool]string{true: ".exe", false: ""}[runtime.GOOS == "windows"]
	legacyBackends.WorkDir = SDCppDirLegacy
	legacyBackends.Install.Target = filepath.Join(dir, SDCppDirLegacy)
	cfg.Backends[SDCppBackendKey] = legacyBackends

	legacyLLM := llamaProcess()
	legacyLLM.Binary = filepath.Join(dir, LlamaCppDirLegacy, "llama-server"+exeSuffix())
	legacyLLM.Install.Target = LlamaCppDirLegacy
	cfg.Processes[LlamaCppProcessKey] = legacyLLM

	cfg.ApplyInstallDefaults()

	b := cfg.Backends[SDCppBackendKey]
	assert.Equal(t, SDCppDir, filepath.ToSlash(filepath.Clean(b.WorkDir)), "relative legacy workdir stays relative")
	assert.Equal(t, filepath.ToSlash(filepath.Join(dir, filepath.FromSlash(SDCppDir))), filepath.ToSlash(filepath.Clean(b.Install.Target)), "absolute legacy target stays absolute")
	assert.Contains(t, filepath.ToSlash(b.Binary), SDCppDir+"/sd-server")

	pc := cfg.Processes[LlamaCppProcessKey]
	// Относительный старый путь остаётся относительным (ResolvePaths дополнит DataDir позже).
	assert.Equal(t, filepath.Clean(LlamaCppDir), filepath.Clean(pc.Install.Target))
	assert.Equal(t, filepath.Join(dir, filepath.FromSlash(LlamaCppDir), "llama-server"+exeSuffix()), filepath.Clean(pc.Binary))
}

func TestPinEngineDirsKeepsCustomPaths(t *testing.T) {
	cfg := NewDefault()
	cfg.DataDir = t.TempDir()

	custom := cfg.Backends[SDCppBackendKey]
	custom.Binary = "/opt/my-sd-server"
	custom.WorkDir = "/opt"
	custom.Install.Target = "/opt/engine"
	cfg.Backends[SDCppBackendKey] = custom

	cfg.ApplyInstallDefaults()

	b := cfg.Backends[SDCppBackendKey]
	assert.Equal(t, "/opt/my-sd-server", b.Binary)
	assert.Equal(t, "/opt", b.WorkDir)
	assert.Equal(t, "/opt/engine", b.Install.Target)
}

func TestLegacyConfigLoadPinsEngineDirs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server-config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("port: 8080\ndata_dir: "+dir+"\nactive_sd: sdcpp\nactive_llm: llamacpp\nbackends:\n  sdcpp:\n    kind: sdcpp\n    process_key: sd\n    binary: engines/llama-cpp/../sd-cpp/sd-server\n    install:\n      method: release\n      target: engines/sd-cpp\n      version: old\nprocesses:\n  llm:\n    name: llama.cpp\n    binary: engines/llama-cpp/llama-server\n    install:\n      method: release\n      target: engines/llama-cpp\n      version: old\n"), 0o644))

	cfg, err := Load(cfgPath)
	require.NoError(t, err)

	b := cfg.Backends[SDCppBackendKey]
	require.NotNil(t, b)
	assert.Equal(t, filepath.Join(dir, filepath.FromSlash(SDCppDir)), filepath.Clean(b.Install.Target))

	pc, ok := cfg.Processes[LlamaCppProcessKey]
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, filepath.FromSlash(LlamaCppDir), "llama-server"+exeSuffix()), filepath.Clean(pc.Binary))
}
