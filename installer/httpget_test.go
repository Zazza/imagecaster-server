package installer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPGetFailsOnStalledBody(t *testing.T) {
	old := downloadStall
	downloadStall = 200 * time.Millisecond
	t.Cleanup(func() { downloadStall = old })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("part"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	resp, err := HTTPGet(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(resp.Body); done <- err }()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, errStalled)
	case <-time.After(5 * time.Second):
		t.Fatal("stalled download was not aborted")
	}
}

func TestHTTPGetSlowButSteadyBodyCompletes(t *testing.T) {
	old := downloadStall
	downloadStall = 300 * time.Millisecond
	t.Cleanup(func() { downloadStall = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 5; i++ {
			w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	resp, err := HTTPGet(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)

	require.NoError(t, err)
	assert.Equal(t, 25, len(b))
}
