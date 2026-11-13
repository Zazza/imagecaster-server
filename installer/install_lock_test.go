package installer

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

func testZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("pkg/hello.txt")
	require.NoError(t, err)
	_, err = f.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func zipServer(t *testing.T, gate chan struct{}) (*httptest.Server, *int32) {
	t.Helper()
	data := testZip(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if gate != nil {
			<-gate
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func zipInstall(url, target string) config.InstallConfig {
	return config.InstallConfig{Method: config.InstallRelease, URL: url, Target: target, Version: "v1"}
}

func legacyInstall(url, target string) config.InstallConfig {
	return config.InstallConfig{Method: "zip", URL: url, Target: target}
}

func newLockInstaller(t *testing.T, cfg *config.Config, url string) *Installer {
	t.Helper()
	sum := sha256.Sum256(testZip(t))
	dest := filepath.Join(t.TempDir(), "art")
	inst := NewInstaller(cfg)
	inst.ReleaseArtifacts = func() ([]Artifact, error) {
		return []Artifact{{Name: "pkg.zip", URL: url, SHA256: hex.EncodeToString(sum[:]), Kind: "zip", Dest: dest, Strip: 1}}, nil
	}
	return inst
}

func baseConfig(t *testing.T) *config.Config {
	return &config.Config{
		DataDir:   t.TempDir(),
		Processes: map[string]config.ProcessConfig{},
		Backends:  map[string]config.BackendConfig{},
	}
}

func TestInstallConcurrentSameKeyDownloadsOnce(t *testing.T) {
	gate := make(chan struct{})
	srv, hits := zipServer(t, gate)
	cfg := baseConfig(t)
	cfg.ActiveSD = "testbackend"
	cfg.Backends["testbackend"] = config.BackendConfig{
		Name:       "test",
		ProcessKey: "sd",
		Install:    zipInstall(srv.URL, filepath.Join(t.TempDir(), "tb")),
	}
	inst := newLockInstaller(t, cfg, srv.URL)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); errs[0] = inst.Install("testbackend") }()
	require.Eventually(t, func() bool { return atomic.LoadInt32(hits) == 1 }, 5*time.Second, 5*time.Millisecond)
	wg.Add(1)
	go func() { defer wg.Done(); errs[1] = inst.Install("testbackend") }()
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()

	assert.NoError(t, errs[0])
	assert.NoError(t, errs[1])
	assert.Equal(t, int32(1), atomic.LoadInt32(hits))
	assert.True(t, inst.IsInstalled("testbackend"))
}

func TestEnsureInstalledBackendProcessUsesBackendKey(t *testing.T) {
	srv, hits := zipServer(t, nil)
	cfg := baseConfig(t)
	cfg.ActiveSD = "testbackend"
	cfg.Backends["testbackend"] = config.BackendConfig{
		Name:       "test",
		ProcessKey: "sd",
		Install:    zipInstall(srv.URL, filepath.Join(t.TempDir(), "tb")),
	}
	cfg.Processes["sd"] = config.ProcessConfig{
		Name:    "sd",
		Install: zipInstall(srv.URL, filepath.Join(t.TempDir(), "sdproc")),
	}
	inst := newLockInstaller(t, cfg, srv.URL)

	require.NoError(t, inst.EnsureInstalled("sd", ""))

	st := inst.Status()
	require.Contains(t, st, "testbackend")
	assert.True(t, st["testbackend"].Installed)
	assert.NotContains(t, st, "sd")
	assert.Equal(t, int32(1), atomic.LoadInt32(hits))
}

func TestEnsureInstalledOtherProcessUsesOwnKey(t *testing.T) {
	srv, _ := zipServer(t, nil)
	cfg := baseConfig(t)
	cfg.ActiveSD = "testbackend"
	cfg.Backends["testbackend"] = config.BackendConfig{Name: "test", ProcessKey: "sd"}
	cfg.Processes["other"] = config.ProcessConfig{
		Name:    "other",
		Install: zipInstall(srv.URL, filepath.Join(t.TempDir(), "other")),
	}
	inst := newLockInstaller(t, cfg, srv.URL)

	require.NoError(t, inst.EnsureInstalled("other", ""))

	st := inst.Status()
	require.Contains(t, st, "other")
	assert.True(t, st["other"].Installed)
	assert.NotContains(t, st, "testbackend")
}

func TestInstallBackendDownloadsOnce(t *testing.T) {
	srv, hits := zipServer(t, nil)
	cfg := baseConfig(t)
	cfg.ActiveSD = "testbackend"
	cfg.Backends["testbackend"] = config.BackendConfig{
		Name:       "test",
		ProcessKey: "sd",
		Install:    zipInstall(srv.URL, filepath.Join(t.TempDir(), "tb")),
	}
	inst := newLockInstaller(t, cfg, srv.URL)

	require.NoError(t, inst.Install("testbackend"))

	assert.Equal(t, int32(1), atomic.LoadInt32(hits))
}

func TestStatusOnlyActiveBackend(t *testing.T) {
	srv, _ := zipServer(t, nil)
	cfg := baseConfig(t)
	cfg.ActiveSD = "testbackend"
	cfg.Backends["testbackend"] = config.BackendConfig{Name: "test", ProcessKey: "sd", Install: zipInstall(srv.URL, filepath.Join(t.TempDir(), "tb"))}
	cfg.Backends["other"] = config.BackendConfig{
		Name:       "other",
		ProcessKey: "sd",
		Install:    zipInstall(srv.URL, filepath.Join(t.TempDir(), "other")),
	}
	inst := newLockInstaller(t, cfg, srv.URL)

	st := inst.Status()

	assert.Contains(t, st, "testbackend")
	assert.NotContains(t, st, "other")
}

func TestEnsureAllInstalledSkipsBackendWithExistingBinary(t *testing.T) {
	tests := []struct {
		name      string
		onBackend bool
	}{
		{"binary in backend", true},
		{"binary in process", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, hits := zipServer(t, nil)
			bin := filepath.Join(t.TempDir(), "custom", "webui")
			require.NoError(t, os.MkdirAll(filepath.Dir(bin), 0o755))
			require.NoError(t, os.WriteFile(bin, []byte("x"), 0o755))

			cfg := baseConfig(t)
			cfg.ActiveSD = "testbackend"
			b := config.BackendConfig{
				Name:       "test",
				ProcessKey: "sd",
				Install:    legacyInstall(srv.URL, filepath.Join(t.TempDir(), "tb")),
			}
			p := config.ProcessConfig{Name: "sd", AutoStart: true}
			if tt.onBackend {
				b.Binary = bin
			} else {
				p.Binary = bin
			}
			cfg.Backends["testbackend"] = b
			cfg.Processes["sd"] = p
			inst := newLockInstaller(t, cfg, srv.URL)

			inst.EnsureAllInstalled()

			assert.Equal(t, int32(0), atomic.LoadInt32(hits))
		})
	}
}

func TestEnsureAllInstalledInstallsBackendWithoutBinary(t *testing.T) {
	srv, hits := zipServer(t, nil)
	cfg := baseConfig(t)
	cfg.ActiveSD = "testbackend"
	cfg.Backends["testbackend"] = config.BackendConfig{
		Name:       "test",
		ProcessKey: "sd",
		Binary:     filepath.Join(t.TempDir(), "missing", "webui"),
		Install:    zipInstall(srv.URL, filepath.Join(t.TempDir(), "tb")),
	}
	cfg.Processes["sd"] = config.ProcessConfig{Name: "sd", AutoStart: true}
	inst := newLockInstaller(t, cfg, srv.URL)

	inst.EnsureAllInstalled()

	assert.Equal(t, int32(1), atomic.LoadInt32(hits))
	assert.True(t, inst.IsInstalled("testbackend"))
}
