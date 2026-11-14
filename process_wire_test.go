//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
	sdgpu "imagecaster-server/gpu"
	sdhealth "imagecaster-server/health"
	"imagecaster-server/installer"
	"imagecaster-server/models"
	"imagecaster-server/process"
)

func sleeperConfig(key string) *config.Config {
	return &config.Config{Processes: map[string]config.ProcessConfig{
		key: {Name: key, Binary: "/bin/sleep", Args: []string{"30"}},
	}}
}

func newSleeperPM(t *testing.T, cfg *config.Config) *process.ProcessManager {
	t.Helper()
	pm := process.NewProcessManager(cfg, nil, t.TempDir())
	t.Cleanup(pm.Close)
	return pm
}

func procState(pm *process.ProcessManager, key string) (string, int, []string) {
	mp, ok := pm.Get(key)
	if !ok {
		return "", 0, nil
	}
	pm.Mu.RLock()
	defer pm.Mu.RUnlock()
	var args []string
	if mp.Cmd != nil {
		args = append(args, mp.Cmd.Args[1:]...)
	}
	return mp.Status, mp.PID, args
}

func waitStatus(t *testing.T, pm *process.ProcessManager, key, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		st, _, _ := procState(pm, key)
		return st == want
	}, 5*time.Second, 5*time.Millisecond, "status of %s never became %s", key, want)
}

func alivePID(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func TestProcessRestarterRestartWithArgs(t *testing.T) {
	tests := []struct {
		name    string
		started bool
	}{
		{"running process gets new args", true},
		{"stopped process is started with new args", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := sleeperConfig("sd")
			pm := newSleeperPM(t, cfg)
			oldPID := 0
			if tt.started {
				require.NoError(t, pm.Start("sd"))
				_, oldPID, _ = procState(pm, "sd")
			}
			r := &processRestarter{pm: pm, cfg: cfg, key: "sd"}

			require.NoError(t, r.RestartWithArgs([]string{"31"}))

			pc, _ := cfg.Process("sd")
			assert.Equal(t, []string{"31"}, pc.Args, "config keeps the new args")
			st, pid, args := procState(pm, "sd")
			assert.True(t, process.IsAlive(st), "status %s", st)
			assert.True(t, alivePID(pid))
			assert.NotEqual(t, oldPID, pid)
			assert.Equal(t, []string{"31"}, args)
			if tt.started {
				assert.False(t, alivePID(oldPID), "old instance must be gone")
			}
			assert.True(t, r.Alive())
		})
	}
}

func TestProcessRestarterUnknownKey(t *testing.T) {
	cfg := sleeperConfig("sd")
	pm := newSleeperPM(t, cfg)
	r := &processRestarter{pm: pm, cfg: cfg, key: "missing"}

	assert.Error(t, r.RestartWithArgs([]string{"31"}))
	st, _, _ := procState(pm, "sd")
	assert.Equal(t, process.StatusStopped, st, "other processes untouched")
	assert.False(t, r.Alive())
}

func TestProcessRestarterAlive(t *testing.T) {
	cfg := sleeperConfig("sd")
	pm := newSleeperPM(t, cfg)
	r := &processRestarter{pm: pm, cfg: cfg, key: "sd"}

	assert.False(t, r.Alive(), "never started")
	require.NoError(t, pm.Start("sd"))
	assert.True(t, r.Alive(), "started")
	require.NoError(t, pm.Stop("sd"))
	assert.False(t, r.Alive(), "stopped")
}

func TestLlamaReloaderReload(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(healthy.Close)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(broken.Close)

	tests := []struct {
		name        string
		started     bool
		health      string
		wantErr     bool
		wantRestart bool
	}{
		{"stopped server is left alone", false, healthy.URL, false, false},
		{"running server restarted and healthy", true, healthy.URL, false, true},
		{"running server never healthy", true, broken.URL, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := newSleeperPM(t, sleeperConfig("llm"))
			oldPID := 0
			if tt.started {
				require.NoError(t, pm.Start("llm"))
				_, oldPID, _ = procState(pm, "llm")
			}
			r := &llamaReloader{pm: pm, key: "llm", health: tt.health, wait: 100 * time.Millisecond}

			err := r.Reload(context.Background())

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			st, pid, _ := procState(pm, "llm")
			if tt.wantRestart {
				assert.NotEqual(t, oldPID, pid)
				assert.True(t, alivePID(pid))
				assert.False(t, alivePID(oldPID))
			} else {
				assert.Equal(t, process.StatusStopped, st)
				assert.Zero(t, pid)
			}
		})
	}
}

func TestLlamaReloaderWithoutManager(t *testing.T) {
	r := &llamaReloader{key: "llm"}
	assert.NoError(t, r.Reload(context.Background()))
}

func TestApplyLlamaEnv(t *testing.T) {
	key := config.LlamaCppProcessKey
	tests := []struct {
		name    string
		procs   map[string]config.ProcessConfig
		wantLib bool
	}{
		{"no llama process", map[string]config.ProcessConfig{}, false},
		{"llama without proxy path", map[string]config.ProcessConfig{
			key: {Binary: "/opt/llama/llama-server", Env: map[string]string{"A": "1"}},
		}, false},
		{"llama behind proxy", map[string]config.ProcessConfig{
			key: {Binary: "/opt/llama/llama-server", ProxyPath: "/api/llm/", Env: map[string]string{"A": "1"}},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{DataDir: t.TempDir(), Processes: tt.procs}

			applyLlamaEnv(cfg)

			pc, ok := cfg.Process(key)
			if len(tt.procs) == 0 {
				assert.False(t, ok, "must not create the llama process")
				return
			}
			require.True(t, ok)
			assert.Equal(t, "1", pc.Env["A"], "existing env kept")
			lib := pc.Env["LD_LIBRARY_PATH"]
			if tt.wantLib {
				assert.True(t, strings.HasPrefix(lib, "/opt/llama"), "LD_LIBRARY_PATH %q must start with the binary dir", lib)
			} else {
				assert.Empty(t, lib)
			}
		})
	}
}

func newProcessHandlers(t *testing.T) (*http.ServeMux, *process.ProcessManager) {
	t.Helper()
	cfg := sleeperConfig("svc")
	pm := newSleeperPM(t, cfg)
	inst := installer.NewInstaller(cfg)
	h := NewHandlers(pm, sdhealth.NewHealthMonitor(cfg), sdgpu.NewGPUMonitor(), cfg, inst)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, pm
}

func serve(mux http.Handler, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		body = nil
	}
	return rec, body
}

func TestProcessHandlersRejectBadRequests(t *testing.T) {
	mux, pm := newProcessHandlers(t)
	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/server/start/svc", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/server/stop/svc", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/server/restart/svc", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/server/logs/svc", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/server/start/", http.StatusBadRequest},
		{http.MethodPost, "/api/server/stop/", http.StatusBadRequest},
		{http.MethodPost, "/api/server/restart/", http.StatusBadRequest},
		{http.MethodGet, "/api/server/logs/", http.StatusBadRequest},
		{http.MethodPost, "/api/server/start/nope", http.StatusInternalServerError},
		{http.MethodPost, "/api/server/stop/nope", http.StatusInternalServerError},
		{http.MethodPost, "/api/server/restart/nope", http.StatusInternalServerError},
		{http.MethodGet, "/api/server/logs/nope", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec, _ := serve(mux, tt.method, tt.path)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
	st, _, _ := procState(pm, "svc")
	assert.Equal(t, process.StatusStopped, st, "rejected requests must not start anything")
}

func TestProcessHandlersLifecycle(t *testing.T) {
	mux, pm := newProcessHandlers(t)

	rec, body := serve(mux, http.MethodPost, "/api/server/start/svc")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "started", body["status"])
	_, firstPID, _ := procState(pm, "svc")
	assert.True(t, alivePID(firstPID))

	rec, body = serve(mux, http.MethodPost, "/api/server/start/svc")
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "second start of a live process")
	assert.Contains(t, body["error"], "already")

	rec, body = serve(mux, http.MethodPost, "/api/server/restart/svc")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "restarted", body["status"])
	_, secondPID, _ := procState(pm, "svc")
	assert.NotEqual(t, firstPID, secondPID)
	assert.False(t, alivePID(firstPID))

	rec, body = serve(mux, http.MethodPost, "/api/server/stop/svc")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "stopped", body["status"])
	assert.False(t, alivePID(secondPID))
	waitStatus(t, pm, "svc", process.StatusStopped)
}

func TestProcessHandlersLogsLines(t *testing.T) {
	mux, pm := newProcessHandlers(t)
	require.NoError(t, pm.Start("svc"))
	require.NoError(t, pm.Restart("svc"))

	tests := []struct {
		query     string
		wantLines float64
		maxLogs   int
	}{
		{"", 50, 50},
		{"?lines=1", 1, 1},
		{"?lines=0", 50, 50},
		{"?lines=abc", 50, 50},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rec, body := serve(mux, http.MethodGet, "/api/server/logs/svc/"+tt.query)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "svc", body["process"])
			assert.Equal(t, tt.wantLines, body["lines"])
			logs, _ := body["logs"].([]any)
			assert.NotEmpty(t, logs)
			assert.LessOrEqual(t, len(logs), tt.maxLogs)
		})
	}
}

type markerHandler string

func (m markerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Route", string(m))
}

func TestSetupMuxRoutes(t *testing.T) {
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.Processes = map[string]config.ProcessConfig{"svc": {Name: "svc", Binary: "/bin/sleep", Args: []string{"30"}}}
	pm := newSleeperPM(t, &cfg)
	inst := installer.NewInstaller(&cfg)
	h := NewHandlers(pm, sdhealth.NewHealthMonitor(&cfg), sdgpu.NewGPUMonitor(), &cfg, inst)
	proxy := NewProxyHandler(nil, &cfg, nil)
	proxy.SetHandler("/api/sd/", markerHandler("sd"))
	proxy.SetHandler("/api/llm/", markerHandler("llm"))
	mux := setupMux(h, inst, models.NewModelManager(&cfg), NewBackendManager(&cfg), proxy)

	tests := []struct {
		name   string
		method string
		path   string
		route  string
	}{
		{"process logs", http.MethodGet, "/api/server/logs/svc", "json"},
		{"process start", http.MethodGet, "/api/server/start/svc", "method"},
		{"models list", http.MethodGet, "/api/server/models/sd", "json"},
		{"models delete", http.MethodGet, "/api/server/models/delete/sd/x", "notdefault"},
		{"backends", http.MethodGet, "/api/server/backends/active", "json"},
		{"installer", http.MethodGet, "/api/server/install/status", "json"},
		{"sd proxy", http.MethodGet, "/api/sd/sdapi/v1/txt2img", "sd"},
		{"llm proxy", http.MethodGet, "/api/llm/v1/models", "llm"},
		{"root", http.MethodGet, "/", "json"},
		{"unknown", http.MethodGet, "/nope", "default404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			isDefault404 := rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), "404 page not found")
			switch tt.route {
			case "json":
				assert.Equal(t, "application/json", rec.Header().Get("Content-Type"), "body %s", rec.Body.String())
			case "method":
				assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
			case "notdefault":
				assert.False(t, isDefault404, "fell through to the root handler")
			case "default404":
				assert.True(t, isDefault404, "code %d body %s", rec.Code, rec.Body.String())
			default:
				assert.Equal(t, tt.route, rec.Header().Get("X-Route"))
			}
		})
	}
}
