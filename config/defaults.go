package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	SDCppBackendKey = "sdcpp"
	SDCppVersion    = "master-1086-465adf3"
	OnnxVersion     = "1.31.1"
	SDCppDirLegacy  = "engines/sd-cpp"
	onnxDir         = "engines/onnxruntime"
)

// Каталоги движков sd.cpp и llama.cpp получают суффикс платформы:
// папка данных может быть общим томом у Windows и Linux, а сборки
// движков несовместимы между ОС — каждая держит свою.
var SDCppDir = engineDir(SDCppDirLegacy)

// EngineDirFor — путь каталога движка с суффиксом указанной ОС.
func EngineDirFor(dir, goos string) string {
	return dir + "-" + goos
}

// engineDir — путь каталога движка с суффиксом текущей ОС.
func engineDir(dir string) string {
	return EngineDirFor(dir, runtime.GOOS)
}

var InstallDefaultsBackends map[string]InstallConfig
var InstallDefaultsProcesses map[string]InstallConfig

func init() {
	InstallDefaultsBackends = map[string]InstallConfig{
		SDCppBackendKey: sdcppInstall(),
	}
	InstallDefaultsProcesses = map[string]InstallConfig{
		LlamaCppProcessKey: llamaInstall(),
	}
}

func sdcppInstall() InstallConfig {
	return InstallConfig{Method: InstallRelease, Target: SDCppDir, Version: SDCppVersion}
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
		Binary:         SDCppDir + "/sd-server" + exeSuffix(),
		WorkDir:        SDCppDir,
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
		ActiveSD:  SDCppBackendKey,
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
				HealthURL:    "http://localhost:7860/sdcpp/v1/capabilities",
				TargetURL:    "http://localhost:7860",
				ProxyPath:    "/api/sd/",
				InternalPort: 7861,
				AutoStart:    true,
				Restart:      true,
				MaxRestart:   5,
			},
			LlamaCppProcessKey: llamaProcess(),
		},
		Backends: map[string]BackendConfig{
			SDCppBackendKey: sdcppBackend(),
		},
	}
}
