package config

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func (c *Config) MergeProcessEnvDefaults() {
	defaults := NewDefault()
	for key, defProc := range defaults.Processes {
		curProc, ok := c.Processes[key]
		if !ok {
			continue
		}
		if curProc.Env == nil {
			curProc.Env = make(map[string]string)
		}
		for k, v := range defProc.Env {
			if _, exists := curProc.Env[k]; !exists {
				curProc.Env[k] = v
			}
		}
		if curProc.Category == "" && defProc.Category != "" {
			curProc.Category = defProc.Category
		}
		if defProc.Binary != "" && defProc.Binary != curProc.Binary {
			curProc.Binary = defProc.Binary
		}
		if len(defProc.Args) > 0 {
			curProc.Args = defProc.Args
		}
		c.Processes[key] = curProc
	}
}

func (c *Config) ResolvePaths() {
	for key, pc := range c.Processes {
		pc.Binary = resolveRelPath(c.DataDir, pc.Binary)
		pc.WorkDir = resolveDirPath(c.DataDir, pc.WorkDir)
		pc.Install.Target = resolveInstallTarget(c.DataDir, pc.Install)
		for ek, ev := range pc.Env {
			pc.Env[ek] = resolveRelPath(c.DataDir, ev)
		}
		c.Processes[key] = pc
	}
	for key, bc := range c.Backends {
		bc.Binary = resolveRelPath(c.DataDir, bc.Binary)
		bc.WorkDir = resolveDirPath(c.DataDir, bc.WorkDir)
		bc.ModelsDir = resolveDirPath(c.DataDir, bc.ModelsDir)
		bc.LoraDir = resolveDirPath(c.DataDir, bc.LoraDir)
		bc.VaeDir = resolveDirPath(c.DataDir, bc.VaeDir)
		bc.EmbeddingDir = resolveDirPath(c.DataDir, bc.EmbeddingDir)
		bc.Install.Target = resolveInstallTarget(c.DataDir, bc.Install)
		c.Backends[key] = bc
	}
}

func (c *Config) ApplyEnvOverrides() {
	if v := os.Getenv("SD_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Port = p
		}
	}
	if v := os.Getenv("SD_ACTIVE_BACKEND"); v != "" {
		c.ActiveSD = v
	}
	if v := os.Getenv("SD_DATA_DIR"); v != "" {
		c.DataDir = v
	}
}

func (c *Config) ApplyInstallDefaults() {
	for key, def := range InstallDefaultsBackends {
		if bc, ok := c.Backends[key]; ok && bc.Install.Method == "" {
			bc.Install = def
			c.Backends[key] = bc
		}
	}
	for key, def := range InstallDefaultsProcesses {
		if pc, ok := c.Processes[key]; ok && pc.Install.Method == "" {
			pc.Install = def
			c.Processes[key] = pc
		}
	}
}

type GPUOptimizer interface {
	DetectGPU() int
	CheckXformersCompat(binary, workDir string) bool
	ForgeArgsForVRAM(vramMB int, xformers bool) []string
}

func (c *Config) ApplyBackendToProcess(gpu GPUOptimizer) {
	backend, ok := c.Backends[c.ActiveSD]
	if !ok {
		return
	}
	proc, ok := c.Processes[backend.ProcessKey]
	if !ok {
		return
	}
	proc.Binary = backend.Binary
	if backend.AutoOptimize {
		vram := gpu.DetectGPU()
		c.DetectedVRAMMB = vram
		useXformers := gpu.CheckXformersCompat(proc.Binary, backend.WorkDir)
		proc.Args = gpu.ForgeArgsForVRAM(vram, useXformers)
	} else {
		proc.Args = backend.Args
	}
	proc.WorkDir = backend.WorkDir
	if proc.Install.Method == "" && backend.Install.Method != "" {
		proc.Install = backend.Install
	}
	c.Processes[backend.ProcessKey] = proc
}

type PortGuard struct {
	Process    string
	ListenAddr string
	ProxyPath  string
}

func (c *Config) ApplyPortGuard() []PortGuard {
	return c.ApplyPortGuardIf(nil)
}

func (c *Config) ApplyPortGuardIf(allow func(PortGuard) bool) []PortGuard {
	if !c.GPU.GuardPorts {
		return nil
	}
	keys := make([]string, 0, len(c.Processes))
	for k := range c.Processes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var guards []PortGuard
	for _, key := range keys {
		pc := c.Processes[key]
		if !pc.AutoStart || pc.ProxyPath == "" || pc.TargetURL == "" {
			continue
		}
		target, err := url.Parse(pc.TargetURL)
		if err != nil {
			continue
		}
		host, portStr, err := net.SplitHostPort(target.Host)
		if err != nil || !isLoopbackHost(host) {
			continue
		}
		public, err := strconv.Atoi(portStr)
		if err != nil || public <= 0 {
			continue
		}
		internal := pc.InternalPort
		if internal <= 0 {
			internal = public + 1
		}
		if internal == public {
			continue
		}
		guard := PortGuard{
			Process:    key,
			ListenAddr: ":" + portStr,
			ProxyPath:  pc.ProxyPath,
		}
		if allow != nil && !allow(guard) {
			continue
		}
		internalHost := net.JoinHostPort("127.0.0.1", strconv.Itoa(internal))

		target.Host = internalHost
		pc.TargetURL = target.String()
		pc.HealthURL = moveLoopbackPort(pc.HealthURL, public, internalHost)

		env := make(map[string]string, len(pc.Env)+1)
		for k, v := range pc.Env {
			env[k] = v
		}
		_, hasOllamaHost := env["OLLAMA_HOST"]
		if hasOllamaHost || pc.ProxyPath == "/api/llm/" {
			env["OLLAMA_HOST"] = internalHost
		}
		pc.Env = env

		if pc.ProxyPath == "/api/sd/" {
			pc.Args = forgeArgsOnPort(pc.Args, internal)
		}

		c.Processes[key] = pc
		guards = append(guards, guard)
	}
	return guards
}

func isLoopbackHost(host string) bool {
	switch host {
	case "", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func moveLoopbackPort(raw string, public int, internalHost string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil || !isLoopbackHost(host) || portStr != strconv.Itoa(public) {
		return raw
	}
	u.Host = internalHost
	return u.String()
}

func forgeArgsOnPort(args []string, port int) []string {
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--listen":
		case a == "--port", a == "--server-name":
			i++
		case strings.HasPrefix(a, "--port="), strings.HasPrefix(a, "--server-name="):
		default:
			out = append(out, a)
		}
	}
	return append(out, "--port", strconv.Itoa(port))
}

func resolveInstallTarget(baseDir string, ic InstallConfig) string {
	if ic.Target == "" || filepath.IsAbs(ic.Target) {
		return ic.Target
	}
	return filepath.Join(baseDir, ic.Target)
}

func resolveRelPath(baseDir, p string) string {
	if p == "" || filepath.IsAbs(p) || !strings.ContainsRune(p, '/') {
		return p
	}
	return filepath.Join(baseDir, p)
}

func resolveDirPath(baseDir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}
