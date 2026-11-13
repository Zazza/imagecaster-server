package installer

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

func TestLogsUnknownKeyIsNil(t *testing.T) {
	cfg := baseConfig(t)
	inst := NewInstaller(cfg)

	assert.Nil(t, inst.Logs("no-such-key", 10))
	assert.Nil(t, inst.Logs("", 10))
}

func logsConfig(t *testing.T, url string) *config.Config {
	t.Helper()
	cfg := baseConfig(t)
	cfg.ActiveSD = "testbackend"
	cfg.Backends["testbackend"] = config.BackendConfig{
		Name:       "test",
		ProcessKey: "sd",
		Install:    zipInstall(url, filepath.Join(t.TempDir(), "tb")),
	}
	return cfg
}

func TestLogsReturnsInstallLines(t *testing.T) {
	srv, _ := zipServer(t, nil)
	inst := newLockInstaller(t, logsConfig(t, srv.URL), srv.URL)

	require.NoError(t, inst.Install("testbackend"))

	all := inst.Logs("testbackend", 1000)
	require.GreaterOrEqual(t, len(all), 2)
	assert.Contains(t, all, "Downloading "+srv.URL)
	assert.Contains(t, all, "Installation complete")
	assert.Equal(t, all[len(all)-2:], inst.Logs("testbackend", 2))

	assert.Nil(t, inst.Logs("other", 10))
}

func TestLogsKeptAfterFailedInstall(t *testing.T) {
	srv, _ := zipServer(t, nil)
	inst := NewInstaller(logsConfig(t, srv.URL))
	inst.ReleaseArtifacts = func() ([]Artifact, error) {
		return []Artifact{{Name: "pkg.zip", URL: srv.URL, SHA256: "00000000000000000000000000000000000000000000000000000000000000ff", Kind: "zip", Dest: filepath.Join(t.TempDir(), "art"), Strip: 1}}, nil
	}

	require.Error(t, inst.Install("testbackend"))

	assert.Contains(t, inst.Logs("testbackend", 100), "Downloading "+srv.URL)
}
