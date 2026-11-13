package config

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
		bc.TextEncoderDir = resolveDirPath(c.DataDir, bc.TextEncoderDir)
		bc.UpscalerDir = resolveDirPath(c.DataDir, bc.UpscalerDir)
		bc.TokenizerDir = resolveDirPath(c.DataDir, bc.TokenizerDir)
		bc.PoseModelsDir = resolveDirPath(c.DataDir, bc.PoseModelsDir)
		bc.OnnxRuntimeLib = resolveRelPath(c.DataDir, bc.OnnxRuntimeLib)
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
	if bc, ok := c.Backends[SDCppBackendKey]; ok && bc.Install.Method == InstallRelease {
		bc.Install.Version = SDCppVersion
		c.Backends[SDCppBackendKey] = bc
	}
	for key, def := range InstallDefaultsBackends {
		if bc, ok := c.Backends[key]; ok && bc.Install.Method == "" {
			if key == SDCppBackendKey && bc.Binary != "" && bc.Binary != sdcppBackend().Binary {
				continue
			}
			bc.Install = def
			c.Backends[key] = bc
		}
	}
	c.pinLlamaVersion()
	c.pinEngineDirs()
	for key, def := range InstallDefaultsProcesses {
		if pc, ok := c.Processes[key]; ok && pc.Install.Method == "" {
			pc.Install = def
			c.Processes[key] = pc
		}
	}
}

// pinEngineDirs — пути движков sd.cpp и llama.cpp в старых конфигах указывают
// на каталоги без суффикса платформы; обновляем их на суффиксированные,
// иначе общая папка данных затирается чужой ОС.
func (c *Config) pinEngineDirs() {
	if bc, ok := c.Backends[SDCppBackendKey]; ok {
		bc.Binary = replaceEnginePath(bc.Binary, c.DataDir, SDCppDirLegacy, SDCppDir)
		bc.WorkDir = replaceEnginePath(bc.WorkDir, c.DataDir, SDCppDirLegacy, SDCppDir)
		bc.Install.Target = replaceEnginePath(bc.Install.Target, c.DataDir, SDCppDirLegacy, SDCppDir)
		c.Backends[SDCppBackendKey] = bc
	}
	if pc, ok := c.Processes[LlamaCppProcessKey]; ok {
		pc.Binary = replaceEnginePath(pc.Binary, c.DataDir, LlamaCppDirLegacy, LlamaCppDir)
		pc.Install.Target = replaceEnginePath(pc.Install.Target, c.DataDir, LlamaCppDirLegacy, LlamaCppDir)
		c.Processes[LlamaCppProcessKey] = pc
	}
}

// replaceEnginePath заменяет путь (относительный или абсолютный внутри dataDir),
// ведущий в старый каталог движка, на новый; чужие пути не трогает.
func replaceEnginePath(p, dataDir, legacy, next string) string {
	if p == "" {
		return p
	}
	clean := filepath.Clean(p)
	for _, base := range []string{filepath.Clean(legacy), filepath.Clean(filepath.Join(dataDir, legacy))} {
		if clean == base || strings.HasPrefix(clean, base+string(filepath.Separator)) {
			tail := strings.TrimPrefix(strings.TrimPrefix(clean, base), string(filepath.Separator))
			if filepath.IsAbs(clean) {
				return filepath.Join(dataDir, next, tail)
			}
			return filepath.Join(next, tail)
		}
	}
	return p
}

func (c *Config) ApplyBackendToProcess() {
	backend, ok := c.Backends[c.ActiveSD]
	if !ok || backend.Kind != KindSDCpp {
		return
	}
	proc, ok := c.Processes[backend.ProcessKey]
	if !ok {
		return
	}
	proc.Binary = backend.Binary
	proc.HealthURL = sdHealthURL(proc.HealthURL)
	args := withDirFlag(backend.Args, "--lora-model-dir", backend.LoraDir)
	args = withDirFlag(args, "--hires-upscalers-dir", backend.UpscalerDir)
	proc.Args = sdcppArgsOnPort(args, targetPort(proc.TargetURL, 7860))
	proc.WorkDir = backend.WorkDir
	if proc.Install.Method == "" && backend.Install.Method != "" {
		proc.Install = backend.Install
	}
	c.Processes[backend.ProcessKey] = proc
}

const (
	sdcppHealthPath  = "/sdcpp/v1/capabilities"
	legacyHealthPath = "/sdapi/v1/options"
)

func sdHealthURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Path != legacyHealthPath {
		return raw
	}
	u.Path = sdcppHealthPath
	return u.String()
}

func (c *Config) migrateLegacy() {
	delete(c.Processes, "python")
	delete(c.Processes, "rembg")
	if _, ok := c.Backends[c.ActiveSD]; !ok {
		c.ActiveSD = SDCppBackendKey
	}
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

		if pc.Env != nil {
			env := make(map[string]string, len(pc.Env))
			for k, v := range pc.Env {
				env[k] = v
			}
			pc.Env = env
		}

		if key == LlamaCppProcessKey {
			pc.Args = llamaArgsOnPort(pc.Args, internal)
		}
		if pc.ProxyPath == "/api/sd/" && c.SDCppActive(key) {
			pc.Args = sdcppArgsOnPort(pc.Args, internal)
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

func (c *Config) SDCppActive(processKey string) bool {
	b, ok := c.Backends[c.ActiveSD]
	return ok && b.Kind == KindSDCpp && b.ProcessKey == processKey
}

func targetPort(raw string, def int) int {
	u, err := url.Parse(raw)
	if err != nil {
		return def
	}
	if p, err := strconv.Atoi(u.Port()); err == nil && p > 0 {
		return p
	}
	return def
}

func withDirFlag(args []string, flag, dir string) []string {
	if dir == "" {
		return args
	}
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return args
		}
	}
	return append(slices.Clone(args), flag, dir)
}

func sdcppArgsOnPort(args []string, port int) []string {
	out := make([]string, 0, len(args)+4)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--listen-ip", a == "-l", a == "--listen-port":
			i++
		case strings.HasPrefix(a, "--listen-ip="), strings.HasPrefix(a, "--listen-port="):
		default:
			out = append(out, a)
		}
	}
	return append(out, "--listen-ip", "127.0.0.1", "--listen-port", strconv.Itoa(port))
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
