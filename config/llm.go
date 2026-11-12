package config

import (
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	LLMLlamaCpp        = "llamacpp"
	LLMOllama          = "ollama"
	LlamaCppProcessKey = "llm"
	LlamaCppVersion    = "b12283"
	LlamaCppDir        = "engines/llama-cpp"
	LlamaPresetName    = "llamacpp-models.ini"
	llamaPublicPort    = 11434
)

type LLMConfig struct {
	ContextSize int `yaml:"context_size,omitempty"`
	ModelsMax   int `yaml:"models_max,omitempty"`
}

func defaultLLMConfig() LLMConfig {
	return LLMConfig{ContextSize: 8192, ModelsMax: 1}
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

func (c *Config) LLMActive() string {
	if c.ActiveLLM == LLMOllama {
		return LLMOllama
	}
	return LLMLlamaCpp
}

func (c *Config) LLMProcessKey() string {
	if c.LLMActive() == LLMOllama {
		return "ollama"
	}
	return LlamaCppProcessKey
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
	return l
}

func (c *Config) LlamaKeepAlive(vramMB int) string {
	if vramMB > 0 && vramMB < 24000 {
		return "5m"
	}
	if pc, ok := c.Processes["ollama"]; ok && pc.Env["OLLAMA_KEEP_ALIVE"] != "" {
		return pc.Env["OLLAMA_KEEP_ALIVE"]
	}
	return "5m"
}

func (c *Config) PinLlamaInternal() {
	pc, ok := c.Processes[LlamaCppProcessKey]
	if !ok || c.LLMProcessKey() != LlamaCppProcessKey {
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

func (c *Config) OllamaModelsDir() string {
	if pc, ok := c.Processes["ollama"]; ok {
		if d := pc.Env["OLLAMA_MODELS"]; d != "" {
			return resolveDirPath(c.DataDir, d)
		}
	}
	return filepath.Join(c.DataDir, "models", "ollama")
}

func (c *Config) pinLlamaVersion() {
	if pc, ok := c.Processes[LlamaCppProcessKey]; ok && pc.Install.Method == InstallRelease && pc.Install.Version != LlamaCppVersion {
		pc.Install.Version = LlamaCppVersion
		c.Processes[LlamaCppProcessKey] = pc
	}
}

func (c *Config) ApplyLLMBackend() {
	active := c.LLMProcessKey()
	if active == LlamaCppProcessKey {
		if _, ok := c.Processes[LlamaCppProcessKey]; !ok {
			pc := llamaProcess()
			pc.Binary = resolveRelPath(c.DataDir, pc.Binary)
			pc.Install.Target = resolveInstallTarget(c.DataDir, pc.Install)
			c.Processes[LlamaCppProcessKey] = pc
		}
	}
	for _, key := range []string{"ollama", LlamaCppProcessKey} {
		pc, ok := c.Processes[key]
		if !ok || key == active {
			continue
		}
		pc.AutoStart = false
		pc.ProxyPath = ""
		pc.Install = InstallConfig{}
		c.Processes[key] = pc
	}
	pc, ok := c.Processes[LlamaCppProcessKey]
	if !ok || active != LlamaCppProcessKey {
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

func (c *Config) inactiveLLM(key string) bool {
	if c.ActiveLLM == "" {
		return false
	}
	return (key == "ollama" || key == LlamaCppProcessKey) && key != c.LLMProcessKey()
}
