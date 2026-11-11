package installer

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

type rrReq struct {
	rng  string
	code int
}

type rrServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	cut   map[string]int
	reqs  map[string][]rrReq
}

func newRRServer(t *testing.T) *rrServer {
	t.Helper()
	s := &rrServer{files: map[string][]byte{}, cut: map[string]int{}, reqs: map[string][]rrReq{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *rrServer) record(p, rng string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs[p] = append(s.reqs[p], rrReq{rng: rng, code: code})
}

func (s *rrServer) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	rng := r.Header.Get("Range")
	s.mu.Lock()
	body, ok := s.files[p]
	cutAt, cut := s.cut[p]
	if cut {
		delete(s.cut, p)
	}
	s.mu.Unlock()

	if !ok {
		s.record(p, rng, http.StatusNotFound)
		http.NotFound(w, r)
		return
	}
	if cut {
		s.record(p, rng, http.StatusOK)
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nContent-Type: application/octet-stream\r\n\r\n", len(body))
		buf.Write(body[:cutAt])
		buf.Flush()
		conn.Close()
		return
	}
	if strings.HasPrefix(rng, "bytes=") {
		spec := strings.TrimPrefix(rng, "bytes=")
		fromStr := spec
		if i := strings.Index(spec, "-"); i >= 0 {
			fromStr = spec[:i]
		}
		if from, err := strconv.Atoi(fromStr); err == nil {
			if from >= len(body) {
				s.record(p, rng, http.StatusRequestedRangeNotSatisfiable)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(body)))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if from > 0 {
				s.record(p, rng, http.StatusPartialContent)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
				w.Header().Set("Content-Length", strconv.Itoa(len(body)-from))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(body[from:])
				return
			}
		}
	}
	s.record(p, rng, http.StatusOK)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

func (s *rrServer) put(p string, body []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[p] = body
	return s.srv.URL + p
}

func (s *rrServer) cutOnce(p string, at int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cut[p] = at
}

func (s *rrServer) requests(p string) []rrReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]rrReq(nil), s.reqs[p]...)
}

func findPartFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.Contains(filepath.Base(p), ".part") {
			out = append(out, p)
		}
		return nil
	}))
	return out
}

type rrArchive struct {
	name    string
	path    string
	kind    string
	body    []byte
	binPath string
}

func rrArchives(t *testing.T) []rrArchive {
	t.Helper()
	big := strings.Repeat("payload-", 16384)
	zipBody := rtRead(t, writeZipFile(t, map[string]string{"sd-bin/" + exe("sd-server"): "bin", "sd-bin/pad.txt": big}))
	tgzBody := rtRead(t, writeTgzFile(t, map[string]string{"sd-bin/" + exe("sd-server"): "bin", "sd-bin/pad.txt": big}))
	return []rrArchive{
		{name: "zip", path: "/sd.zip", kind: "zip", body: zipBody},
		{name: "tgz", path: "/sd.tgz", kind: "tgz", body: tgzBody},
	}
}

func prepareFullPart(t *testing.T, s *rrServer, cfg *config.Config, arts []Artifact, a rrArchive, content []byte) string {
	t.Helper()
	require.Len(t, content, len(a.body))
	s.cutOnce(a.path, len(a.body)/2)
	require.Error(t, rtInstaller(cfg, arts).Install(config.SDCppBackendKey))
	require.Len(t, s.requests(a.path), 1)

	parts := findPartFiles(t, cfg.DataDir)
	require.Len(t, parts, 1, "interrupted archive download must leave exactly one .part in DataDir, got %v", parts)
	require.NoError(t, os.WriteFile(parts[0], content, 0o644))
	assert.NoFileExists(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", exe("sd-server")))
	return parts[0]
}

func TestInstallReleaseFullPartNotExtractedIsExtractedWithoutRedownload(t *testing.T) {
	for _, a := range rrArchives(t) {
		t.Run(a.name, func(t *testing.T) {
			s := newRRServer(t)
			cfg := rtConfig(t, "v1")
			arts := []Artifact{{Name: "sd", URL: s.put(a.path, a.body), SHA256: rtSum(a.body), Size: int64(len(a.body)), Kind: a.kind, Dest: "engines/sd-cpp", Strip: 1}}
			prepareFullPart(t, s, cfg, arts, a, a.body)
			before := len(s.requests(a.path))

			inst := rtInstaller(cfg, arts)
			require.NoError(t, inst.Install(config.SDCppBackendKey))

			after := s.requests(a.path)[before:]
			switch len(after) {
			case 0:
			case 1:
				assert.True(t, strings.HasPrefix(after[0].rng, "bytes="), "only a Range request is allowed, got Range %q", after[0].rng)
				assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, after[0].code, "file must not be downloaded again")
			default:
				t.Fatalf("archive downloaded again: %+v", after)
			}
			dest := filepath.Join(cfg.DataDir, "engines", "sd-cpp")
			assert.Equal(t, []byte("bin"), rtRead(t, filepath.Join(dest, exe("sd-server"))))
			assert.FileExists(t, filepath.Join(dest, "pad.txt"))
			assert.Equal(t, "v1", strings.TrimSpace(string(rtRead(t, filepath.Join(dest, ".installed")))))
			assert.True(t, inst.IsInstalled(config.SDCppBackendKey))
			assert.True(t, rtInstaller(cfg, arts).IsInstalled(config.SDCppBackendKey))
		})
	}
}

func TestInstallReleaseFullPartWithShaFileIsExtractedWithoutRedownload(t *testing.T) {
	a := rrArchives(t)[1]
	s := newRRServer(t)
	cfg := rtConfig(t, "v1")
	arts := []Artifact{{Name: "sd", URL: s.put(a.path, a.body), SHAURL: s.put(a.path+".sha256", []byte(rtSum(a.body)+"  sd.tgz\n")), Size: int64(len(a.body)), Kind: a.kind, Dest: "engines/sd-cpp", Strip: 1}}
	prepareFullPart(t, s, cfg, arts, a, a.body)
	before := len(s.requests(a.path))

	inst := rtInstaller(cfg, arts)
	require.NoError(t, inst.Install(config.SDCppBackendKey))

	after := s.requests(a.path)[before:]
	require.LessOrEqual(t, len(after), 1, "archive downloaded again: %+v", after)
	for _, r := range after {
		assert.True(t, strings.HasPrefix(r.rng, "bytes="), r.rng)
		assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, r.code)
	}
	assert.FileExists(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", exe("sd-server")))
	assert.True(t, inst.IsInstalled(config.SDCppBackendKey))
}

func TestInstallReleaseFullPartBadShaIsRedownloaded(t *testing.T) {
	for _, a := range rrArchives(t) {
		t.Run(a.name, func(t *testing.T) {
			s := newRRServer(t)
			cfg := rtConfig(t, "v1")
			arts := []Artifact{{Name: "sd", URL: s.put(a.path, a.body), SHA256: rtSum(a.body), Size: int64(len(a.body)), Kind: a.kind, Dest: "engines/sd-cpp", Strip: 1}}
			garbage := bytes.Repeat([]byte{'Z'}, len(a.body))
			prepareFullPart(t, s, cfg, arts, a, garbage)
			before := len(s.requests(a.path))

			inst := rtInstaller(cfg, arts)
			require.NoError(t, inst.Install(config.SDCppBackendKey))

			after := s.requests(a.path)[before:]
			require.NotEmpty(t, after, "corrupted .part must be downloaded again")
			last := after[len(after)-1]
			assert.Equal(t, http.StatusOK, last.code, "file must be downloaded whole, got %+v", after)
			assert.Contains(t, []string{"", "bytes=0-"}, last.rng, "full download must not resume from the corrupted .part")
			dest := filepath.Join(cfg.DataDir, "engines", "sd-cpp")
			assert.Equal(t, []byte("bin"), rtRead(t, filepath.Join(dest, exe("sd-server"))))
			assert.Equal(t, "v1", strings.TrimSpace(string(rtRead(t, filepath.Join(dest, ".installed")))))
			assert.True(t, inst.IsInstalled(config.SDCppBackendKey))
		})
	}
}

func TestEnsureInstalledReleaseBinaryPresentWithoutMarkerFinishesInstall(t *testing.T) {
	fx := newRTFixture(t)
	cfg := rtConfig(t, "v1")
	bin := cfg.Backends[config.SDCppBackendKey].Binary
	cfg.Processes["sd"] = config.ProcessConfig{Name: "sd", Binary: bin}

	fx.srv.mu.Lock()
	fx.srv.status["/onnx.tgz"] = http.StatusServiceUnavailable
	fx.srv.mu.Unlock()
	require.Error(t, rtInstaller(cfg, fx.arts).Install(config.SDCppBackendKey))
	require.FileExists(t, bin, "first archive must be extracted before the cut")
	require.NoFileExists(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", ".installed"))
	fx.srv.mu.Lock()
	delete(fx.srv.status, "/onnx.tgz")
	fx.srv.mu.Unlock()
	onnxBefore, poseBefore := fx.srv.hitsOf("/onnx.tgz"), fx.srv.hitsOf("/yolox_l.onnx")

	inst := rtInstaller(cfg, fx.arts)
	require.NoError(t, inst.EnsureInstalled("sd", bin))

	assert.Greater(t, fx.srv.hitsOf("/onnx.tgz"), onnxBefore)
	assert.Greater(t, fx.srv.hitsOf("/yolox_l.onnx"), poseBefore)
	d := cfg.DataDir
	assert.FileExists(t, filepath.Join(d, "engines", "onnxruntime", "lib", "libort.so"))
	assert.Equal(t, fx.pose, rtRead(t, filepath.Join(d, filepath.FromSlash(fx.poseRel))))
	assert.Equal(t, "v1", strings.TrimSpace(string(rtRead(t, filepath.Join(d, "engines", "sd-cpp", ".installed")))))
	assert.True(t, inst.IsInstalled(config.SDCppBackendKey))

	total := fx.srv.total()
	require.NoError(t, rtInstaller(cfg, fx.arts).EnsureInstalled("sd", bin))
	assert.Equal(t, total, fx.srv.total(), "complete install must not download again")
}

func TestEnsureInstalledReleaseBinaryPresentNoMarkerNoPriorAttempt(t *testing.T) {
	fx := newRTFixture(t)
	cfg := rtConfig(t, "v1")
	bin := cfg.Backends[config.SDCppBackendKey].Binary
	cfg.Processes["sd"] = config.ProcessConfig{Name: "sd", Binary: bin}
	touch(t, bin)

	inst := rtInstaller(cfg, fx.arts)
	require.NoError(t, inst.EnsureInstalled("sd", bin))

	assert.Positive(t, fx.srv.hitsOf("/onnx.tgz"))
	assert.Positive(t, fx.srv.hitsOf("/yolox_l.onnx"))
	assert.Equal(t, "v1", strings.TrimSpace(string(rtRead(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", ".installed")))))
	assert.True(t, inst.IsInstalled(config.SDCppBackendKey))
}

func TestEnsureInstalledNonReleaseBinaryPresentDownloadsNothing(t *testing.T) {
	srv, hits := zipServer(t, nil)
	cfg := baseConfig(t)
	target := filepath.Join(t.TempDir(), "tb")
	bin := filepath.Join(target, exe("server"))
	touch(t, bin)
	cfg.ActiveSD = "testbackend"
	cfg.Backends["testbackend"] = config.BackendConfig{
		Name:       "test",
		ProcessKey: "sd",
		Binary:     bin,
		Install:    zipInstall(srv.URL, target),
	}
	cfg.Processes["sd"] = config.ProcessConfig{Name: "sd", Binary: bin}
	inst := NewInstaller(cfg)
	calls := stubSetup(inst, nil)

	require.NoError(t, inst.EnsureInstalled("sd", bin))

	assert.Zero(t, atomic.LoadInt32(hits))
	assert.Empty(t, calls.list())
	assert.FileExists(t, bin)
}
