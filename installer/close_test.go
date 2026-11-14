package installer

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

func TestCloseCancelsRunningDownloadAndWaits(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(1<<20))
		w.WriteHeader(http.StatusOK)
		w.Write(make([]byte, 1024))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	cfg := rtConfig(t, "v1")
	arts := []Artifact{{Name: "sd", URL: srv.URL + "/sd.zip", SHA256: rtSum([]byte("x")), Size: 1 << 20, Kind: "zip", Dest: "engines/sd-cpp", Strip: 1}}
	inst := rtInstaller(cfg, arts)

	done := make(chan error, 1)
	go func() { done <- inst.Install(config.SDCppBackendKey) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}

	closed := make(chan struct{})
	start := time.Now()
	go func() {
		inst.Close()
		close(closed)
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Install was not cancelled by Close")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return")
	}
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestInstallAfterCloseFailsWithoutNetwork(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := rtConfig(t, "v1")
	arts := []Artifact{{Name: "sd", URL: srv.URL + "/sd.zip", SHA256: rtSum([]byte("x")), Size: 1, Kind: "zip", Dest: "engines/sd-cpp", Strip: 1}}
	inst := rtInstaller(cfg, arts)
	inst.Close()

	require.Error(t, inst.Install(config.SDCppBackendKey))
	assert.Equal(t, 0, hits)
}
