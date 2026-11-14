package installer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStallTimer struct {
	mu      sync.Mutex
	fire    func()
	resets  []time.Duration
	stopped bool
}

func (f *fakeStallTimer) Reset(d time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resets = append(f.resets, d)
	return true
}

func (f *fakeStallTimer) Stop() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return true
}

func (f *fakeStallTimer) resetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resets)
}

func useFakeStallTimer(t *testing.T) chan *fakeStallTimer {
	t.Helper()
	created := make(chan *fakeStallTimer, 1)
	old := newStallTimer
	newStallTimer = func(d time.Duration, f func()) stallTimer {
		assert.Equal(t, downloadStall, d)
		ft := &fakeStallTimer{fire: f}
		created <- ft
		return ft
	}
	t.Cleanup(func() { newStallTimer = old })
	return created
}

func TestHTTPGetFailsOnStalledBody(t *testing.T) {
	created := useFakeStallTimer(t)
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
	timer := <-created

	buf := make([]byte, 4)
	_, err = io.ReadFull(resp.Body, buf)
	require.NoError(t, err)
	assert.Equal(t, "part", string(buf))

	timer.fire()
	_, err = io.ReadAll(resp.Body)
	assert.ErrorIs(t, err, errStalled)
}

func TestHTTPGetSlowButSteadyBodyCompletes(t *testing.T) {
	created := useFakeStallTimer(t)
	next := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for i := 0; i < 5; i++ {
			select {
			case <-next:
			case <-r.Context().Done():
				return
			}
			w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)

	resp, err := HTTPGet(srv.URL)
	require.NoError(t, err)
	timer := <-created

	var got []byte
	buf := make([]byte, 5)
	for i := 0; i < 5; i++ {
		next <- struct{}{}
		_, err := io.ReadFull(resp.Body, buf)
		require.NoError(t, err)
		got = append(got, buf...)
		assert.GreaterOrEqual(t, timer.resetCount(), i+1, "chunk %d must restart the stall countdown", i+1)
	}
	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, "chunkchunkchunkchunkchunk", string(got))

	require.NoError(t, resp.Body.Close())
	timer.mu.Lock()
	defer timer.mu.Unlock()
	assert.True(t, timer.stopped)
	for _, d := range timer.resets {
		assert.Equal(t, downloadStall, d)
	}
}

func TestHTTPGetRealTimerAbortsStall(t *testing.T) {
	old := downloadStall
	downloadStall = 50 * time.Millisecond
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
	case <-time.After(10 * time.Second):
		t.Fatal("stalled download was not aborted")
	}
}
