package probe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbe(t *testing.T) {
	t.Parallel()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(ok.Close)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(bad.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	_, err := Probe(context.Background(), ok.URL)
	require.NoError(t, err)

	_, err = Probe(context.Background(), bad.URL)
	var se *StatusError
	require.True(t, errors.As(err, &se))
	assert.Equal(t, http.StatusServiceUnavailable, se.Code)
	assert.Equal(t, "status 503", err.Error())

	_, err = Probe(context.Background(), closedURL)
	require.Error(t, err)
	assert.False(t, errors.As(err, &se))

	_, err = Probe(context.Background(), "://bad")
	require.Error(t, err)
}

func TestProbeTimeoutOnHungServer(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		hung.Close()
	})
	start := time.Now()
	_, err := ProbeTimeout(hung.URL, 100*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
}
