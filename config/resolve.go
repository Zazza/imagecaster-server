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
		bc.EmbeddingDir = resolveDirPath(c.DataDir, bc.EmbeddingDir)
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
	if backend.Kind == KindSDCpp {
		args := withDirFlag(backend.Args, "--lora-model-dir", backend.LoraDir)
		args = withDirFlag(args, "--hires-upscalers-dir", backend.UpscalerDir)
		proc.Args = sdcppArgsOnPort(args, targetPort(proc.TargetURL, 7860))
	} else if backend.AutoOptimize {
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
	proc.Env = c.uvRuntimeEnv(proc.Env, backend.Binary)
	c.Processes[backend.ProcessKey] = proc
}

func (c *Config) uvRuntimeEnv(env map[string]string, binary string) map[string]string {
	out := make(map[string]string, len(env)+3)
	for k, v := range env {
		out[k] = v
	}
	set := func(k, v string) {
		if _, ok := out[k]; !ok && v != "" {
			out[k] = v
		}
	}
	set("VIRTUAL_ENV", venvOf(binary))
	if c.DataDir != "" {
		set("UV_CACHE_DIR", filepath.Join(c.DataDir, "uv-cache"))
		set("UV_PYTHON_INSTALL_DIR", filepath.Join(c.DataDir, "python"))
	}
	return out
}

func venvOf(binary string) string {
	if !filepath.IsAbs(binary) {
		return ""
	}
	switch strings.ToLower(filepath.Base(binary)) {
	case "python", "python.exe", "python3", "python3.exe":
	default:
		return ""
	}
	dir := filepath.Dir(binary)
	switch strings.ToLower(filepath.Base(dir)) {
	case "bin", "scripts":
	default:
		return ""
	}
	root := filepath.Dir(dir)
	switch strings.ToLower(filepath.Base(root)) {
	case "venv", ".venv":
		return root
	}
	if _, err := os.Stat(filepath.Join(root, "pyvenv.cfg")); err == nil {
		return root
	}
	return ""
}

func (c *Config) adoptForgeModels(prevSD string) {
	sd, ok := c.Backends[SDCppBackendKey]
	if !ok {
		return
	}
	var forge *BackendConfig
	keys := []string{c.ActiveSD, prevSD, NeoBackendKey}
	rest := make([]string, 0, len(c.Backends))
	for key := range c.Backends {
		rest = append(rest, key)
	}
	sort.Strings(rest)
	for _, key := range append(keys, rest...) {
		if b, ok := c.Backends[key]; ok && b.Kind != KindSDCpp && dirExists(b.ModelsDir) {
			forge = &b
			break
		}
	}
	if forge == nil {
		return
	}
	root := filepath.Dir(forge.ModelsDir)
	sd.ModelsDir = forge.ModelsDir
	sd.LoraDir = orDefault(forge.LoraDir, filepath.Join(root, "Lora"))
	sd.VaeDir = orDefault(forge.VaeDir, filepath.Join(root, "VAE"))
	sd.TextEncoderDir = filepath.Join(root, "text_encoder")
	sd.TokenizerDir = sd.TextEncoderDir
	sd.PoseModelsDir = filepath.Join(root, "ControlNetPreprocessor", "openpose")
	sd.UpscalerDir = filepath.Join(root, "ESRGAN")
	c.Backends[SDCppBackendKey] = sd
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (c *Config) migrateLegacy() {
	delete(c.Processes, "python")
	delete(c.Processes, "rembg")
	if c.ActiveSD == "forge" {
		delete(c.Backends, "forge")
	}
	if _, ok := c.Backends[c.ActiveSD]; !ok {
		c.ActiveSD = NeoBackendKey
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
		if c.inactiveLLM(key) {
			continue
		}
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

		if key == LlamaCppProcessKey {
			pc.Args = llamaArgsOnPort(pc.Args, internal)
		}
		if pc.ProxyPath == "/api/sd/" {
			if c.SDCppActive(key) {
				pc.Args = sdcppArgsOnPort(pc.Args, internal)
			} else {
				pc.Args = forgeArgsOnPort(pc.Args, internal)
			}
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
