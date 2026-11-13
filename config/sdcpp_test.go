package config

import (
	"time"

	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSDCppBackendListensOnInternalPort(t *testing.T) {
	cfg := NewDefault()
	cfg.ActiveSD = "sdcpp"
	cfg.Backends["sdcpp"] = BackendConfig{
		Name: "sd.cpp", Kind: KindSDCpp, ProcessKey: "sd", Binary: "/opt/sd-server",
		Args: []string{"--lora-model-dir", "/m/Lora", "--listen-port", "9999"},
	}
	cfg.ApplyBackendToProcess()
	pc := cfg.Processes["sd"]
	assert.Equal(t, []string{"--lora-model-dir", "/m/Lora", "--listen-ip", "127.0.0.1", "--listen-port", "7860"}, pc.Args)

	guards := cfg.ApplyPortGuard()
	assert.NotEmpty(t, guards)
	pc = cfg.Processes["sd"]
	assert.Equal(t, []string{"--lora-model-dir", "/m/Lora", "--listen-ip", "127.0.0.1", "--listen-port", "7861"}, pc.Args)
	assert.Equal(t, "http://127.0.0.1:7861", pc.TargetURL)
}

func TestNonSDCppBackendLeavesProcessArgs(t *testing.T) {
	cfg := NewDefault()
	cfg.Backends["custom"] = BackendConfig{Name: "x", Kind: "other", ProcessKey: "sd", Args: []string{"--x"}}
	cfg.ActiveSD = "custom"
	before := cfg.Processes["sd"].Args
	cfg.ApplyBackendToProcess()
	assert.Equal(t, before, cfg.Processes["sd"].Args)
}

func TestSDCppBackendPassesLoraAndUpscalerDirs(t *testing.T) {
	cfg := NewDefault()
	cfg.ActiveSD = "sdcpp"
	cfg.Backends["sdcpp"] = BackendConfig{
		Name: "sd.cpp", Kind: KindSDCpp, ProcessKey: "sd", Binary: "/opt/sd-server",
		LoraDir: "/m/Lora", UpscalerDir: "/m/up",
	}
	cfg.ApplyBackendToProcess()
	assert.Equal(t, []string{"--lora-model-dir", "/m/Lora", "--hires-upscalers-dir", "/m/up", "--listen-ip", "127.0.0.1", "--listen-port", "7860"}, cfg.Processes["sd"].Args)

	cfg.Backends["sdcpp"] = BackendConfig{
		Name: "sd.cpp", Kind: KindSDCpp, ProcessKey: "sd", Binary: "/opt/sd-server",
		LoraDir: "/m/Lora", Args: []string{"--lora-model-dir=/other"},
	}
	cfg.ApplyBackendToProcess()
	assert.Equal(t, []string{"--lora-model-dir=/other", "--listen-ip", "127.0.0.1", "--listen-port", "7860"}, cfg.Processes["sd"].Args)
}

func TestSDHealthURLRewritesLegacyPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://localhost:7860/sdapi/v1/options", "http://localhost:7860/sdcpp/v1/capabilities"},
		{"http://localhost:7860/sdcpp/v1/capabilities", "http://localhost:7860/sdcpp/v1/capabilities"},
		{"http://localhost:7860/custom", "http://localhost:7860/custom"},
		{"", ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, sdHealthURL(tc.in), tc.in)
	}
}

func TestSDJobResultTTL(t *testing.T) {
	c := &Config{}
	if got := c.SDJobResultTTL(); got != 24*time.Hour {
		t.Errorf("default %v", got)
	}
	c.SD.JobResultTTLHours = 6
	if got := c.SDJobResultTTL(); got != 6*time.Hour {
		t.Errorf("custom %v", got)
	}
}
