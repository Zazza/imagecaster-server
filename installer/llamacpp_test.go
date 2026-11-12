package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"imagecaster-server/config"
)

func TestLlamaCppArtifacts(t *testing.T) {
	tests := []struct {
		goos, goarch string
		gpu          GPUKind
		shared       bool
		want         []string
	}{
		{"windows", "amd64", GPUNvidia, true, []string{"llama-b12283-bin-win-cuda-12.4-x64.zip"}},
		{"windows", "amd64", GPUNvidia, false, []string{"llama-b12283-bin-win-cuda-12.4-x64.zip", "cudart-llama-bin-win-cuda-12.4-x64.zip"}},
		{"windows", "amd64", GPUVulkan, false, []string{"llama-b12283-bin-win-vulkan-x64.zip"}},
		{"windows", "amd64", GPUNone, false, []string{"llama-b12283-bin-win-cpu-x64.zip"}},
		{"linux", "amd64", GPUNvidia, true, []string{"llama-b12283-bin-ubuntu-cuda-12.8-x64.tar.gz"}},
		{"linux", "amd64", GPUNvidia, false, []string{"llama-b12283-bin-ubuntu-cuda-12.8-x64.tar.gz", "cudart-llama-b12283-bin-ubuntu-cuda-12.8-x64.tar.gz"}},
		{"linux", "amd64", GPUVulkan, false, []string{"llama-b12283-bin-ubuntu-vulkan-x64.tar.gz"}},
		{"linux", "amd64", GPUNone, false, []string{"llama-b12283-bin-ubuntu-x64.tar.gz"}},
		{"darwin", "arm64", GPUNone, false, []string{"llama-b12283-bin-macos-arm64.tar.gz"}},
		{"darwin", "amd64", GPUNone, false, []string{"llama-b12283-bin-macos-x64.tar.gz"}},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.goarch+"/"+tt.want[len(tt.want)-1], func(t *testing.T) {
			arts, err := LlamaCppArtifacts(tt.goos, tt.goarch, tt.gpu, tt.shared)
			require.NoError(t, err)
			var names []string
			for _, a := range arts {
				names = append(names, a.Name)
				assert.Len(t, a.SHA256, 64)
				assert.Positive(t, a.Size)
				assert.Equal(t, "https://github.com/ggml-org/llama.cpp/releases/download/b12283/"+a.Name, a.URL)
				assert.Equal(t, config.LlamaCppDir, a.Dest)
				if tt.goos == "windows" {
					assert.Equal(t, "zip", a.Kind)
					assert.Equal(t, 0, a.Strip)
				} else {
					assert.Equal(t, "tgz", a.Kind)
					assert.Equal(t, 1, a.Strip)
				}
			}
			assert.Equal(t, tt.want, names)
		})
	}
	_, err := LlamaCppArtifacts("linux", "riscv64", GPUNone, false)
	assert.ErrorIs(t, err, ErrNoLlamaBuild)
}

func TestSharedCUDADirAndEnv(t *testing.T) {
	sd := t.TempDir()
	assert.Empty(t, SharedCUDADir(sd, "linux"))
	require.NoError(t, os.WriteFile(filepath.Join(sd, "libcudart.so.12"), []byte("x"), 0o644))
	assert.Equal(t, sd, SharedCUDADir(sd, "linux"))
	assert.Empty(t, SharedCUDADir(sd, "windows"))

	t.Setenv("LD_LIBRARY_PATH", "/usr/lib/x")
	env := LlamaEnv(map[string]string{"A": "b"}, "/data/engines/llama-cpp/llama-server", sd, "linux")
	sep := string(os.PathListSeparator)
	assert.Equal(t, strings.Join([]string{"/data/engines/llama-cpp", sd, "/usr/lib/x"}, sep), env["LD_LIBRARY_PATH"])
	assert.Equal(t, "b", env["A"])
	assert.NotContains(t, LlamaEnv(nil, "/x/llama-server", sd, "darwin"), "LD_LIBRARY_PATH")
}

func tgzWithSymlink(t *testing.T, link string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(h *tar.Header, body string) {
		require.NoError(t, tw.WriteHeader(h))
		if body != "" {
			_, err := tw.Write([]byte(body))
			require.NoError(t, err)
		}
	}
	add(&tar.Header{Name: "llama-b12283/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "llama-b12283/" + exe("llama-server"), Typeflag: tar.TypeReg, Mode: 0o755, Size: 3}, "bin")
	add(&tar.Header{Name: "llama-b12283/libmtmd.so.0.9.0", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3}, "lib")
	add(&tar.Header{Name: "llama-b12283/libmtmd.so.0", Typeflag: tar.TypeSymlink, Linkname: link}, "")
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func llamaInstallCfg(t *testing.T) *config.Config {
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.ApplyInstallDefaults()
	cfg.ResolvePaths()
	cfg.ApplyLLMBackend()
	return &cfg
}

func TestInstallLlamaReleaseWithSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks in tar are Linux/macOS only")
	}
	srv := newRTServer(t)
	body := tgzWithSymlink(t, "libmtmd.so.0.9.0")
	cfg := llamaInstallCfg(t)
	inst := NewInstaller(cfg)
	inst.LlamaArtifacts = func() ([]Artifact, error) {
		return []Artifact{{Name: "llama.tgz", URL: srv.put("/llama.tgz", body), SHA256: rtSum(body), Size: int64(len(body)), Kind: "tgz", Dest: config.LlamaCppDir, Strip: 1}}, nil
	}
	inst.ReleaseArtifacts = func() ([]Artifact, error) { t.Fatal("sd.cpp artifacts must not be used for llm"); return nil, nil }

	require.NoError(t, inst.Install(config.LlamaCppProcessKey))
	dir := filepath.Join(cfg.DataDir, "engines", "llama-cpp")
	got, err := os.Readlink(filepath.Join(dir, "libmtmd.so.0"))
	require.NoError(t, err)
	assert.Equal(t, "libmtmd.so.0.9.0", got)
	st, err := os.Stat(filepath.Join(dir, "llama-server"))
	require.NoError(t, err)
	assert.NotZero(t, st.Mode()&0o100)
	assert.True(t, inst.IsInstalled(config.LlamaCppProcessKey))
	mark, err := os.ReadFile(filepath.Join(dir, ".installed"))
	require.NoError(t, err)
	assert.Equal(t, config.LlamaCppVersion, strings.TrimSpace(string(mark)))
}

func TestTarSymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks in tar are Linux/macOS only")
	}
	for _, link := range []string{"../../etc/passwd", "/etc/passwd"} {
		t.Run(link, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "a.tgz")
			require.NoError(t, os.WriteFile(p, tgzWithSymlink(t, link), 0o644))
			dest := filepath.Join(t.TempDir(), "out")
			err := extractArchive(p, "tgz", dest, 1)
			assert.ErrorContains(t, err, "escapes")
			assert.NoDirExists(t, dest)
		})
	}
}
