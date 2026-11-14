package installer

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeHost struct {
	goos    string
	smi     map[string]string
	files   map[string]bool
	render  []string
	globErr error
	env     map[string]string
	ran     []string
}

func (h *fakeHost) probe() gpuProbe {
	return gpuProbe{
		goos: h.goos,
		run: func(name string, args ...string) ([]byte, error) {
			h.ran = append(h.ran, name)
			out, ok := h.smi[name]
			if !ok {
				return nil, errors.New("executable file not found")
			}
			return []byte(out), nil
		},
		exists: func(p string) bool { return h.files[p] },
		glob:   func(string) ([]string, error) { return h.render, h.globErr },
		getenv: func(k string) string { return h.env[k] },
	}
}

func TestGPUProbeKind(t *testing.T) {
	t.Parallel()
	winSMI := filepath.Join(`C:\Program Files`, "NVIDIA Corporation", "NVSMI", "nvidia-smi.exe")
	sys32SMI := filepath.Join(`C:\Windows`, "System32", "nvidia-smi.exe")
	vulkanDLL := filepath.Join(`C:\Windows`, "System32", "vulkan-1.dll")
	nvidiaList := "GPU 0: NVIDIA GeForce RTX 3060 (UUID: GPU-1234)\n"
	tests := []struct {
		name    string
		host    fakeHost
		want    GPUKind
		wantRan string
	}{
		{
			name:    "linux nvidia",
			host:    fakeHost{goos: "linux", smi: map[string]string{"nvidia-smi": nvidiaList}},
			want:    GPUNvidia,
			wantRan: "nvidia-smi",
		},
		{
			name: "linux nvidia-smi without gpus falls back to vulkan",
			host: fakeHost{goos: "linux", smi: map[string]string{"nvidia-smi": "No devices were found\n"},
				render: []string{"/dev/dri/renderD128"}, files: map[string]bool{"/usr/lib64/libvulkan.so.1": true}},
			want: GPUVulkan,
		},
		{
			name: "linux vulkan",
			host: fakeHost{goos: "linux", render: []string{"/dev/dri/renderD128"},
				files: map[string]bool{"/usr/lib/x86_64-linux-gnu/libvulkan.so.1": true}},
			want: GPUVulkan,
		},
		{
			name: "linux render node without vulkan loader",
			host: fakeHost{goos: "linux", render: []string{"/dev/dri/renderD128"}},
			want: GPUNone,
		},
		{
			name: "linux vulkan loader without render node",
			host: fakeHost{goos: "linux", files: map[string]bool{"/usr/lib/libvulkan.so.1": true}},
			want: GPUNone,
		},
		{
			name: "linux render node lookup fails",
			host: fakeHost{goos: "linux", globErr: filepath.ErrBadPattern,
				files: map[string]bool{"/usr/lib/libvulkan.so.1": true}},
			want: GPUNone,
		},
		{
			name: "windows nvidia-smi in Program Files",
			host: fakeHost{goos: "windows", env: map[string]string{"ProgramW6432": `C:\Program Files`},
				files: map[string]bool{winSMI: true}, smi: map[string]string{winSMI: nvidiaList}},
			want:    GPUNvidia,
			wantRan: winSMI,
		},
		{
			name: "windows nvidia-smi in System32",
			host: fakeHost{goos: "windows", env: map[string]string{"ProgramFiles": `C:\Program Files`, "SystemRoot": `C:\Windows`},
				files: map[string]bool{sys32SMI: true}, smi: map[string]string{sys32SMI: nvidiaList}},
			want:    GPUNvidia,
			wantRan: sys32SMI,
		},
		{
			name: "windows vulkan",
			host: fakeHost{goos: "windows", env: map[string]string{"SystemRoot": `C:\Windows`},
				files: map[string]bool{vulkanDLL: true}},
			want:    GPUVulkan,
			wantRan: "nvidia-smi",
		},
		{
			name: "windows without SystemRoot",
			host: fakeHost{goos: "windows", files: map[string]bool{vulkanDLL: true}},
			want: GPUNone,
		},
		{
			name: "darwin has neither",
			host: fakeHost{goos: "darwin", render: []string{"/dev/dri/renderD128"},
				files: map[string]bool{"/usr/lib/libvulkan.so.1": true}},
			want: GPUNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := tt.host
			assert.Equal(t, tt.want, h.probe().kind())
			if tt.wantRan != "" {
				assert.Equal(t, []string{tt.wantRan}, h.ran)
			}
		})
	}
}
