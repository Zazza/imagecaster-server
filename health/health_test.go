package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

func statusServer(t *testing.T, code int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func closedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

func TestHealthMonitorStartChecksEveryProcessWithHealthURL(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{
		"ok":     {HealthURL: statusServer(t, http.StatusOK)},
		"broken": {HealthURL: statusServer(t, http.StatusServiceUnavailable)},
		"down":   {HealthURL: closedURL(t)},
		"silent": {},
	}}
	hm := NewHealthMonitor(cfg)
	updates := make(chan map[string]HealthResult, 16)
	hm.OnUpdate = func(r map[string]HealthResult) { updates <- r }

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		hm.Start(ctx)
		close(stopped)
	}()

	var last map[string]HealthResult
	timeout := time.After(10 * time.Second)
	for len(last) < 3 {
		select {
		case last = <-updates:
		case <-timeout:
			t.Fatalf("got %d of 3 health results", len(last))
		}
	}
	cancel()
	<-stopped

	got := hm.Results()
	require.Len(t, got, 3)
	assert.NotContains(t, got, "silent")

	assert.True(t, got["ok"].Healthy)
	assert.Empty(t, got["ok"].Error)
	assert.False(t, got["ok"].CheckedAt.IsZero())

	assert.False(t, got["broken"].Healthy)
	assert.Equal(t, "status 503", got["broken"].Error)

	assert.False(t, got["down"].Healthy)
	assert.NotEmpty(t, got["down"].Error)
}

func TestHealthMonitorCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		url         string
		wantHealthy bool
		wantErr     string
	}{
		{name: "200", url: statusServer(t, http.StatusOK), wantHealthy: true},
		{name: "404", url: statusServer(t, http.StatusNotFound), wantErr: "status 404"},
		{name: "500", url: statusServer(t, http.StatusInternalServerError), wantErr: "status 500"},
		{name: "connection refused", url: closedURL(t), wantErr: "connect"},
		{name: "bad url", url: "http://bad host/", wantErr: "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hm := NewHealthMonitor(&config.Config{})
			hm.check("p", tt.url)
			got := hm.Results()["p"]
			assert.Equal(t, tt.wantHealthy, got.Healthy)
			if tt.wantErr == "" {
				assert.Empty(t, got.Error)
			} else {
				assert.Contains(t, got.Error, tt.wantErr)
			}
		})
	}
}

func TestHealthMonitorResultFollowsLatestCheck(t *testing.T) {
	t.Parallel()
	var code atomic.Int32
	code.Store(http.StatusServiceUnavailable)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(code.Load()))
	}))
	defer srv.Close()
	hm := NewHealthMonitor(&config.Config{})

	hm.check("sd", srv.URL)
	assert.False(t, hm.Results()["sd"].Healthy)

	code.Store(http.StatusOK)
	hm.check("sd", srv.URL)
	assert.True(t, hm.Results()["sd"].Healthy)
	assert.Empty(t, hm.Results()["sd"].Error)
}

func TestHealthMonitorResultsIsACopy(t *testing.T) {
	t.Parallel()
	hm := NewHealthMonitor(&config.Config{})
	hm.check("sd", statusServer(t, http.StatusOK))

	r := hm.Results()
	delete(r, "sd")

	assert.Contains(t, hm.Results(), "sd")
}
