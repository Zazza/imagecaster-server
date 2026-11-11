package config

import (
	"maps"
	"sync"
)

type InstallMethod string

const (
	InstallZip InstallMethod = "zip"
)

type Config struct {
	Port           int                      `yaml:"port"`
	MDNS           bool                     `yaml:"mdns"`
	Lang           string                   `yaml:"lang,omitempty"`
	DataDir        string                   `yaml:"data_dir"`
	ActiveSD       string                   `yaml:"active_sd"`
	DetectedVRAMMB int                      `yaml:"detected_vram_mb"`
	Processes      map[string]ProcessConfig `yaml:"processes"`
	Backends       map[string]BackendConfig `yaml:"backends"`
	GPU            GPUConfig                `yaml:"gpu"`
}

type GPUConfig struct {
	TotalBudgetMB     int  `yaml:"total_budget_mb"`
	ReserveMB         int  `yaml:"reserve_mb"`
	MaxWaitSeconds    int  `yaml:"max_wait_seconds"`
	LeaseTTLSeconds   int  `yaml:"lease_ttl_seconds"`
	SDDefaultWeightMB int  `yaml:"sd_default_weight_mb"`
	SDOverheadMB      int  `yaml:"sd_overhead_mb"`
	LLMWeightMB       int  `yaml:"llm_weight_mb"`
	GuardPorts        bool `yaml:"guard_ports"`
}

func (g *GPUConfig) ResolveBudget(detectedVRAMMB int) int {
	if g.TotalBudgetMB > 0 {
		return g.TotalBudgetMB
	}
	if detectedVRAMMB <= 0 {
		return 0
	}
	return detectedVRAMMB - g.ReserveMB
}

type InstallConfig struct {
	Method  InstallMethod `yaml:"method"`
	URL     string        `yaml:"url"`
	Target  string        `yaml:"target"`
	Version string        `yaml:"version"`
}

type ProcessConfig struct {
	Name         string            `yaml:"name"`
	Binary       string            `yaml:"binary"`
	Args         []string          `yaml:"args"`
	Env          map[string]string `yaml:"env"`
	WorkDir      string            `yaml:"workdir"`
	HealthURL    string            `yaml:"health_url"`
	TargetURL    string            `yaml:"target_url"`
	ProxyPath    string            `yaml:"proxy_path"`
	InternalPort int               `yaml:"internal_port"`
	AutoStart    bool              `yaml:"autostart"`
	Restart      bool              `yaml:"restart"`
	MaxRestart   int               `yaml:"max_restart"`
	Install      InstallConfig     `yaml:"install"`
	Category     string            `yaml:"category"`
}

const KindSDCpp = "sdcpp"

type BackendConfig struct {
	Name           string              `yaml:"name"`
	Kind           string              `yaml:"kind,omitempty"`
	TextEncoderDir string              `yaml:"text_encoder_dir,omitempty"`
	UpscalerDir    string              `yaml:"upscaler_dir,omitempty"`
	TokenizerDir   string              `yaml:"tokenizer_dir,omitempty"`
	PoseModelsDir  string              `yaml:"pose_models_dir,omitempty"`
	OnnxRuntimeLib string              `yaml:"onnxruntime_lib,omitempty"`
	Aliases        map[string][]string `yaml:"aliases,omitempty"`
	ProcessKey     string              `yaml:"process_key"`
	Binary         string              `yaml:"binary"`
	Args           []string            `yaml:"args"`
	WorkDir        string              `yaml:"workdir"`
	ModelsDir      string              `yaml:"models_dir"`
	LoraDir        string              `yaml:"lora_dir"`
	VaeDir         string              `yaml:"vae_dir"`
	EmbeddingDir   string              `yaml:"embedding_dir"`
	AutoOptimize   bool                `yaml:"auto_optimize"`
	Install        InstallConfig       `yaml:"install"`
}

func (c *Config) GetActiveBackend() *BackendConfig {
	b, ok := c.Backends[c.ActiveSD]
	if !ok {
		return nil
	}
	return &b
}

var procMu sync.RWMutex

func (c *Config) Process(key string) (ProcessConfig, bool) {
	procMu.RLock()
	defer procMu.RUnlock()
	pc, ok := c.Processes[key]
	return pc, ok
}

func (c *Config) SetProcess(key string, pc ProcessConfig) {
	procMu.Lock()
	defer procMu.Unlock()
	c.Processes[key] = pc
}

func (c *Config) ProcessesCopy() map[string]ProcessConfig {
	procMu.RLock()
	defer procMu.RUnlock()
	return maps.Clone(c.Processes)
}
