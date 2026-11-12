package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	NeoBackendKey   = "forge-neo"
	NeoVersion      = "2.31"
	neoDir          = "sd-webui-forge-neo"
	SDCppBackendKey = "sdcpp"
	SDCppVersion    = "master-1086-465adf3"
	OnnxVersion     = "1.31.1"
	sdcppDir        = "engines/sd-cpp"
	onnxDir         = "engines/onnxruntime"
)

var InstallDefaultsBackends map[string]InstallConfig
var InstallDefaultsProcesses map[string]InstallConfig

func init() {
	InstallDefaultsBackends = map[string]InstallConfig{
		NeoBackendKey:   neoInstall(),
		SDCppBackendKey: sdcppInstall(),
	}
	InstallDefaultsProcesses = map[string]InstallConfig{
		"ollama":           {},
		LlamaCppProcessKey: llamaInstall(),
	}
}

func neoInstall() InstallConfig {
	return InstallConfig{
		Method:  InstallZip,
		URL:     "https://github.com/Haoming02/sd-webui-forge-classic/archive/refs/tags/" + NeoVersion + ".zip",
		Target:  neoDir,
		Version: NeoVersion,
	}
}

func SDCppSupported(goos, goarch string) bool {
	switch goos + "/" + goarch {
	case "windows/amd64", "linux/amd64", "darwin/arm64":
		return true
	}
	return false
}

func defaultActiveSD(goos, goarch string) string {
	if SDCppSupported(goos, goarch) {
		return SDCppBackendKey
	}
	return NeoBackendKey
}

func sdcppInstall() InstallConfig {
	return InstallConfig{Method: InstallRelease, Target: sdcppDir, Version: SDCppVersion}
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func onnxLibName() string {
	switch runtime.GOOS {
	case "windows":
		return "onnxruntime.dll"
	case "darwin":
		return "libonnxruntime." + OnnxVersion + ".dylib"
	}
	return "libonnxruntime.so." + OnnxVersion
}

func sdcppBackend() BackendConfig {
	return BackendConfig{
		Name:           "stable-diffusion.cpp",
		Kind:           KindSDCpp,
		ProcessKey:     "sd",
		Binary:         sdcppDir + "/sd-server" + exeSuffix(),
		WorkDir:        sdcppDir,
		ModelsDir:      "models/checkpoints",
		LoraDir:        "models/loras",
		VaeDir:         "models/vae",
		TextEncoderDir: "models/text_encoders",
		TokenizerDir:   "models/text_encoders",
		UpscalerDir:    "models/upscalers",
		PoseModelsDir:  "models/openpose",
		OnnxRuntimeLib: onnxDir + "/lib/" + onnxLibName(),
		Install:        sdcppInstall(),
	}
}

func neoBinary() string {
	if runtime.GOOS == "windows" {
		return neoDir + "/venv/Scripts/python.exe"
	}
	return neoDir + "/venv/bin/python"
}

func sdEnv() map[string]string {
	env := map[string]string{
		"PYTORCH_CUDA_ALLOC_CONF":         "expandable_segments:True",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM": "1",
		"GIT_CONFIG_COUNT":                "1",
		"GIT_CONFIG_KEY_0":                "safe.directory",
		"GIT_CONFIG_VALUE_0":              "*",
	}
	if runtime.GOOS == "darwin" {
		env["TORCH_COMMAND"] = "pip install torch==2.12.0 torchvision==0.27.0"
	}
	return env
}

const (
	dataDirName       = "imagecaster-server"
	legacyDataDirName = "sd-studio-server"
)

func DefaultDataDir() string {
	if d := SavedDataDir(); d != "" {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return dataDirName
	}
	return dataDirIn(home)
}

func dataDirPointer() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, dataDirName, "data-dir")
}

func SavedDataDir() string {
	p := dataDirPointer()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func SaveDataDir(dir string) error {
	p := dataDirPointer()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(dir+"\n"), 0o644)
}

func dataDirIn(home string) string {
	dir := filepath.Join(home, dataDirName)
	if _, err := os.Stat(dir); err == nil {
		return dir
	}
	legacy := filepath.Join(home, legacyDataDirName)
	if info, err := os.Stat(legacy); err == nil && info.IsDir() {
		return legacy
	}
	return dir
}

func NewDefault() Config {
	return Config{
		Port:      8080,
		MDNS:      true,
		DataDir:   "",
		ActiveSD:  defaultActiveSD(runtime.GOOS, runtime.GOARCH),
		ActiveLLM: LLMLlamaCpp,
		LLM:       defaultLLMConfig(),
		GPU: GPUConfig{
			TotalBudgetMB:     0,
			ReserveMB:         500,
			MaxWaitSeconds:    120,
			LeaseTTLSeconds:   90,
			SDDefaultWeightMB: 11000,
			SDOverheadMB:      4500,
			LLMWeightMB:       11000,
			GuardPorts:        true,
		},
		Processes: map[string]ProcessConfig{
			"sd": {
				Name:         "Stable Diffusion",
				HealthURL:    "http://localhost:7860/sdapi/v1/options",
				TargetURL:    "http://localhost:7860",
				ProxyPath:    "/api/sd/",
				InternalPort: 7861,
				AutoStart:    true,
				Restart:      true,
				MaxRestart:   5,
				Env:          sdEnv(),
			},
			"ollama": {
				Name:   "Ollama",
				Binary: "ollama",
				Args:   []string{"serve"},
				Env: map[string]string{
					"OLLAMA_HOST":         "0.0.0.0:11434",
					"OLLAMA_MODELS":       "models/ollama",
					"OLLAMA_KEEP_ALIVE":   "24h",
					"OLLAMA_NUM_PARALLEL": "2",
					"OLLAMA_NUM_GPU":      "-1",
				},
				HealthURL:    "http://localhost:11434/api/tags",
				TargetURL:    "http://localhost:11434",
				ProxyPath:    "/api/llm/",
				InternalPort: 11435,
				AutoStart:    true,
				Restart:      true,
				MaxRestart:   5,
				Install:      InstallConfig{},
			},
			LlamaCppProcessKey: llamaProcess(),
		},
		Backends: map[string]BackendConfig{
			SDCppBackendKey: sdcppBackend(),
			NeoBackendKey: {
				Name:         "Stable Diffusion Forge Neo",
				ProcessKey:   "sd",
				Binary:       neoBinary(),
				Args:         []string{"launch.py", "--listen", "--api", "--uv"},
				WorkDir:      neoDir,
				ModelsDir:    neoDir + "/models/Stable-diffusion",
				LoraDir:      neoDir + "/models/Lora",
				VaeDir:       neoDir + "/models/VAE",
				EmbeddingDir: neoDir + "/embeddings",
				Install:      neoInstall(),
			},
		},
	}
}
