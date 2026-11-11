package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type noGPU struct{}

func (noGPU) DetectGPU() int                          { return 0 }
func (noGPU) CheckXformersCompat(string, string) bool { return false }
func (noGPU) ForgeArgsForVRAM(int, bool) []string     { return []string{"--forge-only"} }

func TestSDCppBackendListensOnInternalPort(t *testing.T) {
	cfg := NewDefault()
	cfg.ActiveSD = "sdcpp"
	cfg.Backends["sdcpp"] = BackendConfig{
		Name: "sd.cpp", Kind: KindSDCpp, ProcessKey: "sd", Binary: "/opt/sd-server",
		Args: []string{"--lora-model-dir", "/m/Lora", "--listen-port", "9999"}, AutoOptimize: true,
	}
	cfg.ApplyBackendToProcess(noGPU{})
	pc := cfg.Processes["sd"]
	assert.Equal(t, []string{"--lora-model-dir", "/m/Lora", "--listen-ip", "127.0.0.1", "--listen-port", "7860"}, pc.Args)

	guards := cfg.ApplyPortGuard()
	assert.NotEmpty(t, guards)
	pc = cfg.Processes["sd"]
	assert.Equal(t, []string{"--lora-model-dir", "/m/Lora", "--listen-ip", "127.0.0.1", "--listen-port", "7861"}, pc.Args)
	assert.Equal(t, "http://127.0.0.1:7861", pc.TargetURL)
}

func TestForgeBackendKeepsForgePortArgs(t *testing.T) {
	cfg := NewDefault()
	cfg.ApplyBackendToProcess(noGPU{})
	cfg.ApplyPortGuard()
	args := cfg.Processes["sd"].Args
	assert.Equal(t, []string{"--port", "7861"}, args[len(args)-2:])
	assert.NotContains(t, args, "--listen-port")
}

func TestSDCppBackendPassesLoraAndUpscalerDirs(t *testing.T) {
	cfg := NewDefault()
	cfg.ActiveSD = "sdcpp"
	cfg.Backends["sdcpp"] = BackendConfig{
		Name: "sd.cpp", Kind: KindSDCpp, ProcessKey: "sd", Binary: "/opt/sd-server",
		LoraDir: "/m/Lora", UpscalerDir: "/m/up",
	}
	cfg.ApplyBackendToProcess(noGPU{})
	assert.Equal(t, []string{"--lora-model-dir", "/m/Lora", "--hires-upscalers-dir", "/m/up", "--listen-ip", "127.0.0.1", "--listen-port", "7860"}, cfg.Processes["sd"].Args)

	cfg.Backends["sdcpp"] = BackendConfig{
		Name: "sd.cpp", Kind: KindSDCpp, ProcessKey: "sd", Binary: "/opt/sd-server",
		LoraDir: "/m/Lora", Args: []string{"--lora-model-dir=/other"},
	}
	cfg.ApplyBackendToProcess(noGPU{})
	assert.Equal(t, []string{"--lora-model-dir=/other", "--listen-ip", "127.0.0.1", "--listen-port", "7860"}, cfg.Processes["sd"].Args)
}
