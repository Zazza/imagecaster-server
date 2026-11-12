//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"imagecaster-server/config"
	"imagecaster-server/desktop"
	"imagecaster-server/i18n"
)

const (
	desktopNeedGB  = 8
	consoleExeName = "imagecaster-server-console.exe"
)

type desktopEngine struct {
	cfgFile string
	port    int

	mu      sync.Mutex
	cfg     *config.Config
	d       *appDeps
	ctx     context.Context
	cancel  context.CancelFunc
	srv     *http.Server
	mdns    *MDNSService
	logFile *os.File
	logPath string
	working bool
}

func runDesktop(dataDir, cfgFile string, port int) {
	if err := desktop.BindSelfToJob(); err != nil {
		log.Printf("job object: %v", err)
	}
	known := dataDir
	if known == "" {
		known = knownDataDir(cfgFile)
	}
	e := &desktopEngine{cfgFile: cfgFile, port: port}
	err := desktop.Run(desktop.Options{
		Version:    version,
		DataDir:    known,
		DefaultDir: config.DefaultDataDir(),
		ConsoleExe: consoleExe(),
		NeedGB:     desktopNeedGB,
		Engine:     e,
	})
	if err != nil {
		desktop.ShowError(err.Error())
		os.Exit(1)
	}
}

func knownDataDir(cfgFile string) string {
	if cfgFile != "" {
		if _, err := os.Stat(cfgFile); err == nil {
			return filepath.Dir(cfgFile)
		}
	}
	dir := config.DefaultDataDir()
	if _, err := os.Stat(filepath.Join(dir, configName)); err == nil {
		return dir
	}
	return ""
}

func consoleExe() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	p := filepath.Join(filepath.Dir(self), consoleExeName)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func (e *desktopEngine) Start(dir string) error {
	logPath := filepath.Join(dir, "logs", "server.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	log.SetOutput(lf)
	log.Printf("Imagecaster Server %s starting (desktop), data dir %s", version, dir)

	fail := func(err error) error {
		log.Printf("start failed: %v", err)
		log.SetOutput(os.Stderr)
		lf.Close()
		return err
	}
	cfg, err := e.loadConfig(dir)
	if err != nil {
		return fail(err)
	}
	i18n.Set(cfg.Lang)
	if e.port != 0 {
		cfg.Port = e.port
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Printf("listen :%d: %v", cfg.Port, err)
		return fail(fmt.Errorf(i18n.T("desktop.port_busy"), cfg.Port))
	}

	d := initApp(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	srv := d.newServer()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()
	mdns := d.registerMDNS()

	e.mu.Lock()
	e.cfg, e.d, e.ctx, e.cancel, e.srv, e.mdns, e.logFile, e.logPath = cfg, d, ctx, cancel, srv, mdns, lf, logPath
	e.mu.Unlock()
	e.launch(true)
	return nil
}

func (e *desktopEngine) loadConfig(dir string) (*config.Config, error) {
	cfgFile := e.cfgFile
	if cfgFile == "" {
		cfgFile = filepath.Join(dir, configName)
	}
	if _, err := os.Stat(cfgFile); err != nil {
		return firstRunConfig(cfgFile, dir)
	}
	cfg, err := config.LoadWithDir(cfgFile, dir)
	if err != nil {
		return nil, err
	}
	if err := config.SaveDataDir(dir); err != nil {
		log.Printf("data dir not remembered: %v", err)
	}
	return cfg, nil
}

func (e *desktopEngine) launch(first bool) {
	e.mu.Lock()
	if e.working || e.d == nil {
		e.mu.Unlock()
		return
	}
	e.working = true
	d, ctx := e.d, e.ctx
	e.mu.Unlock()
	go func() {
		defer func() {
			e.mu.Lock()
			e.working = false
			e.mu.Unlock()
		}()
		d.ensureInstalled()
		if ctx.Err() != nil {
			return
		}
		d.pm.StartAll()
		if first {
			d.startMonitors(ctx)
		}
	}()
}

func (e *desktopEngine) Retry() {
	e.launch(false)
}

func (e *desktopEngine) LogPath() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.logPath
}

func (e *desktopEngine) Stop() {
	e.mu.Lock()
	d, cancel, srv, mdns, lf := e.d, e.cancel, e.srv, e.mdns, e.logFile
	e.d = nil
	e.mu.Unlock()
	if d == nil {
		return
	}
	cancel()
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	d.shutdown()
	mdns.Shutdown()
	log.Println("Server stopped")
	log.SetOutput(os.Stderr)
	lf.Close()
}

func (e *desktopEngine) Snapshot() desktop.Snapshot {
	e.mu.Lock()
	d, cfg := e.d, e.cfg
	e.mu.Unlock()
	if d == nil {
		return desktop.Snapshot{}
	}

	statuses := d.inst.Status()
	keys := make([]string, 0, len(statuses))
	for k := range statuses {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := componentRank(keys[i], cfg), componentRank(keys[j], cfg)
		return ri < rj || ri == rj && keys[i] < keys[j]
	})
	comps := make([]desktop.Component, 0, len(keys))
	installingKey := ""
	for _, k := range keys {
		s := statuses[k]
		c := desktop.Component{
			Key:      k,
			Label:    componentName(k),
			State:    desktop.ComponentState(s.Installed, s.Installing, s.Error),
			Progress: s.Progress,
			Error:    s.Error,
		}
		if installingKey == "" && (c.State == desktop.StateRun || c.State == desktop.StateFail) {
			installingKey = k
		}
		comps = append(comps, c)
	}

	if _, ok := statuses["ollama"]; !ok {
		if pc, ok := cfg.Process("ollama"); ok && pc.AutoStart && !d.inst.OllamaFound() {
			comps = append(comps, desktop.Component{Key: "ollama", Label: componentName("ollama"), State: desktop.StateWait})
		}
	}

	sdKey := "sd"
	if b := cfg.GetActiveBackend(); b != nil && b.ProcessKey != "" {
		sdKey = b.ProcessKey
	}
	ps := d.pm.Status()
	hr := d.hm.Results()
	svc := func(key string) string {
		st := ps[key].Status
		if pc, ok := cfg.Process(key); ok && st == "crashed" && pc.Restart && ps[key].Restarts < pc.MaxRestart {
			st = "starting"
		}
		return desktop.ServiceState(st, hr[key].Healthy)
	}

	snap := desktop.Snapshot{
		Components: comps,
		SD:         svc(sdKey),
		Ollama:     svc(cfg.LLMProcessKey()),
		Addr:       desktop.ConnectAddr(desktop.LANIPs(), cfg.Port),
	}
	snap.Phase, snap.Error = desktop.PhaseOf(comps, snap.SD)
	if installingKey != "" {
		snap.Lines = d.inst.Logs(installingKey, 60)
	} else if lines, err := d.pm.Logs(sdKey, 60); err == nil {
		snap.Lines = lines
	}
	q := d.q.Status()
	snap.GPUJobs = len(q.Running) + len(q.Queue)
	return snap
}

func componentRank(key string, cfg *config.Config) int {
	switch {
	case key == cfg.ActiveSD:
		return 0
	case key == "ollama", key == config.LlamaCppProcessKey:
		return 2
	}
	return 1
}

func componentName(key string) string {
	switch key {
	case config.NeoBackendKey:
		return "Forge Neo"
	case config.SDCppBackendKey:
		return "stable-diffusion.cpp"
	case "ollama":
		return "Ollama"
	case config.LlamaCppProcessKey:
		return "llama.cpp"
	}
	return key
}
