package dl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type origin struct {
	mu      sync.Mutex
	content []byte
	etag    string
	offset  int
	ranges  []string
	cutAt   int
}

func (o *origin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	o.ranges = append(o.ranges, r.Header.Get("Range"))
	etag, offset, cut := o.etag, o.offset, o.cutAt
	o.cutAt = 0
	o.mu.Unlock()
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if offset != 0 && r.Header.Get("Range") != "" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, len(o.content)-1, len(o.content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(o.content[offset:])
		return
	}
	if cut > 0 {
		w.Header().Set("Content-Length", fmt.Sprint(len(o.content)))
		w.Write(o.content[:cut])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(o.content))
}

func (o *origin) gotRanges() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.ranges...)
}

func content(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 13)
	}
	return b
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func serve(t *testing.T, o *origin) string {
	t.Helper()
	srv := httptest.NewServer(o)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestResumeContinuesAfterCut(t *testing.T) {
	data := content(64 << 10)
	o := &origin{content: data, cutAt: 10000}
	url := serve(t, o)
	part := filepath.Join(t.TempDir(), "f.part")

	err := Resume(context.Background(), url, part, Options{SHA256: sum(data)})
	require.Error(t, err)
	assert.FileExists(t, part)

	var last int64
	require.NoError(t, Resume(context.Background(), url, part, Options{SHA256: sum(data), OnProgress: func(d, _ int64) { last = d }}))
	assert.Equal(t, int64(len(data)), last)
	assert.Equal(t, "bytes=10000-", o.gotRanges()[1])
	got, err := os.ReadFile(part)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestResumeWrongOffsetDiscardsPart(t *testing.T) {
	data := content(4096)
	o := &origin{content: data, offset: 100}
	part := filepath.Join(t.TempDir(), "f.part")
	require.NoError(t, os.WriteFile(part, data[:1000], 0o644))

	err := Resume(context.Background(), serve(t, o), part, Options{})
	require.ErrorContains(t, err, "wrong offset")
	assert.NoFileExists(t, part)
}

func TestResumeRestartsWhenSourceChanged(t *testing.T) {
	data := content(4096)
	o := &origin{content: data, etag: `"v1"`, cutAt: 1000}
	url := serve(t, o)
	part := filepath.Join(t.TempDir(), "f.part")
	require.Error(t, Resume(context.Background(), url, part, Options{}))
	assert.FileExists(t, part+".meta")

	o.mu.Lock()
	o.etag = `"v2"`
	o.mu.Unlock()
	var statuses []string
	require.NoError(t, Resume(context.Background(), url, part, Options{SHA256: sum(data), OnStatus: func(s string) { statuses = append(statuses, s) }}))
	assert.Contains(t, statuses, "source file changed, downloading from the start")
	r := o.gotRanges()
	assert.Equal(t, "bytes=1000-", r[1])
	assert.Equal(t, "", r[2])

	require.NoError(t, Commit(part, filepath.Join(filepath.Dir(part), "f")))
	assert.NoFileExists(t, part+".meta")
}

func TestResumeCompletePartSkipsRequestWithSize(t *testing.T) {
	data := content(2048)
	o := &origin{content: data}
	part := filepath.Join(t.TempDir(), "f.part")
	require.NoError(t, os.WriteFile(part, data, 0o644))
	require.NoError(t, Resume(context.Background(), serve(t, o), part, Options{SHA256: sum(data), Size: int64(len(data))}))
	assert.Empty(t, o.gotRanges())
}

func TestResumeCorruptCompletePartIsRedownloaded(t *testing.T) {
	data := content(2048)
	bad := append([]byte(nil), data...)
	bad[10] ^= 0xff
	o := &origin{content: data}
	part := filepath.Join(t.TempDir(), "f.part")
	require.NoError(t, os.WriteFile(part, bad, 0o644))
	require.NoError(t, Resume(context.Background(), serve(t, o), part, Options{SHA256: sum(data), Size: int64(len(data))}))
	assert.Equal(t, []string{""}, o.gotRanges())
}

func TestResumeChecksumMismatchRemovesPart(t *testing.T) {
	data := content(2048)
	part := filepath.Join(t.TempDir(), "f.part")
	err := Resume(context.Background(), serve(t, &origin{content: data}), part, Options{SHA256: sum([]byte("other"))})
	var ce *ChecksumError
	require.True(t, errors.As(err, &ce))
	assert.ErrorIs(t, err, ErrChecksum)
	assert.Equal(t, sum(data), ce.Got)
	assert.NoFileExists(t, part)
}

func TestResumeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	err := Resume(context.Background(), srv.URL, filepath.Join(t.TempDir(), "f.part"), Options{})
	var se *StatusError
	require.True(t, errors.As(err, &se))
	assert.Equal(t, http.StatusNotFound, se.Code)
}

func TestResumeStallAborts(t *testing.T) {
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
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	err := Resume(context.Background(), srv.URL, filepath.Join(t.TempDir(), "f.part"), Options{Stall: 50 * time.Millisecond})
	require.ErrorIs(t, err, ErrStalled)
}
