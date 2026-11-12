package gpu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSDCppArgsForVRAM(t *testing.T) {
	base := []string{"--listen-port", "7861"}
	reserve := []string{"--listen-port", "7861", "--max-vram", "-3"}
	tests := []struct {
		name   string
		args   []string
		vramMB int
		want   []string
	}{
		{name: "no gpu detected", args: base, vramMB: 0, want: base},
		{name: "4 GB", args: base, vramMB: 4096, want: reserve},
		{name: "6 GB", args: base, vramMB: 6144, want: reserve},
		{name: "8 GB", args: base, vramMB: 8188, want: reserve},
		{name: "12 GB", args: base, vramMB: 12282, want: reserve},
		{name: "just under 15 GiB", args: base, vramMB: 15359, want: reserve},
		{name: "16 GB card", args: base, vramMB: 16376, want: base},
		{name: "24 GB card", args: base, vramMB: 24564, want: base},
		{name: "user max-vram kept", args: []string{"--max-vram", "5"}, vramMB: 8192, want: []string{"--max-vram", "5"}},
		{name: "user auto-fit kept", args: []string{"--auto-fit", "off"}, vramMB: 8192, want: []string{"--auto-fit", "off"}},
		{name: "user params-backend kept", args: []string{"--params-backend", "cpu"}, vramMB: 8192, want: []string{"--params-backend", "cpu"}},
		{name: "user offload kept", args: []string{"--offload-to-cpu"}, vramMB: 8192, want: []string{"--offload-to-cpu"}},
		{name: "nil args", args: nil, vramMB: 8192, want: []string{"--max-vram", "-3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := append([]string(nil), tt.args...)
			got := SDCppArgsForVRAM(tt.args, tt.vramMB)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, in, tt.args, "input must not be modified")
		})
	}
}
