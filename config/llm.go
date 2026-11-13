package config

import (
	"log"
	"net"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	LLMLlamaCpp        = "llamacpp"
	legacyOllamaKey    = "ollama"
	LlamaCppProcessKey = "llm"
	LlamaCppVersion    = "b12283"
	LlamaCppDirLegacy  = "engines/llama-cpp"
	LlamaPresetName    = "llamacpp-models.ini"
	llamaPublicPort    = 11434
	defaultKeepAlive   = "24h"
)

// LlamaCppDir — суффикс платформы, чтобы Windows и Linux держали свои сборки.
var LlamaCppDir = engineDir(LlamaCppDirLegacy)

var llamaCppOnThisPlatform = func() bool { return LlamaCppSupported(runtime.GOOS, runtime.GOARCH) }

type LLMConfig struct {
	ContextSize int    `yaml:"context_size,omitempty"`
	ModelsMax   int    `yaml:"models_max,omitempty"`
	ModelsDir   string `yaml:"models_dir,omitempty"`
	KeepAlive   string `yaml:"keep_alive,omitempty"`
	ReadOllama  *bool  `yaml:"read_ollama_models,omitempty"`
}

func defaultLLMConfig() LLMConfig {
	return LLMConfig{ContextSize: 8192, ModelsMax: 1}
}

func LlamaCppSupported(goos, goarch string) bool {
	switch goos + "/" + goarch {
	case "windows/amd64", "linux/amd64", "darwin/arm64":
		return true
	}
	return false
}

func llamaInstall() InstallConfig {
	return InstallConfig{Method: InstallRelease, Target: LlamaCppDir, Version: LlamaCppVersion}
}

func llamaProcess() ProcessConfig {
	return ProcessConfig{
		Name:         "llama.cpp",
		Binary:       LlamaCppDir + "/llama-server" + exeSuffix(),
		HealthURL:    "http://localhost:11434/health",
		TargetURL:    "http://localhost:11434",
		ProxyPath:    "/api/llm/",
		InternalPort: 11435,
		AutoStart:    true,
		Restart:      true,
		MaxRestart:   5,
		Install:      llamaInstall(),
	}
}

func fileHasKey(data []byte, key string) bool {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw[key]
	return ok
}

func (c *Config) LlamaPresetPath() string {
	return filepath.Join(c.DataDir, LlamaPresetName)
}

func (c *Config) LLMSettings() LLMConfig {
	l := c.LLM
	def := defaultLLMConfig()
	if l.ContextSize <= 0 {
		l.ContextSize = def.ContextSize
	}
	if l.ModelsMax <= 0 {
		l.ModelsMax = def.ModelsMax
	}
	if l.KeepAlive == "" {
		l.KeepAlive = defaultKeepAlive
	}
	return l
}

func (c *Config) LlamaKeepAlive(vramMB int) string {
	if vramMB > 0 && vramMB < 24000 {
		return "5m"
	}
	return c.LLMSettings().KeepAlive
}

func (c *Config) PinLlamaInternal() {
	pc, ok := c.Processes[LlamaCppProcessKey]
	if !ok || !llamaCppOnThisPlatform() {
		return
	}
	internal := pc.InternalPort
	if internal <= 0 {
		internal = llamaPublicPort + 1
	}
	host := net.JoinHostPort("127.0.0.1", strconv.Itoa(internal))
	if u, err := url.Parse(pc.TargetURL); err == nil && u.Host != "" {
		u.Host = host
		pc.TargetURL = u.String()
	} else {
		pc.TargetURL = "http://" + host
	}
	if u, err := url.Parse(pc.HealthURL); err == nil && u.Host != "" {
		u.Host = host
		pc.HealthURL = u.String()
	}
	pc.Args = llamaArgsOnPort(pc.Args, internal)
	c.Processes[LlamaCppProcessKey] = pc
}

func (c *Config) LLMModelsDir() string {
	if c.LLM.ModelsDir != "" {
		return resolveDirPath(c.DataDir, c.LLM.ModelsDir)
	}
	return filepath.Join(c.DataDir, "models", "ollama")
}

func (c *Config) adoptOllamaProcess() {
	old, had := c.Processes[legacyOllamaKey]
	if had {
		if c.LLM.ModelsDir == "" && old.Env["OLLAMA_MODELS"] != "" {
			c.LLM.ModelsDir = resolveDirPath(c.DataDir, old.Env["OLLAMA_MODELS"])
		}
		if c.LLM.KeepAlive == "" && old.Env["OLLAMA_KEEP_ALIVE"] != "" {
			c.LLM.KeepAlive = old.Env["OLLAMA_KEEP_ALIVE"]
		}
		delete(c.Processes, legacyOllamaKey)
	}
	if c.ActiveLLM == legacyOllamaKey {
		log.Printf("[config] Ollama is no longer started; text models from %s run on llama.cpp", c.LLMModelsDir())
	}
	c.ActiveLLM = LLMLlamaCpp
}

func (c *Config) pinLlamaVersion() {
	if pc, ok := c.Processes[LlamaCppProcessKey]; ok && pc.Install.Method == InstallRelease && pc.Install.Version != LlamaCppVersion {
		pc.Install.Version = LlamaCppVersion
		c.Processes[LlamaCppProcessKey] = pc
	}
}

func (c *Config) ApplyLLMBackend() {
	pc, ok := c.Processes[LlamaCppProcessKey]
	if !ok {
		pc = llamaProcess()
		pc.Binary = resolveRelPath(c.DataDir, pc.Binary)
		pc.Install.Target = resolveInstallTarget(c.DataDir, pc.Install)
	}
	if !llamaCppOnThisPlatform() {
		pc.AutoStart = false
		pc.ProxyPath = ""
		pc.Install = InstallConfig{}
		c.Processes[LlamaCppProcessKey] = pc
		return
	}
	internal := pc.InternalPort
	if internal <= 0 {
		internal = llamaPublicPort + 1
	}
	pc.Args = llamaArgsOnPort(c.llamaArgs(pc.Args), internal)
	c.Processes[LlamaCppProcessKey] = pc
}

func (c *Config) llamaArgs(args []string) []string {
	s := c.LLMSettings()
	out := make([]string, 0, len(args)+6)
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--models-preset", a == "--models-max":
			i++
		case strings.HasPrefix(a, "--models-preset="), strings.HasPrefix(a, "--models-max="):
		default:
			out = append(out, a)
		}
	}
	return append(out, "--models-preset", c.LlamaPresetPath(), "--models-max", strconv.Itoa(s.ModelsMax))
}

func llamaArgsOnPort(args []string, port int) []string {
	out := make([]string, 0, len(args)+4)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--host", a == "--port":
			i++
		case strings.HasPrefix(a, "--host="), strings.HasPrefix(a, "--port="):
		default:
			out = append(out, a)
		}
	}
	return append(out, "--host", "127.0.0.1", "--port", strconv.Itoa(port))
}
