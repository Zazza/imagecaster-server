package main

import (
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
