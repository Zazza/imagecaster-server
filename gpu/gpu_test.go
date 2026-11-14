package gpu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseNvidiaSMI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output string
		want   GPUInfo
		wantOK bool
	}{
		{
			name:   "single gpu",
			output: "NVIDIA GeForce RTX 3060, 12288, 1024, 11264, 37\n",
			want:   GPUInfo{Name: "NVIDIA GeForce RTX 3060", MemoryTotal: 12288, MemoryUsed: 1024, MemoryFree: 11264, Utilization: 37, Available: true},
			wantOK: true,
		},
		{
			name:   "windows line endings",
			output: "NVIDIA GeForce RTX 4090, 24564, 500, 24064, 5\r\n",
			want:   GPUInfo{Name: "NVIDIA GeForce RTX 4090", MemoryTotal: 24564, MemoryUsed: 500, MemoryFree: 24064, Utilization: 5, Available: true},
			wantOK: true,
		},
		{
			name:   "two gpus use the first one",
			output: "NVIDIA GeForce RTX 3090, 24576, 2000, 22576, 81\nNVIDIA GeForce GTX 1080, 8192, 100, 8092, 3\n",
			want:   GPUInfo{Name: "NVIDIA GeForce RTX 3090", MemoryTotal: 24576, MemoryUsed: 2000, MemoryFree: 22576, Utilization: 81, Available: true},
			wantOK: true,
		},
		{
			name:   "leading blank line",
			output: "\n  \nNVIDIA A100, 40960, 0, 40960, 0\n",
			want:   GPUInfo{Name: "NVIDIA A100", MemoryTotal: 40960, MemoryFree: 40960, Available: true},
			wantOK: true,
		},
		{
			name:   "utilization not available",
			output: "NVIDIA GeForce GTX 750 Ti, 2048, 300, 1748, [N/A]\n",
			want:   GPUInfo{Name: "NVIDIA GeForce GTX 750 Ti", MemoryTotal: 2048, MemoryUsed: 300, MemoryFree: 1748, Available: true},
			wantOK: true,
		},
		{
			name:   "utilization not supported",
			output: "Quadro K620, 2048, 10, 2038, [Not Supported]\n",
			want:   GPUInfo{Name: "Quadro K620", MemoryTotal: 2048, MemoryUsed: 10, MemoryFree: 2038, Available: true},
			wantOK: true,
		},
		{
			name:   "unified memory without totals",
			output: "NVIDIA GB10, [N/A], [N/A], [N/A], 12\n",
			want:   GPUInfo{Name: "NVIDIA GB10", Utilization: 12, Available: true},
			wantOK: true,
		},
		{name: "empty", output: "", wantOK: false},
		{name: "only whitespace", output: " \n\r\n", wantOK: false},
		{name: "too few fields", output: "NVIDIA, 12288, 1024\n", wantOK: false},
		{name: "too many fields", output: "NVIDIA, 1, 2, 3, 4, 5\n", wantOK: false},
		{name: "not a number", output: "NVIDIA, 12 GiB, 1024, 11264, 37\n", wantOK: false},
		{name: "error text", output: "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver.\n", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseNvidiaSMI(tt.output)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
