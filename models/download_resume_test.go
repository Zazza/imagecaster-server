package models

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type origin struct {
	content    []byte
	abortAfter int
	ignoreRng  bool
	block      chan struct{}
	mu         sync.Mutex
	ranges     []string
	hits       atomic.Int32
}

func (o *origin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := o.hits.Add(1)
	o.mu.Lock()
	o.ranges = append(o.ranges, r.Header.Get("Range"))
	o.mu.Unlock()
	if o.block != nil {
		<-o.block
	}
	if n == 1 && o.abortAfter > 0 {
		w.Header().Set("Content-Length", strconv.Itoa(len(o.content)))
		w.WriteHeader(http.StatusOK)
		w.Write(o.content[:o.abortAfter])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	if o.ignoreRng {
		w.Header().Set("Content-Length", strconv.Itoa(len(o.content)))
		w.Write(o.content)
		return
	}
	http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(o.content))
}

func (o *origin) rangeHeaders() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.ranges...)
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type sseResult struct {
	done     bool
	err      string
	progress []map[string]float64
}

func parseSSE(t *testing.T, body string) sseResult {
	t.Helper()
	var res sseResult
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok {
			continue
		}
		switch {
		case data == "[DONE]":
			res.done = true
		case strings.HasPrefix(data, "[ERROR]"):
			res.err = strings.TrimSpace(strings.TrimPrefix(data, "[ERROR]"))
		default:
			var p map[string]float64
			if json.Unmarshal([]byte(data), &p) == nil {
				res.progress = append(res.progress, p)
			}
		}
	}
	return res
}

func streamDownload(t *testing.T, mm *ModelManager, src, filename, sha string) sseResult {
	t.Helper()
	return streamDownloadCtx(t, context.Background(), mm, src, filename, sha)
}

func streamDownloadCtx(t *testing.T, ctx context.Context, mm *ModelManager, src, filename, sha string) sseResult {
	t.Helper()
	q := url.Values{"type": {"sd"}, "url": {src}, "filename": {filename}}
	if sha != "" {
		q.Set("sha256", sha)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/server/models/download/stream?"+q.Encode(), nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	mm.handleDownloadStream(rec, req)
	if rec.Code != http.StatusOK {
		return sseResult{err: fmt.Sprintf("HTTP %d: %s", rec.Code, rec.Body.String())}
	}
	return parseSSE(t, rec.Body.String())
}

func TestDownloadResumeAfterBrokenConnection(t *testing.T) {
	t.Parallel()
	content := payload(3 << 20)
	o := &origin{content: content, abortAfter: 1 << 20}
	srv := httptest.NewServer(o)
	defer srv.Close()
	dir := t.TempDir()
	mm := newTestModelManager(t, dir)
	dest := filepath.Join(dir, "m.safetensors")

	first := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", "")
	assert.False(t, first.done)
	assert.NotEmpty(t, first.err)
	assert.NoFileExists(t, dest)
	st, err := os.Stat(dest + ".part")
	require.NoError(t, err, ".part must survive a broken connection")
	have := st.Size()
	require.Greater(t, have, int64(0))

	second := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", sum(content))
	require.True(t, second.done, second.err)
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, sum(content), sum(got))
	assert.NoFileExists(t, dest+".part")

	ranges := o.rangeHeaders()
	require.Len(t, ranges, 2)
	assert.Equal(t, "", ranges[0])
	assert.Equal(t, fmt.Sprintf("bytes=%d-", have), ranges[1])

	require.NotEmpty(t, second.progress)
	assert.GreaterOrEqual(t, second.progress[0]["downloaded"], float64(have))
	for _, p := range second.progress {
		assert.Equal(t, float64(len(content)), p["total"])
		assert.LessOrEqual(t, p["percent"], float64(100))
	}
	assert.Equal(t, float64(100), second.progress[len(second.progress)-1]["percent"])
}

func TestDownloadRestartsWhenOriginIgnoresRange(t *testing.T) {
	t.Parallel()
	content := payload(2 << 20)
	o := &origin{content: content, ignoreRng: true}
	srv := httptest.NewServer(o)
	defer srv.Close()
	dir := t.TempDir()
	mm := newTestModelManager(t, dir)
	dest := filepath.Join(dir, "m.safetensors")
	require.NoError(t, os.WriteFile(dest+".part", content[:4096], 0o644))

	res := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", "")
	require.True(t, res.done, res.err)
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, len(content), len(got))
	assert.Equal(t, sum(content), sum(got))
	assert.Equal(t, "bytes=4096-", o.rangeHeaders()[0])
}

func TestDownloadSHA256(t *testing.T) {
	t.Parallel()
	content := payload(256 << 10)
	tests := []struct {
		name     string
		sha      string
		wantDone bool
		wantErr  string
		wantFile bool
		wantHits int32
	}{
		{name: "match", sha: sum(content), wantDone: true, wantFile: true, wantHits: 1},
		{name: "match uppercase", sha: strings.ToUpper(sum(content)), wantDone: true, wantFile: true, wantHits: 1},
		{name: "mismatch", sha: strings.Repeat("0", 64), wantErr: "sha256 mismatch", wantHits: 1},
		{name: "bad format", sha: "abc", wantErr: "HTTP 400", wantHits: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &origin{content: content}
			srv := httptest.NewServer(o)
			defer srv.Close()
			dir := t.TempDir()
			mm := newTestModelManager(t, dir)
			dest := filepath.Join(dir, "m.safetensors")

			res := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", tt.sha)
			assert.Equal(t, tt.wantDone, res.done, res.err)
			if tt.wantErr != "" {
				assert.Contains(t, res.err, tt.wantErr)
			}
			if tt.name == "mismatch" {
				assert.Contains(t, res.err, sum(content))
			}
			if tt.wantFile {
				assert.FileExists(t, dest)
			} else {
				assert.NoFileExists(t, dest)
			}
			assert.NoFileExists(t, dest+".part")
			assert.Equal(t, tt.wantHits, o.hits.Load())
		})
	}
}

func TestDownloadSkipsExistingFile(t *testing.T) {
	t.Parallel()
	content := payload(64 << 10)
	stale := payload(1000)
	tests := []struct {
		name     string
		existing []byte
		sha      string
		wantHits int32
		want     []byte
	}{
		{name: "exists, no sha", existing: stale, wantHits: 0, want: stale},
		{name: "exists, sha matches", existing: content, sha: sum(content), wantHits: 0, want: content},
		{name: "exists, sha differs", existing: stale, sha: sum(content), wantHits: 1, want: content},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &origin{content: content}
			srv := httptest.NewServer(o)
			defer srv.Close()
			dir := t.TempDir()
			mm := newTestModelManager(t, dir)
			dest := filepath.Join(dir, "m.safetensors")
			require.NoError(t, os.WriteFile(dest, tt.existing, 0o644))

			res := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", tt.sha)
			require.True(t, res.done, res.err)
			assert.Equal(t, tt.wantHits, o.hits.Load())
			got, err := os.ReadFile(dest)
			require.NoError(t, err)
			assert.Equal(t, sum(tt.want), sum(got))
		})
	}
}

func TestDownloadSameTargetTwiceAtOnce(t *testing.T) {
	t.Parallel()
	content := payload(64 << 10)
	o := &origin{content: content, block: make(chan struct{})}
	srv := httptest.NewServer(o)
	defer srv.Close()
	dir := t.TempDir()
	mm := newTestModelManager(t, dir)

	firstDone := make(chan sseResult, 1)
	go func() { firstDone <- streamDownload(t, mm, srv.URL+"/m", "m.safetensors", "") }()
	require.Eventually(t, func() bool { return o.hits.Load() == 1 }, 5*time.Second, 10*time.Millisecond)

	second := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", "")
	assert.False(t, second.done)
	assert.Contains(t, second.err, "already downloading")

	close(o.block)
	first := <-firstDone
	require.True(t, first.done, first.err)
	got, err := os.ReadFile(filepath.Join(dir, "m.safetensors"))
	require.NoError(t, err)
	assert.Equal(t, sum(content), sum(got))
}

func TestDownloadClientGoneKeepsPart(t *testing.T) {
	t.Parallel()
	content := payload(4 << 20)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write(content[:1<<20])
		w.(http.Flusher).Flush()
		<-release
		w.Write(content[1<<20:])
	}))
	defer srv.Close()
	defer close(release)
	dir := t.TempDir()
	mm := newTestModelManager(t, dir)
	dest := filepath.Join(dir, "m.safetensors")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		require.Eventually(t, func() bool {
			st, err := os.Stat(dest + ".part")
			return err == nil && st.Size() >= 1<<20
		}, 5*time.Second, 10*time.Millisecond)
		cancel()
	}()
	res := streamDownloadCtx(t, ctx, mm, srv.URL+"/m", "m.safetensors", "")
	assert.False(t, res.done)
	assert.NoFileExists(t, dest)
	st, err := os.Stat(dest + ".part")
	require.NoError(t, err)
	assert.Equal(t, int64(1<<20), st.Size())
}

func TestDownloadPostAcceptsSHA256AndResumes(t *testing.T) {
	t.Parallel()
	content := payload(1 << 20)
	o := &origin{content: content}
	srv := httptest.NewServer(o)
	defer srv.Close()
	dir := t.TempDir()
	mm := newTestModelManager(t, dir)
	dest := filepath.Join(dir, "m.safetensors")
	require.NoError(t, os.WriteFile(dest+".part", content[:1000], 0o644))

	post := func(sha string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"url": srv.URL + "/m", "filename": "m.safetensors", "sha256": sha})
		rec := httptest.NewRecorder()
		mm.handleSDModels(rec, httptest.NewRequest(http.MethodPost, "/api/server/models/sd", bytes.NewReader(body)))
		return rec
	}

	rec := post(sum(content))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "bytes=1000-", o.rangeHeaders()[0])
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, sum(content), sum(got))

	require.NoError(t, os.Remove(dest))
	rec = post(strings.Repeat("f", 64))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "sha256 mismatch")
	assert.NoFileExists(t, dest)
	assert.NoFileExists(t, dest+".part")

	rec = post("nothex")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

type versionedOrigin struct {
	mu      sync.Mutex
	content []byte
	etag    string
	lastMod time.Time
	abort   int
	hits    int
	codes   []int
}

func (o *versionedOrigin) set(content []byte, etag string, lastMod time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.content, o.etag, o.lastMod = content, etag, lastMod
}

func (o *versionedOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	o.hits++
	first := o.hits == 1
	content, etag, lastMod, abort := o.content, o.etag, o.lastMod, o.abort
	o.mu.Unlock()
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if first && abort > 0 {
		if !lastMod.IsZero() {
			w.Header().Set("Last-Modified", lastMod.UTC().Format(http.TimeFormat))
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content[:abort])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	http.ServeContent(&codeRecorder{ResponseWriter: w, o: o}, r, "f", lastMod, bytes.NewReader(content))
}

func (o *versionedOrigin) statusCodes() []int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]int(nil), o.codes...)
}

type codeRecorder struct {
	http.ResponseWriter
	o     *versionedOrigin
	wrote bool
}

func (c *codeRecorder) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		c.o.mu.Lock()
		c.o.codes = append(c.o.codes, code)
		c.o.mu.Unlock()
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *codeRecorder) Write(p []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(p)
}

func TestDownloadResumeChecksSourceUnchanged(t *testing.T) {
	t.Parallel()
	oldContent := payload(2 << 20)
	newContent := bytes.Repeat([]byte{0xAB}, 2<<20)
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	tests := []struct {
		name      string
		etag1     string
		etag2     string
		lm1, lm2  time.Time
		wantMeta  bool
		want      func(have int64) []byte
		wantCodes []int
	}{
		{name: "same strong etag resumes", etag1: `"v1"`, etag2: `"v1"`, wantMeta: true,
			want: func(int64) []byte { return oldContent }, wantCodes: []int{http.StatusPartialContent}},
		{name: "changed etag restarts", etag1: `"v1"`, etag2: `"v2"`, wantMeta: true,
			want: func(int64) []byte { return newContent }, wantCodes: []int{http.StatusPartialContent, http.StatusOK}},
		{name: "same last-modified resumes", lm1: t1, lm2: t1, wantMeta: true,
			want: func(int64) []byte { return oldContent }, wantCodes: []int{http.StatusPartialContent}},
		{name: "changed last-modified restarts", lm1: t1, lm2: t2, wantMeta: true,
			want: func(int64) []byte { return newContent }, wantCodes: []int{http.StatusPartialContent, http.StatusOK}},
		{name: "weak etag falls back to last-modified", etag1: `W/"a"`, etag2: `W/"a"`, lm1: t1, lm2: t2, wantMeta: true,
			want: func(int64) []byte { return newContent }, wantCodes: []int{http.StatusPartialContent, http.StatusOK}},
		{name: "no validator keeps old behaviour", wantMeta: false,
			want: func(have int64) []byte {
				return append(append([]byte(nil), oldContent[:have]...), newContent[have:]...)
			}, wantCodes: []int{http.StatusPartialContent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &versionedOrigin{abort: 1 << 20}
			o.set(oldContent, tt.etag1, tt.lm1)
			srv := httptest.NewServer(o)
			defer srv.Close()
			dir := t.TempDir()
			mm := newTestModelManager(t, dir)
			dest := filepath.Join(dir, "m.safetensors")

			first := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", "")
			require.False(t, first.done)
			st, err := os.Stat(dest + ".part")
			require.NoError(t, err)
			if tt.wantMeta {
				assert.FileExists(t, dest+".part.meta")
			} else {
				assert.NoFileExists(t, dest+".part.meta")
			}

			next := newContent
			if sum(tt.want(st.Size())) == sum(oldContent) {
				next = oldContent
			}
			o.set(next, tt.etag2, tt.lm2)
			second := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", "")
			require.True(t, second.done, second.err)
			got, err := os.ReadFile(dest)
			require.NoError(t, err)
			assert.Equal(t, sum(tt.want(st.Size())), sum(got))
			assert.Equal(t, tt.wantCodes, o.statusCodes())
			assert.NoFileExists(t, dest+".part")
			assert.NoFileExists(t, dest+".part.meta")
		})
	}
}

func TestDownloadMetaRemovedOnSHAMismatch(t *testing.T) {
	t.Parallel()
	content := payload(2 << 20)
	o := &versionedOrigin{abort: 1 << 20}
	o.set(content, `"v1"`, time.Time{})
	srv := httptest.NewServer(o)
	defer srv.Close()
	dir := t.TempDir()
	mm := newTestModelManager(t, dir)
	dest := filepath.Join(dir, "m.safetensors")

	first := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", "")
	require.False(t, first.done)
	require.FileExists(t, dest+".part.meta")

	res := streamDownload(t, mm, srv.URL+"/m", "m.safetensors", strings.Repeat("0", 64))
	assert.Contains(t, res.err, "sha256 mismatch")
	assert.NoFileExists(t, dest)
	assert.NoFileExists(t, dest+".part")
	assert.NoFileExists(t, dest+".part.meta")
}
