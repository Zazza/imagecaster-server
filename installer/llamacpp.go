package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"imagecaster-server/config"
)

const llamaBase = "https://github.com/ggml-org/llama.cpp/releases/download/" + config.LlamaCppVersion + "/"

var ErrNoLlamaBuild = fmt.Errorf("no llama.cpp build for this platform")

func llamaArt(name, sha string, size int64, kind string, strip int) Artifact {
	return Artifact{Name: name, URL: llamaBase + name, SHA256: sha, Size: size, Kind: kind, Dest: config.LlamaCppDir, Strip: strip}
}

func LlamaCppArtifacts(goos, goarch string, gpu GPUKind, sharedCUDA bool) ([]Artifact, error) {
	switch {
	case goos == "windows" && goarch == "amd64" && gpu == GPUNvidia:
		arts := []Artifact{llamaArt("llama-b12283-bin-win-cuda-12.4-x64.zip", "b5a412657addd63beffeaf9179b0ce3dac3f436951aed573132864e2cd7630f0", 288972616, "zip", 0)}
		if !sharedCUDA {
			arts = append(arts, llamaArt("cudart-llama-bin-win-cuda-12.4-x64.zip", "8c79a9b226de4b3cacfd1f83d24f962d0773be79f1e7b75c6af4ded7e32ae1d6", 391443627, "zip", 0))
		}
		return arts, nil
	case goos == "windows" && goarch == "amd64" && gpu == GPUVulkan:
		return []Artifact{llamaArt("llama-b12283-bin-win-vulkan-x64.zip", "58c3426f7473bc08135c83333259366521fc940e4ad7bc6ad2b26755cc42cb03", 38022362, "zip", 0)}, nil
	case goos == "windows" && goarch == "amd64":
		return []Artifact{llamaArt("llama-b12283-bin-win-cpu-x64.zip", "dafb19ac757bfb7cfc1f91c6e26703264df861c4331e3ef6dc9543ae8d010c5e", 25058174, "zip", 0)}, nil
	case goos == "linux" && goarch == "amd64" && gpu == GPUNvidia:
		arts := []Artifact{llamaArt("llama-b12283-bin-ubuntu-cuda-12.8-x64.tar.gz", "0334bd73d3e5e6e5dd751724986e8e696f14e2849443b313562babe3e1eef6ce", 188353916, "tgz", 1)}
		if !sharedCUDA {
			arts = append(arts, llamaArt("cudart-llama-b12283-bin-ubuntu-cuda-12.8-x64.tar.gz", "44f5e6f622a13396c86e30dee6c9f723b403aadb2300839d499d87b0da27e0fa", 594371475, "tgz", 1))
		}
		return arts, nil
	case goos == "linux" && goarch == "amd64" && gpu == GPUVulkan:
		return []Artifact{llamaArt("llama-b12283-bin-ubuntu-vulkan-x64.tar.gz", "34138a7d99f2cc4b2be8b6978a656eb77240e742ba0870a58d418024c6e2e8b8", 36106160, "tgz", 1)}, nil
	case goos == "linux" && goarch == "amd64":
		return []Artifact{llamaArt("llama-b12283-bin-ubuntu-x64.tar.gz", "5f24a62b84a37c186efd1e3e1d77bfad3a1772d9aa35bfcc9e1572723d0c4faf", 23283834, "tgz", 1)}, nil
	case goos == "darwin" && goarch == "arm64":
		return []Artifact{llamaArt("llama-b12283-bin-macos-arm64.tar.gz", "bbc2abe2001293687540fb274bf4114b27b56af1fc166ee76730f64996d68b00", 16870708, "tgz", 1)}, nil
	case goos == "darwin" && goarch == "amd64":
		return []Artifact{llamaArt("llama-b12283-bin-macos-x64.tar.gz", "7179905c572cd11e9eadf9669058b49846cc9519f3d8d10497cad19fbf9eb8b0", 12613034, "tgz", 1)}, nil
	}
	return nil, fmt.Errorf("%w: %s/%s", ErrNoLlamaBuild, goos, goarch)
}

func cudaLibName(goos string) string {
	if goos == "windows" {
		return "cudart64_12.dll"
	}
	return "libcudart.so.12"
}

func SDCppEngineDir(cfg *config.Config) string {
	if b, ok := cfg.Backends[config.SDCppBackendKey]; ok && b.Install.Target != "" {
		if filepath.IsAbs(b.Install.Target) {
			return b.Install.Target
		}
		return filepath.Join(cfg.DataDir, b.Install.Target)
	}
	return filepath.Join(cfg.DataDir, "engines", "sd-cpp")
}

func SharedCUDADir(sdcppDir, goos string) string {
	if sdcppDir != "" && fileExists(filepath.Join(sdcppDir, cudaLibName(goos))) {
		return sdcppDir
	}
	return ""
}

func (inst *Installer) artifactsFor(key string) ([]Artifact, error) {
	if key != config.LlamaCppProcessKey {
		return inst.releaseArtifacts()
	}
	if inst.LlamaArtifacts != nil {
		return inst.LlamaArtifacts()
	}
	gpu := detectGPUKind()
	return LlamaCppArtifacts(runtime.GOOS, runtime.GOARCH, gpu, SharedCUDADir(SDCppEngineDir(inst.config), runtime.GOOS) != "")
}

func LlamaEnv(env map[string]string, binary, sdcppDir, goos string) map[string]string {
	out := make(map[string]string, len(env)+1)
	for k, v := range env {
		out[k] = v
	}
	dirs := []string{filepath.Dir(binary)}
	if d := SharedCUDADir(sdcppDir, goos); d != "" {
		dirs = append(dirs, d)
	}
	key := "LD_LIBRARY_PATH"
	switch goos {
	case "windows":
		key = "PATH"
	case "darwin":
		return out
	}
	cur := out[key]
	if cur == "" {
		cur = os.Getenv(key)
	}
	if cur != "" {
		dirs = append(dirs, cur)
	}
	out[key] = strings.Join(dirs, string(os.PathListSeparator))
	return out
}
