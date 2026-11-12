package llamacpp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRegistry struct {
	mu        sync.Mutex
	manifests map[string][]byte
	blobs     map[string][]byte
	requests  []string
	ranges    []string
	cutAt     int
	corrupt   string
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{manifests: map[string][]byte{}, blobs: map[string][]byte{}}
}

func (r *fakeRegistry) add(repo, tag, family string, layers ...fixtureLayer) Manifest {
	cfgData := []byte(`{"model_format":"gguf","model_family":"` + family + `"}`)
	cfg := Layer{MediaType: "application/vnd.docker.container.image.v1+json", Digest: digestOf(cfgData), Size: int64(len(cfgData))}
	r.blobs[cfg.Digest] = cfgData
	mf := Manifest{SchemaVersion: 2, MediaType: MediaManifest, Config: cfg}
	for _, l := range layers {
		d := digestOf(l.data)
		r.blobs[d] = l.data
		mf.Layers = append(mf.Layers, Layer{MediaType: l.media, Digest: d, Size: int64(len(l.data))})
	}
	raw, _ := json.Marshal(mf)
	r.manifests[repo+"/"+tag] = raw
	return mf
}

func (r *fakeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req.URL.Path)
	p := strings.TrimPrefix(req.URL.Path, "/v2/")
	if i := strings.Index(p, "/manifests/"); i >= 0 {
		raw, ok := r.manifests[p[:i]+"/"+p[i+len("/manifests/"):]]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Write(raw)
		return
	}
	if i := strings.Index(p, "/blobs/"); i >= 0 {
		d := p[i+len("/blobs/"):]
		data, ok := r.blobs[d]
		if !ok {
			http.NotFound(w, req)
			return
		}
		if d == r.corrupt {
			data = append([]byte("X"), data[1:]...)
		}
		start := 0
		if rg := req.Header.Get("Range"); rg != "" {
			r.ranges = append(r.ranges, rg)
			fmt.Sscanf(rg, "bytes=%d-", &start)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
			w.WriteHeader(http.StatusPartialContent)
		}
		body := data[start:]
		if r.cutAt > 0 && len(body) > r.cutAt {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Write(body[:r.cutAt])
			r.cutAt = 0
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}
		w.Write(body)
		return
	}
	http.NotFound(w, req)
}

func (r *fakeRegistry) blobGets() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.requests {
		if strings.Contains(p, "/blobs/") {
			n++
		}
	}
	return n
}

func newPuller(t *testing.T, reg *fakeRegistry) (*Puller, *Store) {
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	st := &Store{Root: t.TempDir()}
	return &Puller{Store: st, BaseURL: func(string) string { return srv.URL }}, st
}

func TestPullWritesOllamaLayout(t *testing.T) {
	reg := newFakeRegistry()
	model := []byte(strings.Repeat("GGUF-model-", 1000))
	reg.add("GitMylo/vl", "Q4_K_M", "qwen3vl", fixtureLayer{MediaModel, model}, fixtureLayer{MediaProjector, []byte("mmproj")})
	p, st := newPuller(t, reg)

	var statuses []string
	require.NoError(t, p.Pull(context.Background(), "hf.co/GitMylo/vl:Q4_K_M", func(pr Progress) { statuses = append(statuses, pr.Status) }))
	assert.Equal(t, "pulling manifest", statuses[0])
	assert.Equal(t, "success", statuses[len(statuses)-1])

	m, err := st.Resolve("hf.co/GitMylo/vl:Q4_K_M")
	require.NoError(t, err)
	assert.True(t, m.Vision())
	b, err := os.ReadFile(m.ModelPath)
	require.NoError(t, err)
	assert.Equal(t, model, b)
	assert.FileExists(t, filepath.Join(st.Root, "manifests", "hf.co", "GitMylo", "vl", "Q4_K_M"))

	first := reg.blobGets()
	require.NoError(t, p.Pull(context.Background(), "hf.co/GitMylo/vl:Q4_K_M", nil))
	assert.Equal(t, first, reg.blobGets(), "second pull downloads nothing")
}

func TestPullResumesAfterCut(t *testing.T) {
	reg := newFakeRegistry()
	model := []byte(strings.Repeat("0123456789", 5000))
	reg.add("library/m", "latest", "qwen2", fixtureLayer{MediaModel, model})
	p, st := newPuller(t, reg)
	reg.cutAt = 20000

	err := p.Pull(context.Background(), "m", nil)
	require.Error(t, err)
	_, err = st.Resolve("m")
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, p.Pull(context.Background(), "m", nil))
	require.NotEmpty(t, reg.ranges)
	assert.Equal(t, "bytes=20000-", reg.ranges[len(reg.ranges)-1])
	m, err := st.Resolve("m:latest")
	require.NoError(t, err)
	b, _ := os.ReadFile(m.ModelPath)
	assert.Equal(t, model, b)
}

func TestPullRejectsBadDigest(t *testing.T) {
	reg := newFakeRegistry()
	mf := reg.add("library/m", "latest", "qwen2", fixtureLayer{MediaModel, []byte("GGUF-good-bytes")})
	reg.corrupt = mf.Layers[0].Digest
	p, st := newPuller(t, reg)

	err := p.Pull(context.Background(), "m", nil)
	assert.ErrorIs(t, err, ErrDigest)
	path, _ := st.BlobPath(mf.Layers[0].Digest)
	assert.NoFileExists(t, path)
	assert.NoFileExists(t, path+"-partial")
	_, err = st.Resolve("m")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPullRefusesOllamaOwnFormatBeforeModelBlob(t *testing.T) {
	reg := newFakeRegistry()
	mf := reg.add("library/qwen2.5vl", "7b", "qwen25vl", fixtureLayer{MediaModel, []byte("huge-blob")})
	p, st := newPuller(t, reg)

	err := p.Pull(context.Background(), "qwen2.5vl:7b", nil)
	assert.ErrorIs(t, err, ErrUnsupported)
	path, _ := st.BlobPath(mf.Layers[0].Digest)
	assert.NoFileExists(t, path)
	for _, r := range reg.requests {
		assert.NotContains(t, r, mf.Layers[0].Digest)
	}
}

func TestPullRejectsForeignRegistryAndMissing(t *testing.T) {
	p, _ := newPuller(t, newFakeRegistry())
	assert.ErrorIs(t, p.Pull(context.Background(), "evil.example/ns/m:1", nil), ErrBadName)
	assert.ErrorIs(t, p.Pull(context.Background(), "nope:1", nil), ErrNotFound)
}

func TestFacadePullStreamsAndRefreshes(t *testing.T) {
	e := newFacadeEnv(t)
	reg := newFakeRegistry()
	reg.add("library/newm", "latest", "qwen2", fixtureLayer{MediaModel, []byte("GGUF-new")})
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	e.f.cfg.Puller = &Puller{Store: e.store, BaseURL: func(string) string { return srv.URL }}

	rec := e.do(t, http.MethodPost, "/api/pull", `{"model":"newm"}`)
	require.Equal(t, 200, rec.Code)
	lines := readNDJSON(t, rec.Body.Bytes())
	assert.Equal(t, "success", lines[len(lines)-1]["status"])
	assert.Contains(t, e.f.IDs(), "newm:latest")

	rec = e.do(t, http.MethodPost, "/api/pull", `{"model":"missing","stream":false}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
