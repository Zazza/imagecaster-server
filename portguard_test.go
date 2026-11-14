package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"imagecaster-server/config"
	"imagecaster-server/installer"
	"imagecaster-server/process"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeWarner struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeWarner) Warn(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
}

func (f *fakeWarner) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.msgs...)
}

func holdAddr(t *testing.T, held map[string]net.Listener) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	held[addr] = l
	return addr
}

type openPorts map[string]net.Listener

func (o openPorts) open(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	o[":"+strconv.Itoa(port)] = l
	t.Cleanup(func() {
		if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close %d: %v", port, err)
		}
	})
	return port
}

func (o openPorts) listen(addr string) (net.Listener, error) {
	if l, ok := o[addr]; ok {
		delete(o, addr)
		return l, nil
	}
	return listenIfFree(addr)
}

const (
	internalPortA = 1
	internalPortB = 2
)

func noKeepAliveClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
}

type seenRequest struct {
	method  string
	path    string
	rawPath string
	query   string
	body    string
	header  string
	host    string
}

func recordingHandler(ch chan<- seenRequest) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- seenRequest{
			method:  r.Method,
			path:    r.URL.Path,
			rawPath: r.URL.RawPath,
			query:   r.URL.RawQuery,
			body:    string(b),
			header:  r.Header.Get("X-Test"),
			host:    r.Host,
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestGuardHandler_PrefixesPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		proxyPath string
		method    string
		target    string
		body      string
		wantPath  string
		wantQuery string
	}{
		{"sd post with query", "/api/sd/", http.MethodPost, "/jobs?a=1&b=two", `{"p":1}`, "/api/sd/jobs", "a=1&b=two"},
		{"llm get", "/api/llm/", http.MethodGet, "/api/tags", "", "/api/llm/api/tags", ""},
		{"proxy path without trailing slash", "/api/sd", http.MethodGet, "/state", "", "/api/sd/state", ""},
		{"root", "/api/sd/", http.MethodGet, "/", "", "/api/sd/", ""},
		{"delete", "/api/llm/", http.MethodDelete, "/api/delete", `{"name":"m"}`, "/api/llm/api/delete", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ch := make(chan seenRequest, 1)
			h := guardHandler(tt.proxyPath, recordingHandler(ch))

			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, "http://guard.local"+tt.target, body)
			req.Header.Set("X-Test", "keep")
			origPath := req.URL.Path
			origQuery := req.URL.RawQuery
			origURL := req.URL.String()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)

			got := <-ch
			assert.Equal(t, tt.method, got.method)
			assert.Equal(t, tt.wantPath, got.path)
			assert.Equal(t, tt.wantQuery, got.query)
			assert.Equal(t, tt.body, got.body)
			assert.Equal(t, "keep", got.header)

			assert.Equal(t, origPath, req.URL.Path)
			assert.Equal(t, origQuery, req.URL.RawQuery)
			assert.Equal(t, origURL, req.URL.String())
		})
	}
}

func TestGuardHandler_PrefixesRawPath(t *testing.T) {
	t.Parallel()
	ch := make(chan seenRequest, 1)
	h := guardHandler("/api/sd/", recordingHandler(ch))

	req := httptest.NewRequest(http.MethodGet, "http://guard.local/a%2Fb?x=1", nil)
	require.Equal(t, "/a%2Fb", req.URL.RawPath)

	h.ServeHTTP(httptest.NewRecorder(), req)

	got := <-ch
	assert.Equal(t, "/api/sd/a/b", got.path)
	assert.Equal(t, "/api/sd/a%2Fb", got.rawPath)
	assert.Equal(t, "x=1", got.query)
	assert.Equal(t, "/a%2Fb", req.URL.RawPath)
	assert.Equal(t, "/a/b", req.URL.Path)
}

func TestStartPortGuards_ServesPrefixedPaths(t *testing.T) {
	t.Parallel()
	held := map[string]net.Listener{}
	sdAddr := holdAddr(t, held)
	llmAddr := holdAddr(t, held)
	ch := make(chan seenRequest, 4)
	w := &fakeWarner{}

	pg := startHeldPortGuards([]config.PortGuard{
		{Process: "sd", ListenAddr: sdAddr, ProxyPath: "/api/sd/"},
		{Process: "ollama", ListenAddr: llmAddr, ProxyPath: "/api/llm/"},
	}, held, recordingHandler(ch), w)
	require.NotNil(t, pg)
	t.Cleanup(func() { pg.shutdown(context.Background()) })

	client := noKeepAliveClient()
	resp, err := client.Post("http://"+sdAddr+"/jobs?q=1", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	got := <-ch
	assert.Equal(t, "/api/sd/jobs", got.path)
	assert.Equal(t, "q=1", got.query)
	assert.Equal(t, http.MethodPost, got.method)

	resp, err = client.Get("http://" + llmAddr + "/api/tags")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	got = <-ch
	assert.Equal(t, "/api/llm/api/tags", got.path)

	assert.Empty(t, w.messages())
}

func TestStartPortGuards_AddressInUse(t *testing.T) {
	t.Parallel()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { busy.Close() })
	busyAddr := busy.Addr().String()
	held := map[string]net.Listener{}
	freeA := holdAddr(t, held)

	ch := make(chan seenRequest, 2)
	w := &fakeWarner{}
	var pg *portGuards
	require.NotPanics(t, func() {
		pg = startHeldPortGuards([]config.PortGuard{
			{Process: "ollama", ListenAddr: busyAddr, ProxyPath: "/api/llm/"},
			{Process: "sd", ListenAddr: freeA, ProxyPath: "/api/sd/"},
		}, held, recordingHandler(ch), w)
	})
	t.Cleanup(func() { pg.shutdown(context.Background()) })

	resp, err := noKeepAliveClient().Get("http://" + freeA + "/state")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "/api/sd/state", (<-ch).path)

	msgs := w.messages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0], "ollama")
	assert.Contains(t, msgs[0], busyAddr)
}

func TestPortGuards_Shutdown(t *testing.T) {
	t.Parallel()
	held := map[string]net.Listener{}
	addr := holdAddr(t, held)
	ch := make(chan seenRequest, 2)
	pg := startHeldPortGuards([]config.PortGuard{
		{Process: "sd", ListenAddr: addr, ProxyPath: "/api/sd/"},
	}, held, recordingHandler(ch), &fakeWarner{})
	require.NotNil(t, pg)

	client := noKeepAliveClient()
	resp, err := client.Get("http://" + addr + "/x")
	require.NoError(t, err)
	resp.Body.Close()
	<-ch

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pg.shutdown(ctx)

	_, err = client.Get("http://" + addr + "/x")
	assert.Error(t, err)
}

func TestPortGuards_ShutdownNil(t *testing.T) {
	t.Parallel()
	var pg *portGuards
	assert.NotPanics(t, func() { pg.shutdown(context.Background()) })
}

type e2eEnv struct {
	sdAddr  string
	llmAddr string
	sdSeen  chan seenRequest
	llmSeen chan seenRequest
	sdHost  string
	llmHost string
	running chan int
	release chan struct{}
	proxy   http.Handler
}

func newPortGuardE2E(t *testing.T) (*e2eEnv, func() int) {
	t.Helper()
	gcfg := GateConfig{SDDefaultWeightMB: 3000, SDOverheadMB: 100, LLMWeightMB: 2000, MaxWaitSeconds: 1}
	gate, q := newTestGate(5000, 1, gcfg, "")

	env := &e2eEnv{
		sdSeen:  make(chan seenRequest, 8),
		llmSeen: make(chan seenRequest, 8),
		running: make(chan int, 8),
		release: make(chan struct{}),
	}

	sdBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.running <- len(q.Status().Running)
		if r.URL.Path == "/jobs" {
			<-env.release
		}
		env.sdSeen <- seenRequest{method: r.Method, path: r.URL.Path, host: r.Host}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sdBackend.Close)
	llmBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.llmSeen <- seenRequest{method: r.Method, path: r.URL.Path, host: r.Host}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(llmBackend.Close)
	env.sdHost = strings.TrimPrefix(sdBackend.URL, "http://")
	env.llmHost = strings.TrimPrefix(llmBackend.URL, "http://")

	cfg := &config.Config{
		Processes: map[string]config.ProcessConfig{
			"sd":     {Name: "Stable Diffusion", TargetURL: sdBackend.URL, ProxyPath: "/api/sd/"},
			"ollama": {Name: "Ollama", TargetURL: llmBackend.URL, ProxyPath: "/api/llm/"},
		},
	}
	pm := process.NewProcessManager(cfg, installer.NewInstaller(cfg), "")
	ph := NewProxyHandler(pm, cfg, gate)

	held := map[string]net.Listener{}
	env.sdAddr = holdAddr(t, held)
	env.llmAddr = holdAddr(t, held)
	w := &fakeWarner{}
	pg := startHeldPortGuards([]config.PortGuard{
		{Process: "ollama", ListenAddr: env.llmAddr, ProxyPath: "/api/llm/"},
		{Process: "sd", ListenAddr: env.sdAddr, ProxyPath: "/api/sd/"},
	}, held, ph, w)
	t.Cleanup(func() { pg.shutdown(context.Background()) })
	require.Empty(t, w.messages())
	env.proxy = ph
	return env, func() int { return len(q.Status().Running) }
}

func TestPortGuardE2E_SDJobSubmitTakesNoLease(t *testing.T) {
	env, running := newPortGuardE2E(t)
	client := noKeepAliveClient()

	done := make(chan *http.Response, 1)
	go func() {
		resp, err := client.Post("http://"+env.sdAddr+"/jobs", "application/json", strings.NewReader("{}"))
		if err != nil {
			done <- nil
			return
		}
		done <- resp
	}()

	select {
	case n := <-env.running:
		assert.Equal(t, 0, n, "job submit via guard port must not hold a GPU lease")
	case <-time.After(3 * time.Second):
		close(env.release)
		t.Fatal("txt2img did not reach backend")
	}
	assert.Equal(t, 0, running())
	close(env.release)

	select {
	case resp := <-done:
		require.NotNil(t, resp)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	case <-time.After(3 * time.Second):
		t.Fatal("txt2img did not complete")
	}
	got := <-env.sdSeen
	assert.Equal(t, "/jobs", got.path)
	assert.True(t, waitFor(t, time.Second, func() bool { return running() == 0 }))
}

func TestPortGuardE2E_LightSDNoLease(t *testing.T) {
	env, running := newPortGuardE2E(t)
	close(env.release)
	client := noKeepAliveClient()

	resp, err := client.Get("http://" + env.sdAddr + "/state")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, 0, <-env.running)
	assert.Equal(t, "/state", (<-env.sdSeen).path)
	assert.Equal(t, 0, running())
}

func TestPortGuardE2E_HostHeader(t *testing.T) {
	env, _ := newPortGuardE2E(t)
	close(env.release)
	client := noKeepAliveClient()

	req, err := http.NewRequest(http.MethodGet, "http://"+env.llmAddr+"/api/tags", nil)
	require.NoError(t, err)
	req.Host = "client.example:11434"
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	got := <-env.llmSeen
	assert.Equal(t, "/api/tags", got.path)
	assert.Equal(t, env.llmHost, got.host, "llm backend must receive its own host:port as Host")

	req, err = http.NewRequest(http.MethodGet, "http://"+env.sdAddr+"/state", nil)
	require.NoError(t, err)
	req.Host = "client.example:7860"
	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	<-env.running
	got = <-env.sdSeen
	assert.Equal(t, "client.example:7860", got.host, "sd backend must keep the client Host")
}

func TestProxyHandler_LLMHostViaServerPort(t *testing.T) {
	env, _ := newPortGuardE2E(t)
	close(env.release)
	srv := httptest.NewServer(env.proxy)
	t.Cleanup(srv.Close)
	client := noKeepAliveClient()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/llm/api/tags", nil)
	require.NoError(t, err)
	req.Host = "client.example:8080"
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	got := <-env.llmSeen
	assert.Equal(t, "/api/tags", got.path)
	assert.Equal(t, env.llmHost, got.host)

	req, err = http.NewRequest(http.MethodGet, srv.URL+"/api/sd/state", nil)
	require.NoError(t, err)
	req.Host = "client.example:8080"
	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	<-env.running
	assert.Equal(t, "client.example:8080", (<-env.sdSeen).host)
}

func TestApplyPortGuard_ProbesBusyPorts(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	t.Cleanup(func() { busy.Close() })
	busyPort := busy.Addr().(*net.TCPAddr).Port
	ports := openPorts{}
	freeP := ports.open(t)

	busyURL := "http://127.0.0.1:" + strconv.Itoa(busyPort)
	busyPC := config.ProcessConfig{
		Name: "Busy", AutoStart: true, TargetURL: busyURL, HealthURL: busyURL + "/h",
		ProxyPath: "/api/busy/", InternalPort: internalPortA,
	}
	cfg := &config.Config{
		GPU: config.GPUConfig{GuardPorts: true},
		Processes: map[string]config.ProcessConfig{
			"busysvc": busyPC,
			"freesvc": {
				Name: "Free", AutoStart: true, TargetURL: "http://127.0.0.1:" + strconv.Itoa(freeP),
				ProxyPath: "/api/free/", InternalPort: internalPortB,
			},
		},
	}

	guards, warnings, held := holdPortGuardsWith(cfg, ports.listen)

	require.Len(t, guards, 1)
	assert.Equal(t, "freesvc", guards[0].Process)
	assert.Equal(t, ":"+strconv.Itoa(freeP), guards[0].ListenAddr)
	assert.Equal(t, "/api/free/", guards[0].ProxyPath)

	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "busysvc")
	assert.Contains(t, warnings[0], ":"+strconv.Itoa(busyPort))

	assert.Equal(t, busyPC, cfg.Processes["busysvc"])
	assert.NotEqual(t, "http://127.0.0.1:"+strconv.Itoa(freeP), cfg.Processes["freesvc"].TargetURL)

	w := &fakeWarner{}
	ch := make(chan seenRequest, 1)
	pg := startHeldPortGuards(guards, held, recordingHandler(ch), w)
	require.NotNil(t, pg)
	t.Cleanup(func() { pg.shutdown(context.Background()) })

	resp, err := noKeepAliveClient().Get("http://127.0.0.1:" + strconv.Itoa(freeP) + "/x")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "/api/free/x", (<-ch).path)
	assert.Empty(t, w.messages())
}

func TestHoldPortGuards_LoopbackOnlyListenerCountsAsBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { busy.Close() })
	busyPort := busy.Addr().(*net.TCPAddr).Port
	url := "http://127.0.0.1:" + strconv.Itoa(busyPort)
	cfg := &config.Config{
		GPU: config.GPUConfig{GuardPorts: true},
		Processes: map[string]config.ProcessConfig{
			"ollama": {Name: "Ollama", AutoStart: true, TargetURL: url, ProxyPath: "/api/llm/", InternalPort: internalPortA},
		},
	}

	guards, warns, held := holdPortGuards(cfg)

	assert.Empty(t, guards)
	assert.Empty(t, held)
	require.Len(t, warns, 1)
	assert.Equal(t, url, cfg.Processes["ollama"].TargetURL)
}

func TestHoldPortGuards_KeepsPortUntilGuardStarts(t *testing.T) {
	ports := openPorts{}
	p := ports.open(t)
	url := "http://127.0.0.1:" + strconv.Itoa(p)
	cfg := &config.Config{
		GPU: config.GPUConfig{GuardPorts: true},
		Processes: map[string]config.ProcessConfig{
			"sd": {Name: "SD", AutoStart: true, TargetURL: url, ProxyPath: "/api/sd/", InternalPort: internalPortA},
		},
	}

	guards, _, held := holdPortGuardsWith(cfg, ports.listen)
	require.Len(t, guards, 1)
	_, err := net.Listen("tcp", guards[0].ListenAddr)
	require.Error(t, err, "port must stay taken between probe and guard start")

	ch := make(chan seenRequest, 1)
	pg := startHeldPortGuards(guards, held, recordingHandler(ch), &fakeWarner{})
	t.Cleanup(func() { pg.shutdown(context.Background()) })
	resp, err := noKeepAliveClient().Get(url + "/x")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "/api/sd/x", (<-ch).path)
}
