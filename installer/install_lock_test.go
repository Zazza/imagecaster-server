package installer

import (
	"archive/zip"
	"bytes"
	"errors"
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
	"imagecaster-server/process"
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
	return config.InstallConfig{Method: config.InstallZip, URL: url, Target: target}
}

func baseConfig(t *testing.T) *config.Config {
	return &config.Config{
		DataDir:   t.TempDir(),
		Processes: map[string]config.ProcessConfig{},
		Backends:  map[string]config.BackendConfig{},
	}
}

type setupCalls struct {
	mu      sync.Mutex
	targets []string
}

func (s *setupCalls) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func stubSetup(inst *Installer, err error) *setupCalls {
	c := &setupCalls{}
	inst.backendSetup = func(target string, lb *process.RingBuffer) error {
		c.mu.Lock()
		c.targets = append(c.targets, target)
		c.mu.Unlock()
		return err
	}
	return c
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
	inst := NewInstaller(cfg)
	stubSetup(inst, nil)

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
	inst := NewInstaller(cfg)
	stubSetup(inst, nil)

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
	inst := NewInstaller(cfg)
	calls := stubSetup(inst, nil)

	require.NoError(t, inst.EnsureInstalled("other", ""))
	assert.Empty(t, calls.list())

	st := inst.Status()
	require.Contains(t, st, "other")
	assert.True(t, st["other"].Installed)
	assert.NotContains(t, st, "testbackend")
}

func neoConfig(t *testing.T, url string) (*config.Config, string) {
	t.Helper()
	cfg := baseConfig(t)
	target := filepath.Join(t.TempDir(), "neo")
	cfg.ActiveSD = config.NeoBackendKey
	cfg.Backends[config.NeoBackendKey] = config.BackendConfig{
		Name:       "neo",
		ProcessKey: "sd",
		Binary:     VenvPython(target),
		Install:    zipInstall(url, target),
	}
	return cfg, target
}

func makeVenv(t *testing.T, target string) {
	t.Helper()
	p := VenvPython(target)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o755))
}

func TestInstallNeoRunsSetupOnTarget(t *testing.T) {
	srv, _ := zipServer(t, nil)
	cfg, target := neoConfig(t, srv.URL)
	inst := NewInstaller(cfg)
	calls := stubSetup(inst, nil)

	require.NoError(t, inst.Install(config.NeoBackendKey))

	assert.Equal(t, []string{target}, calls.list())
}

func TestInstallNeoSetupErrorFailsInstall(t *testing.T) {
	srv, _ := zipServer(t, nil)
	cfg, _ := neoConfig(t, srv.URL)
	inst := NewInstaller(cfg)
	calls := stubSetup(inst, errors.New("venv failed"))

	err := inst.Install(config.NeoBackendKey)

	require.Error(t, err)
	assert.Len(t, calls.list(), 1)
	assert.False(t, inst.IsInstalled(config.NeoBackendKey))
	st := inst.Status()
	require.Contains(t, st, config.NeoBackendKey)
	assert.False(t, st[config.NeoBackendKey].Installed)
}

func TestInstallNonNeoBackendSkipsSetup(t *testing.T) {
	srv, hits := zipServer(t, nil)
	cfg := baseConfig(t)
	cfg.ActiveSD = "testbackend"
	cfg.Backends["testbackend"] = config.BackendConfig{
		Name:       "test",
		ProcessKey: "sd",
		Install:    zipInstall(srv.URL, filepath.Join(t.TempDir(), "tb")),
	}
	inst := NewInstaller(cfg)
	calls := stubSetup(inst, nil)

	require.NoError(t, inst.Install("testbackend"))

	assert.Equal(t, int32(1), atomic.LoadInt32(hits))
	assert.Empty(t, calls.list())
}

func TestNeoInstalledRequiresFolderAndVenv(t *testing.T) {
	tests := []struct {
		name   string
		folder bool
		venv   bool
		want   bool
	}{
		{"nothing", false, false, false},
		{"folder without venv", true, false, false},
		{"folder and venv", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := zipServer(t, nil)
			cfg, target := neoConfig(t, srv.URL)
			if tt.folder {
				require.NoError(t, os.MkdirAll(target, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(target, "launch.py"), []byte("x"), 0o644))
			}
			if tt.venv {
				makeVenv(t, target)
			}
			inst := NewInstaller(cfg)

			assert.Equal(t, tt.want, inst.IsInstalled(config.NeoBackendKey))
			st := inst.Status()
			require.Contains(t, st, config.NeoBackendKey)
			assert.Equal(t, tt.want, st[config.NeoBackendKey].Installed)
		})
	}
}

func TestInstallNeoRetryAfterFailedSetupSkipsDownload(t *testing.T) {
	srv, hits := zipServer(t, nil)
	cfg, target := neoConfig(t, srv.URL)
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "launch.py"), []byte("x"), 0o644))
	inst := NewInstaller(cfg)
	c := &setupCalls{}
	inst.backendSetup = func(tg string, lb *process.RingBuffer) error {
		c.mu.Lock()
		c.targets = append(c.targets, tg)
		c.mu.Unlock()
		makeVenv(t, tg)
		return nil
	}

	require.NoError(t, inst.Install(config.NeoBackendKey))

	assert.Equal(t, int32(0), atomic.LoadInt32(hits))
	assert.Equal(t, []string{target}, c.list())
	assert.True(t, inst.IsInstalled(config.NeoBackendKey))
}

func TestInstallNeoRetrySetupErrorFailsInstall(t *testing.T) {
	srv, hits := zipServer(t, nil)
	cfg, target := neoConfig(t, srv.URL)
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "launch.py"), []byte("x"), 0o644))
	inst := NewInstaller(cfg)
	calls := stubSetup(inst, errors.New("venv failed"))

	err := inst.Install(config.NeoBackendKey)

	require.Error(t, err)
	assert.Equal(t, int32(0), atomic.LoadInt32(hits))
	assert.Len(t, calls.list(), 1)
	assert.False(t, inst.IsInstalled(config.NeoBackendKey))
}

func TestStatusOnlyActiveBackend(t *testing.T) {
	srv, _ := zipServer(t, nil)
	cfg, _ := neoConfig(t, srv.URL)
	cfg.Backends["other"] = config.BackendConfig{
		Name:       "other",
		ProcessKey: "sd",
		Install:    zipInstall(srv.URL, filepath.Join(t.TempDir(), "other")),
	}
	inst := NewInstaller(cfg)

	st := inst.Status()

	assert.Contains(t, st, config.NeoBackendKey)
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
				Install:    zipInstall(srv.URL, filepath.Join(t.TempDir(), "tb")),
			}
			p := config.ProcessConfig{Name: "sd", AutoStart: true}
			if tt.onBackend {
				b.Binary = bin
			} else {
				p.Binary = bin
			}
			cfg.Backends["testbackend"] = b
			cfg.Processes["sd"] = p
			inst := NewInstaller(cfg)
			calls := stubSetup(inst, nil)

			inst.EnsureAllInstalled()

			assert.Equal(t, int32(0), atomic.LoadInt32(hits))
			assert.Empty(t, calls.list())
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
	inst := NewInstaller(cfg)
	stubSetup(inst, nil)

	inst.EnsureAllInstalled()

	assert.Equal(t, int32(1), atomic.LoadInt32(hits))
	assert.True(t, inst.IsInstalled("testbackend"))
}
