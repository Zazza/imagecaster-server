package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
	"imagecaster-server/installer"
	"imagecaster-server/process"
)

func TestNewServerTimeouts(t *testing.T) {
	d := &appDeps{cfg: &config.Config{Port: 7777}}
	srv := d.newServer()
	assert.Equal(t, ":7777", srv.Addr)
	assert.Equal(t, 30*time.Second, srv.ReadHeaderTimeout)
	assert.Equal(t, 120*time.Second, srv.IdleTimeout)
	assert.Zero(t, srv.ReadTimeout, "SSE and long jobs need no read timeout")
	assert.Zero(t, srv.WriteTimeout, "SSE and long jobs need no write timeout")
}

func TestShutdownConcurrentCallsRunOnce(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir(), Processes: map[string]config.ProcessConfig{"svc": {Name: "svc", Binary: "sleep", Args: []string{"30"}}}}
	d := &appDeps{cfg: cfg, inst: installer.NewInstaller(cfg), pm: process.NewProcessManager(cfg, nil, cfg.DataDir)}

	var wg sync.WaitGroup
	done := make(chan struct{})
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.shutdown()
		}()
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not return")
	}
	require.Error(t, d.inst.Install("sd-cpp"))
	assert.ErrorIs(t, d.pm.Start("svc"), process.ErrClosed)
}

func TestAppRuntimeStopRunsOnceAndClosesServer(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir()}
	d := &appDeps{cfg: cfg, inst: installer.NewInstaller(cfg), pm: process.NewProcessManager(cfg, nil, cfg.DataDir)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	r := &appRuntime{d: d, ctx: ctx, cancel: cancel, srv: d.newServer(), mdns: NewMDNS(0, false, "test"), grace: time.Second, serveCh: make(chan error, 1)}
	served := make(chan error, 1)
	go func() { served <- r.srv.Serve(ln) }()

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Stop()
		}()
	}
	wg.Wait()
	assert.Error(t, r.ctx.Err())
	select {
	case err := <-served:
		assert.ErrorIs(t, err, http.ErrServerClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("server still serving after Stop")
	}
}

func TestStopBackendsFirstDoesNotWaitForLongRequests(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir(), Processes: map[string]config.ProcessConfig{"svc": {Name: "svc", Binary: "sleep", Args: []string{"30"}}}}
	entered := make(chan struct{})
	release := make(chan struct{})
	d := &appDeps{
		cfg:  cfg,
		inst: installer.NewInstaller(cfg),
		pm:   process.NewProcessManager(cfg, nil, cfg.DataDir),
		mux: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	grace := 3 * time.Second
	r := &appRuntime{d: d, ctx: ctx, cancel: cancel, srv: d.newServer(), mdns: NewMDNS(0, false, "test"), grace: grace, serveCh: make(chan error, 1)}
	go r.srv.Serve(ln)
	t.Cleanup(func() { close(release) })

	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/long")
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-entered

	start := time.Now()
	stopped := make(chan struct{})
	go func() {
		r.stopBackendsFirst()
		close(stopped)
	}()
	require.Eventually(t, func() bool {
		return errors.Is(d.pm.Start("svc"), process.ErrClosed)
	}, time.Second, 10*time.Millisecond, "backends must be shut down before the HTTP grace runs out")
	assert.Less(t, time.Since(start), grace)
	assert.Error(t, r.ctx.Err())
	select {
	case <-stopped:
		t.Fatal("HTTP shutdown must still be waiting for the long request")
	default:
	}
}
