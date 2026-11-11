package installer

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

var rtHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func rtFind(arts []Artifact, pred func(Artifact) bool) []Artifact {
	var out []Artifact
	for _, a := range arts {
		if pred(a) {
			out = append(out, a)
		}
	}
	return out
}

func rtURLHas(subs ...string) func(Artifact) bool {
	return func(a Artifact) bool {
		u := strings.ToLower(a.URL)
		for _, s := range subs {
			if !strings.Contains(u, strings.ToLower(s)) {
				return false
			}
		}
		return true
	}
}

func rtInDest(dest string) func(Artifact) bool {
	return func(a Artifact) bool { return filepath.ToSlash(a.Dest) == dest }
}

func TestSDCppArtifactsByPlatform(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		goarch    string
		gpu       GPUKind
		engine    []string
		extra     []string
		forbidden []string
		ownBuild  bool
		kind      string
		onnxOS    string
	}{
		{name: "windows nvidia", goos: "windows", goarch: "amd64", gpu: GPUNvidia, engine: []string{"win-cuda12"}, extra: []string{"cudart"}, kind: "zip", onnxOS: "win-x64"},
		{name: "windows vulkan", goos: "windows", goarch: "amd64", gpu: GPUVulkan, engine: []string{"win-vulkan"}, forbidden: []string{"cudart", "cuda12"}, kind: "zip", onnxOS: "win-x64"},
		{name: "windows no gpu", goos: "windows", goarch: "amd64", gpu: GPUNone, engine: []string{"win-cpu"}, forbidden: []string{"cudart", "cuda12", "vulkan"}, kind: "zip", onnxOS: "win-x64"},
		{name: "macos arm64", goos: "darwin", goarch: "arm64", gpu: GPUNone, engine: []string{"darwin", "arm64"}, forbidden: []string{"cudart"}, kind: "zip", onnxOS: "osx-arm64"},
		{name: "linux nvidia", goos: "linux", goarch: "amd64", gpu: GPUNvidia, engine: []string{"github.com/Zazza/imagecaster-server", "sdcpp-linux-x86_64-cuda.tar.gz"}, forbidden: []string{"cudart"}, ownBuild: true, kind: "tgz", onnxOS: "linux-x64"},
		{name: "linux vulkan", goos: "linux", goarch: "amd64", gpu: GPUVulkan, engine: []string{"github.com/Zazza/imagecaster-server", "sdcpp-linux-x86_64-vulkan.tar.gz"}, forbidden: []string{"cudart", "-cuda.", "-cpu."}, ownBuild: true, kind: "tgz", onnxOS: "linux-x64"},
		{name: "linux no gpu", goos: "linux", goarch: "amd64", gpu: GPUNone, engine: []string{"github.com/Zazza/imagecaster-server", "sdcpp-linux-x86_64-cpu.tar.gz"}, forbidden: []string{"cudart", "-cuda."}, ownBuild: true, kind: "tgz", onnxOS: "linux-x64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arts, err := SDCppArtifacts(tt.goos, tt.goarch, tt.gpu)
			require.NoError(t, err)

			engine := rtFind(arts, func(a Artifact) bool { return rtInDest("engines/sd-cpp")(a) && rtURLHas(tt.engine...)(a) })
			require.Len(t, engine, 1, "engine artifact")
			assert.Equal(t, tt.kind, engine[0].Kind)
			if tt.ownBuild {
				assert.Empty(t, engine[0].SHA256)
				assert.Equal(t, engine[0].URL+".sha256", engine[0].SHAURL)
				assert.True(t, strings.HasSuffix(engine[0].URL, tt.engine[1]), engine[0].URL)
			}
			for _, e := range tt.extra {
				found := rtFind(arts, func(a Artifact) bool { return rtInDest("engines/sd-cpp")(a) && rtURLHas(e)(a) })
				require.Len(t, found, 1, e)
				assert.Equal(t, "zip", found[0].Kind)
			}
			for _, f := range tt.forbidden {
				assert.Empty(t, rtFind(arts, rtURLHas(f)), f)
			}

			onnx := rtFind(arts, rtInDest("engines/onnxruntime"))
			require.Len(t, onnx, 1)
			assert.True(t, rtURLHas("onnxruntime", tt.onnxOS, "1.31.1")(onnx[0]), onnx[0].URL)
			assert.Equal(t, 1, onnx[0].Strip)
			assert.Contains(t, []string{"zip", "tgz"}, onnx[0].Kind)

			for _, model := range []string{"yolox_l.onnx", "dw-ll_ucoco_384.onnx"} {
				pose := rtFind(arts, func(a Artifact) bool { return path.Base(filepath.ToSlash(a.Dest)) == model })
				require.Len(t, pose, 1, model)
				assert.Equal(t, "file", pose[0].Kind)
				assert.Equal(t, "models/openpose", path.Dir(filepath.ToSlash(pose[0].Dest)))
			}

			for _, a := range arts {
				assert.NotEmpty(t, a.Name)
				assert.NotEmpty(t, a.URL)
				assert.NotEmpty(t, a.Dest, a.Name)
				assert.False(t, filepath.IsAbs(a.Dest), a.Dest)
				assert.Contains(t, []string{"zip", "tgz", "file"}, a.Kind, a.Name)
				if a.SHAURL != "" && a.SHA256 == "" {
					continue
				}
				assert.Regexp(t, rtHex64, a.SHA256, a.Name)
			}
			assert.Len(t, rtFind(arts, func(a Artifact) bool { return a.SHA256 == "" }), map[bool]int{true: 1, false: 0}[tt.ownBuild])
		})
	}
}

func TestSDCppArtifactsUnsupportedPlatform(t *testing.T) {
	tests := []struct{ goos, goarch string }{
		{"linux", "arm64"},
		{"darwin", "amd64"},
		{"windows", "arm64"},
	}
	for _, tt := range tests {
		for _, gpu := range []GPUKind{GPUNone, GPUNvidia, GPUVulkan} {
			t.Run(fmt.Sprintf("%s/%s/%v", tt.goos, tt.goarch, gpu), func(t *testing.T) {
				arts, err := SDCppArtifacts(tt.goos, tt.goarch, gpu)
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrUnsupportedPlatform), err.Error())
				assert.Empty(t, arts)
			})
		}
	}
}

type rtServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	files  map[string][]byte
	status map[string]int
	hits   map[string]int
	ranges map[string][]string
	cut    map[string]int
}

func newRTServer(t *testing.T) *rtServer {
	t.Helper()
	s := &rtServer{
		files:  map[string][]byte{},
		status: map[string]int{},
		hits:   map[string]int{},
		ranges: map[string][]string{},
		cut:    map[string]int{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *rtServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p := r.URL.Path
	s.hits[p]++
	s.ranges[p] = append(s.ranges[p], r.Header.Get("Range"))
	code, hasCode := s.status[p]
	body, ok := s.files[p]
	cutAt, cut := s.cut[p]
	if cut {
		delete(s.cut, p)
	}
	s.mu.Unlock()

	if hasCode {
		w.WriteHeader(code)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if cut {
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
	if rg := r.Header.Get("Range"); strings.HasPrefix(rg, "bytes=") {
		spec := strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-")
		if from, err := strconv.Atoi(spec); err == nil && from > 0 && from < len(body) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-from))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body[from:])
			return
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

func (s *rtServer) put(p string, body []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[p] = body
	return s.srv.URL + p
}

func (s *rtServer) fail(p string, code int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[p] = code
	return s.srv.URL + p
}

func (s *rtServer) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, h := range s.hits {
		n += h
	}
	return n
}

func (s *rtServer) hitsOf(p string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[p]
}

func rtSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func rtRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return b
}

func rtRawZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, n := range names {
		f, err := w.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Store})
		require.NoError(t, err)
		_, err = f.Write([]byte("data:" + n))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func rtConfig(t *testing.T, version string) *config.Config {
	t.Helper()
	cfg := baseConfig(t)
	cfg.ActiveSD = config.SDCppBackendKey
	cfg.Backends[config.SDCppBackendKey] = config.BackendConfig{
		Name:       "sd.cpp",
		Kind:       config.KindSDCpp,
		ProcessKey: "sd",
		Binary:     filepath.Join(cfg.DataDir, "engines", "sd-cpp", exe("sd-server")),
		Install: config.InstallConfig{
			Method:  config.InstallRelease,
			Target:  filepath.Join(cfg.DataDir, "engines", "sd-cpp"),
			Version: version,
		},
	}
	return cfg
}

func rtInstaller(cfg *config.Config, arts []Artifact) *Installer {
	inst := NewInstaller(cfg)
	inst.ReleaseArtifacts = func() ([]Artifact, error) { return arts, nil }
	return inst
}

type rtFixture struct {
	srv     *rtServer
	arts    []Artifact
	engine  []byte
	onnx    []byte
	pose    []byte
	poseRel string
}

func newRTFixture(t *testing.T) *rtFixture {
	t.Helper()
	srv := newRTServer(t)
	engine := rtRead(t, writeZipFile(t, map[string]string{"sd-bin/" + exe("sd-server"): "bin", "sd-bin/ggml.txt": "lib"}))
	onnx := rtRead(t, writeTgzFile(t, map[string]string{"onnxruntime-x/lib/libort.so": "ort"}))
	pose := bytes.Repeat([]byte("pose"), 1024)
	shaFile := []byte(rtSum(onnx) + "  onnx.tgz\n")
	poseRel := "models/openpose/yolox_l.onnx"
	return &rtFixture{
		srv:     srv,
		engine:  engine,
		onnx:    onnx,
		pose:    pose,
		poseRel: poseRel,
		arts: []Artifact{
			{Name: "sd", URL: srv.put("/sd.zip", engine), SHA256: rtSum(engine), Size: int64(len(engine)), Kind: "zip", Dest: "engines/sd-cpp", Strip: 1},
			{Name: "ort", URL: srv.put("/onnx.tgz", onnx), SHAURL: srv.put("/onnx.tgz.sha256", shaFile), Size: int64(len(onnx)), Kind: "tgz", Dest: "engines/onnxruntime", Strip: 1},
			{Name: "yolox", URL: srv.put("/yolox_l.onnx", pose), SHA256: rtSum(pose), Size: int64(len(pose)), Kind: "file", Dest: poseRel},
		},
	}
}

func TestInstallReleaseDownloadsVerifiesAndMarksVersion(t *testing.T) {
	fx := newRTFixture(t)
	cfg := rtConfig(t, "v1")
	inst := rtInstaller(cfg, fx.arts)

	require.NoError(t, inst.Install(config.SDCppBackendKey))

	d := cfg.DataDir
	assert.FileExists(t, filepath.Join(d, "engines", "sd-cpp", exe("sd-server")))
	assert.FileExists(t, filepath.Join(d, "engines", "sd-cpp", "ggml.txt"))
	assert.NoDirExists(t, filepath.Join(d, "engines", "sd-cpp", "sd-bin"))
	assert.FileExists(t, filepath.Join(d, "engines", "onnxruntime", "lib", "libort.so"))
	assert.Equal(t, fx.pose, rtRead(t, filepath.Join(d, filepath.FromSlash(fx.poseRel))))
	assert.Equal(t, "v1", strings.TrimSpace(string(rtRead(t, filepath.Join(d, "engines", "sd-cpp", ".installed")))))
	assert.True(t, inst.IsInstalled(config.SDCppBackendKey))
	assert.Equal(t, "v1", inst.Status()[config.SDCppBackendKey].Version)
	assert.Equal(t, 1, fx.srv.hitsOf("/onnx.tgz.sha256"))
}

func TestInstallReleaseRepeatDoesNotDownload(t *testing.T) {
	fx := newRTFixture(t)
	cfg := rtConfig(t, "v1")
	inst := rtInstaller(cfg, fx.arts)
	require.NoError(t, inst.Install(config.SDCppBackendKey))
	before := fx.srv.total()
	require.Positive(t, before)

	require.NoError(t, inst.Install(config.SDCppBackendKey))
	assert.Equal(t, before, fx.srv.total())

	fresh := rtInstaller(cfg, fx.arts)
	assert.True(t, fresh.IsInstalled(config.SDCppBackendKey))
	require.NoError(t, fresh.Install(config.SDCppBackendKey))
	assert.Equal(t, before, fx.srv.total())
}

func TestInstallReleaseVersionChangeReinstalls(t *testing.T) {
	fx := newRTFixture(t)
	cfg := rtConfig(t, "v1")
	require.NoError(t, rtInstaller(cfg, fx.arts).Install(config.SDCppBackendKey))
	before := fx.srv.hitsOf("/sd.zip")

	b := cfg.Backends[config.SDCppBackendKey]
	b.Install.Version = "v2"
	cfg.Backends[config.SDCppBackendKey] = b
	inst := rtInstaller(cfg, fx.arts)
	assert.False(t, inst.IsInstalled(config.SDCppBackendKey))

	require.NoError(t, inst.Install(config.SDCppBackendKey))
	assert.Greater(t, fx.srv.hitsOf("/sd.zip"), before)
	assert.Equal(t, "v2", strings.TrimSpace(string(rtRead(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", ".installed")))))
	assert.True(t, inst.IsInstalled(config.SDCppBackendKey))
}

func TestInstallReleaseForeignMarkerIsNotInstalled(t *testing.T) {
	cfg := rtConfig(t, "v1")
	touch(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", exe("sd-server")))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.DataDir, "engines", "sd-cpp", ".installed"), []byte("v0\n"), 0o644))

	assert.False(t, rtInstaller(cfg, nil).IsInstalled(config.SDCppBackendKey))
}

func TestInstallReleaseNoMarkerIsNotInstalled(t *testing.T) {
	cfg := rtConfig(t, "v1")
	touch(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", exe("sd-server")))

	assert.False(t, rtInstaller(cfg, nil).IsInstalled(config.SDCppBackendKey))
}

func TestInstallReleaseBadChecksum(t *testing.T) {
	tests := []struct {
		name  string
		art   func(s *rtServer) Artifact
		check func(t *testing.T, dataDir string)
	}{
		{
			name: "zip sha mismatch",
			art: func(s *rtServer) Artifact {
				body := rtRawZip(t, exe("sd-server"), "lib.txt")
				return Artifact{Name: "sd", URL: s.put("/sd.zip", body), SHA256: rtSum([]byte("other")), Size: int64(len(body)), Kind: "zip", Dest: "engines/sd-cpp"}
			},
			check: func(t *testing.T, dataDir string) {
				dest := filepath.Join(dataDir, "engines", "sd-cpp")
				assert.NoFileExists(t, filepath.Join(dest, exe("sd-server")))
				assert.NoFileExists(t, filepath.Join(dest, "lib.txt"))
			},
		},
		{
			name: "sha file mismatch",
			art: func(s *rtServer) Artifact {
				body := rtRead(t, writeTgzFile(t, map[string]string{"top/" + exe("sd-server"): "bin"}))
				return Artifact{Name: "sd", URL: s.put("/sd.tgz", body), SHAURL: s.put("/sd.tgz.sha256", []byte(rtSum([]byte("other"))+"  sd.tgz\n")), Size: int64(len(body)), Kind: "tgz", Dest: "engines/sd-cpp", Strip: 1}
			},
			check: func(t *testing.T, dataDir string) {
				assert.NoFileExists(t, filepath.Join(dataDir, "engines", "sd-cpp", exe("sd-server")))
			},
		},
		{
			name: "file sha mismatch",
			art: func(s *rtServer) Artifact {
				body := []byte("pose-model")
				return Artifact{Name: "pose", URL: s.put("/pose.onnx", body), SHA256: rtSum([]byte("other")), Size: int64(len(body)), Kind: "file", Dest: "models/openpose/yolox_l.onnx"}
			},
			check: func(t *testing.T, dataDir string) {
				dest := filepath.Join(dataDir, "models", "openpose", "yolox_l.onnx")
				assert.NoFileExists(t, dest)
				left, _ := filepath.Glob(dest + "*")
				assert.Empty(t, left)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newRTServer(t)
			cfg := rtConfig(t, "v1")
			inst := rtInstaller(cfg, []Artifact{tt.art(s)})

			require.Error(t, inst.Install(config.SDCppBackendKey))

			assert.False(t, inst.IsInstalled(config.SDCppBackendKey))
			assert.NoFileExists(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", ".installed"))
			tt.check(t, cfg.DataDir)
			assert.False(t, rtInstaller(cfg, nil).IsInstalled(config.SDCppBackendKey))
		})
	}
}

func TestInstallReleaseHTTPError(t *testing.T) {
	tests := []struct {
		name string
		code int
		art  func(s *rtServer, code int) Artifact
	}{
		{
			name: "artifact 404",
			code: http.StatusNotFound,
			art: func(s *rtServer, code int) Artifact {
				return Artifact{Name: "sd", URL: s.fail("/sd.zip", code), SHA256: rtSum([]byte("x")), Kind: "zip", Dest: "engines/sd-cpp"}
			},
		},
		{
			name: "artifact 503",
			code: http.StatusServiceUnavailable,
			art: func(s *rtServer, code int) Artifact {
				return Artifact{Name: "pose", URL: s.fail("/pose.onnx", code), SHA256: rtSum([]byte("x")), Kind: "file", Dest: "models/openpose/yolox_l.onnx"}
			},
		},
		{
			name: "sha file 500",
			code: http.StatusInternalServerError,
			art: func(s *rtServer, code int) Artifact {
				body := rtRead(t, writeTgzFile(t, map[string]string{"top/a": "a"}))
				return Artifact{Name: "sd", URL: s.put("/sd.tgz", body), SHAURL: s.fail("/sd.tgz.sha256", code), Kind: "tgz", Dest: "engines/sd-cpp", Strip: 1}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newRTServer(t)
			cfg := rtConfig(t, "v1")
			inst := rtInstaller(cfg, []Artifact{tt.art(s, tt.code)})

			err := inst.Install(config.SDCppBackendKey)

			require.Error(t, err)
			assert.Contains(t, err.Error(), strconv.Itoa(tt.code))
			assert.False(t, inst.IsInstalled(config.SDCppBackendKey))
			assert.NoFileExists(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", ".installed"))
		})
	}
}

func TestInstallReleaseArtifactListError(t *testing.T) {
	cfg := rtConfig(t, "v1")
	inst := NewInstaller(cfg)
	inst.ReleaseArtifacts = func() ([]Artifact, error) { return nil, ErrUnsupportedPlatform }

	err := inst.Install(config.SDCppBackendKey)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnsupportedPlatform), err.Error())
	assert.False(t, inst.IsInstalled(config.SDCppBackendKey))
	assert.NoFileExists(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", ".installed"))
}

func TestInstallReleaseZipSlip(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		strip int
	}{
		{name: "parent path", names: []string{"ok.txt", "../evil.txt"}, strip: 0},
		{name: "parent after strip", names: []string{"top/ok.txt", "top/../../evil.txt"}, strip: 1},
		{name: "deep parent", names: []string{"ok.txt", "a/../../../evil.txt"}, strip: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newRTServer(t)
			cfg := rtConfig(t, "v1")
			body := rtRawZip(t, tt.names...)
			inst := rtInstaller(cfg, []Artifact{{Name: "sd", URL: s.put("/sd.zip", body), SHA256: rtSum(body), Size: int64(len(body)), Kind: "zip", Dest: "engines/sd-cpp", Strip: tt.strip}})

			require.Error(t, inst.Install(config.SDCppBackendKey))

			d := cfg.DataDir
			assert.NoFileExists(t, filepath.Join(d, "engines", "evil.txt"))
			assert.NoFileExists(t, filepath.Join(d, "evil.txt"))
			assert.NoFileExists(t, filepath.Join(filepath.Dir(d), "evil.txt"))
			assert.False(t, inst.IsInstalled(config.SDCppBackendKey))
			assert.NoFileExists(t, filepath.Join(d, "engines", "sd-cpp", ".installed"))
		})
	}
}

func TestInstallReleaseFileAlreadyInPlace(t *testing.T) {
	tests := []struct {
		name     string
		existing []byte
		wantHits int
	}{
		{name: "correct sha kept", existing: []byte("pose-model"), wantHits: 0},
		{name: "wrong content replaced", existing: []byte("broken"), wantHits: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newRTServer(t)
			cfg := rtConfig(t, "v1")
			want := []byte("pose-model")
			dest := filepath.Join(cfg.DataDir, "models", "openpose", "yolox_l.onnx")
			require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
			require.NoError(t, os.WriteFile(dest, tt.existing, 0o644))
			engine := rtRawZip(t, exe("sd-server"))
			arts := []Artifact{
				{Name: "sd", URL: s.put("/sd.zip", engine), SHA256: rtSum(engine), Size: int64(len(engine)), Kind: "zip", Dest: "engines/sd-cpp"},
				{Name: "pose", URL: s.put("/yolox_l.onnx", want), SHA256: rtSum(want), Size: int64(len(want)), Kind: "file", Dest: "models/openpose/yolox_l.onnx"},
			}
			inst := rtInstaller(cfg, arts)

			require.NoError(t, inst.Install(config.SDCppBackendKey))

			assert.Equal(t, tt.wantHits, s.hitsOf("/yolox_l.onnx"))
			assert.Equal(t, want, rtRead(t, dest))
			assert.True(t, inst.IsInstalled(config.SDCppBackendKey))
		})
	}
}

func TestInstallReleaseResumesAfterCut(t *testing.T) {
	s := newRTServer(t)
	cfg := rtConfig(t, "v1")
	body := bytes.Repeat([]byte("0123456789abcdef"), 8192)
	half := len(body) / 2
	engine := rtRawZip(t, exe("sd-server"))
	arts := []Artifact{
		{Name: "sd", URL: s.put("/sd.zip", engine), SHA256: rtSum(engine), Size: int64(len(engine)), Kind: "zip", Dest: "engines/sd-cpp"},
		{Name: "pose", URL: s.put("/big.onnx", body), SHA256: rtSum(body), Size: int64(len(body)), Kind: "file", Dest: "models/openpose/dw-ll_ucoco_384.onnx"},
	}
	s.mu.Lock()
	s.cut["/big.onnx"] = half
	s.mu.Unlock()
	inst := rtInstaller(cfg, arts)

	require.Error(t, inst.Install(config.SDCppBackendKey))
	assert.False(t, inst.IsInstalled(config.SDCppBackendKey))

	require.NoError(t, inst.Install(config.SDCppBackendKey))

	s.mu.Lock()
	ranges := append([]string(nil), s.ranges["/big.onnx"]...)
	s.mu.Unlock()
	require.Len(t, ranges, 2)
	require.True(t, strings.HasPrefix(ranges[1], "bytes="), "second request must resume, got Range %q", ranges[1])
	from, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(ranges[1], "bytes="), "-"))
	require.NoError(t, err)
	assert.Greater(t, from, 0)
	assert.LessOrEqual(t, from, half)
	assert.Equal(t, body, rtRead(t, filepath.Join(cfg.DataDir, "models", "openpose", "dw-ll_ucoco_384.onnx")))
	assert.True(t, inst.IsInstalled(config.SDCppBackendKey))
}

func TestInstallReleaseRetryAfterLateFailureSkipsExtractedArchives(t *testing.T) {
	s := newRTServer(t)
	cfg := rtConfig(t, "v1")
	engine := rtRead(t, writeZipFile(t, map[string]string{"sd-bin/" + exe("sd-server"): "bin"}))
	onnx := rtRead(t, writeTgzFile(t, map[string]string{"onnxruntime-x/lib/libort.so": "ort"}))
	pose := []byte("pose-model")
	arts := []Artifact{
		{Name: "sd", URL: s.put("/sd.zip", engine), SHA256: rtSum(engine), Size: int64(len(engine)), Kind: "zip", Dest: "engines/sd-cpp", Strip: 1},
		{Name: "ort", URL: s.put("/onnx.tgz", onnx), SHA256: rtSum(onnx), Size: int64(len(onnx)), Kind: "tgz", Dest: "engines/onnxruntime", Strip: 1},
		{Name: "pose", URL: s.fail("/yolox_l.onnx", http.StatusServiceUnavailable), SHA256: rtSum(pose), Size: int64(len(pose)), Kind: "file", Dest: "models/openpose/yolox_l.onnx"},
	}
	inst := rtInstaller(cfg, arts)

	require.Error(t, inst.Install(config.SDCppBackendKey))
	assert.False(t, inst.IsInstalled(config.SDCppBackendKey))
	zipHits, tgzHits, poseHits := s.hitsOf("/sd.zip"), s.hitsOf("/onnx.tgz"), s.hitsOf("/yolox_l.onnx")
	require.Equal(t, 1, zipHits)
	require.Equal(t, 1, tgzHits)
	require.Equal(t, 1, poseHits)

	s.mu.Lock()
	delete(s.status, "/yolox_l.onnx")
	s.files["/yolox_l.onnx"] = pose
	s.mu.Unlock()

	require.NoError(t, inst.Install(config.SDCppBackendKey))

	assert.Equal(t, zipHits, s.hitsOf("/sd.zip"))
	assert.Equal(t, tgzHits, s.hitsOf("/onnx.tgz"))
	assert.Equal(t, poseHits+1, s.hitsOf("/yolox_l.onnx"))
	assert.Equal(t, pose, rtRead(t, filepath.Join(cfg.DataDir, "models", "openpose", "yolox_l.onnx")))
	assert.FileExists(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", exe("sd-server")))
	assert.FileExists(t, filepath.Join(cfg.DataDir, "engines", "onnxruntime", "lib", "libort.so"))
	assert.Equal(t, "v1", strings.TrimSpace(string(rtRead(t, filepath.Join(cfg.DataDir, "engines", "sd-cpp", ".installed")))))
	assert.True(t, inst.IsInstalled(config.SDCppBackendKey))
}
