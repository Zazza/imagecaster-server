package installer

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
	"imagecaster-server/process"
)

func TestLogsUnknownKeyIsNil(t *testing.T) {
	cfg := baseConfig(t)
	inst := NewInstaller(cfg)

	assert.Nil(t, inst.Logs("no-such-key", 10))
	assert.Nil(t, inst.Logs("", 10))
}

func TestLogsReturnsInstallLines(t *testing.T) {
	srv, _ := zipServer(t, nil)
	cfg, _ := neoConfig(t, srv.URL)
	inst := NewInstaller(cfg)
	inst.backendSetup = func(target string, lb *process.RingBuffer) error {
		for i := 1; i <= 5; i++ {
			lb.Write(fmt.Sprintf("setup-line-%d", i))
		}
		makeVenv(t, target)
		return nil
	}

	require.NoError(t, inst.Install(config.NeoBackendKey))

	all := inst.Logs(config.NeoBackendKey, 1000)
	require.NotEmpty(t, all)
	for i := 1; i <= 5; i++ {
		assert.Contains(t, all, fmt.Sprintf("setup-line-%d", i))
	}

	require.GreaterOrEqual(t, len(all), 2)
	assert.Equal(t, all[len(all)-2:], inst.Logs(config.NeoBackendKey, 2))

	assert.Nil(t, inst.Logs("other", 10))
}

func TestLogsKeptAfterFailedInstall(t *testing.T) {
	srv, _ := zipServer(t, nil)
	cfg := baseConfig(t)
	target := filepath.Join(t.TempDir(), "neo")
	cfg.ActiveSD = config.NeoBackendKey
	cfg.Backends[config.NeoBackendKey] = config.BackendConfig{
		Name:       "neo",
		ProcessKey: "sd",
		Binary:     VenvPython(target),
		Install:    zipInstall(srv.URL, target),
	}
	inst := NewInstaller(cfg)
	inst.backendSetup = func(target string, lb *process.RingBuffer) error {
		lb.Write("pip exploded")
		return fmt.Errorf("venv failed")
	}

	require.Error(t, inst.Install(config.NeoBackendKey))

	assert.Contains(t, inst.Logs(config.NeoBackendKey, 100), "pip exploded")
}
