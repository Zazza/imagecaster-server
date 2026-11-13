package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"imagecaster-server/attach"
	"imagecaster-server/config"
	sdgpu "imagecaster-server/gpu"
	"imagecaster-server/gpuqueue"
	sdhealth "imagecaster-server/health"
	"imagecaster-server/i18n"
	"imagecaster-server/installer"
	"imagecaster-server/llamacpp"
	"imagecaster-server/models"
	"imagecaster-server/process"
	"imagecaster-server/tui"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "attach" {
		if err := attach.Main(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "attach:", err)
			os.Exit(1)
		}
		return
	}

	dataDir := flag.String("data", "", "data directory (default: ~/imagecaster-server)")
	configPath := flag.String("config", "", "config file path (default: {data-dir}/server-config.yaml)")
	port := flag.Int("port", 0, "override server port")
	headless := flag.Bool("headless", false, "run without TUI (log output only)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if desktopBuild && !*headless {
		dir := *dataDir
		if dir != "" {
			dir, _ = filepath.Abs(dir)
		}
		runDesktop(dir, *configPath, *port)
		return
	}

	dir := *dataDir
	if dir == "" {
		dir = config.DefaultDataDir()
	}
	dir, _ = filepath.Abs(dir)

	cfgFile := *configPath
	if cfgFile == "" {
		cfgFile = filepath.Join(dir, configName)
	}

	_, configErr := os.Stat(cfgFile)
	firstRun := configErr != nil

	isTerminal := term.IsTerminal(int(os.Stdin.Fd()))
	useTUI := !*headless && isTerminal

	if useTUI && firstRun {
		runTUI(&config.Config{DataDir: dir}, cfgFile, true, *port)
		return
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("Failed to create data directory %s: %v", dir, err)
	}

	cfg, err := config.LoadWithDir(cfgFile, dir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	i18n.Set(cfg.Lang)

	if *port != 0 {
		cfg.Port = *port
	}

	if useTUI {
		runTUI(cfg, cfgFile, false, *port)
	} else {
		runHeadless(cfg)
	}
}

type appDeps struct {
	cfg    *config.Config
	inst   *installer.Installer
	pm     *process.ProcessManager
	hm     *sdhealth.HealthMonitor
	gm     *sdgpu.GPUMonitor
	guards *portGuards
	mux    http.Handler
	q      *gpuqueue.Queue
	llm    *llamacpp.Facade
}

func initApp(cfg *config.Config) *appDeps {
	cfg.ApplyBackendToProcess()
	cfg.ApplyLLMBackend()
	applyLlamaEnv(cfg)
	if cfg.DetectedVRAMMB == 0 {
		cfg.DetectedVRAMMB = sdgpu.DetectVRAMMB()
	}
	guardSpecs, guardWarns, guardHeld := holdPortGuards(cfg)
	cfg.PinLlamaInternal()

	inst := installer.NewInstaller(cfg)
	pm := process.NewProcessManager(cfg, inst, cfg.DataDir)
	hm := sdhealth.NewHealthMonitor(cfg)
	gm := sdgpu.NewGPUMonitor()
	mm := models.NewModelManager(cfg)
	bm := NewBackendManager(cfg)

	budget := cfg.GPU.ResolveBudget(cfg.DetectedVRAMMB)
	q := gpuqueue.New(gpuqueue.Config{
		TotalBudgetMB: budget,
		MaxWait:       time.Duration(cfg.GPU.MaxWaitSeconds) * time.Second,
		LeaseTTL:      time.Duration(cfg.GPU.LeaseTTLSeconds) * time.Second,
	})
	log.Printf("GPU queue: budget %dMB (detected VRAM %dMB)", budget, cfg.DetectedVRAMMB)

	gateCfg := GateConfig{
		SDDefaultWeightMB: cfg.GPU.SDDefaultWeightMB,
		SDOverheadMB:      cfg.GPU.SDOverheadMB,
		LLMWeightMB:       cfg.GPU.LLMWeightMB,
		MaxWaitSeconds:    cfg.GPU.MaxWaitSeconds,
	}
	modelsDir := ""
	if backend := cfg.GetActiveBackend(); backend != nil {
		modelsDir = backend.ModelsDir
	}
	sdw := newSDWeight(gateCfg, mm, modelsDir)
	gate := NewGate(q, gateCfg, sdw)

	proxy := NewProxyHandler(pm, cfg, gate)
	sdRoutes := newSDCppHandler(cfg, pm, gm)
	if sdRoutes != nil {
		wireSDJobs(sdRoutes.jobs, gate)
		proxy.SetHandler("/api/sd/", sdRoutes)
		log.Printf("SD backend: sd.cpp facade on /api/sd/")
	}
	lf := newLlamaFacade(cfg, pm)
	if lf != nil {
		proxy.SetHandler("/api/llm/", lf)
		gate.SetBeforeSD(llmEvictor(lf, q.Budget, cfg.GPU.LLMWeightMB))
		log.Printf("LLM backend: llama.cpp on /api/llm/")
	}
	handlers := NewHandlers(pm, hm, gm, cfg, inst)
	if sdRoutes != nil {
		handlers.sdCheckpoint = sdRoutes.jobs.LoadedCheckpoint
	}
	if lf != nil {
		handlers.llmRunning = lf.RunningNames
	}
	mux := setupMux(handlers, inst, mm, bm, proxy)
	gpuqueue.NewAPI(q).RegisterRoutes(mux)

	for _, msg := range guardWarns {
		q.Warn(msg)
	}
	guards := startHeldPortGuards(guardSpecs, guardHeld, proxy, q)

	return &appDeps{
		cfg: cfg, inst: inst, pm: pm, hm: hm, gm: gm,
		guards: guards, mux: withLLMServerRoutes(lf, mux), q: q, llm: lf,
	}
}

func (d *appDeps) startMonitors(ctx context.Context) {
	go d.pm.Watch(ctx)
	go d.hm.Start(ctx)
	go d.gm.Start(ctx)
	d.q.Start(ctx)
	if d.llm != nil {
		d.llm.Start(ctx)
	}
}

func (d *appDeps) ensureInstalled() {
	d.inst.EnsureAllInstalled()
}

func (d *appDeps) newServer() *http.Server {
	return &http.Server{
		Addr:         fmt.Sprintf(":%d", d.cfg.Port),
		Handler:      corsMiddleware(d.mux),
		ReadTimeout:  0,
		WriteTimeout: 0,
	}
}

func (d *appDeps) registerMDNS() *MDNSService {
	mdns := NewMDNS(d.cfg.Port, d.cfg.MDNS, version)
	if err := mdns.Register(); err != nil {
		log.Printf("mDNS registration failed (non-fatal): %v", err)
	} else if d.cfg.MDNS {
		log.Printf("mDNS: registered _imagecaster._tcp on port %d", d.cfg.Port)
	}
	return mdns
}

func (d *appDeps) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.guards.shutdown(ctx)
	d.inst.Close()
	d.pm.Close()
}

func runHeadless(cfg *config.Config) {
	log.Printf("Imagecaster Server starting on port %d (backend: %s)", cfg.Port, cfg.ActiveSD)
	log.Printf("Data directory: %s", cfg.DataDir)

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatalf("Server error: %v", err)
	}

	d := initApp(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := d.newServer()
	mdns := d.registerMDNS()

	go func() {
		d.ensureInstalled()
		if ctx.Err() != nil {
			return
		}
		d.pm.StartAll()
		d.startMonitors(ctx)
	}()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		cancel()
		srv.Shutdown(shutdownCtx)
		d.shutdown()
		mdns.Shutdown()

		log.Println("Server stopped")
		os.Exit(0)
	}()

	log.Printf("HTTP server listening on :%d", cfg.Port)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
	select {}
}

func runTUI(cfg *config.Config, cfgFile string, firstRun bool, port int) {
	logCapture := process.NewLogCapture(300)
	log.SetOutput(logCapture)

	if firstRun {
		result := tui.RunWizard(cfg.DataDir)
		if result.DataDir == "" {
			return
		}
		result.DataDir = absDir(result.DataDir)

		if err := os.MkdirAll(result.DataDir, 0755); err != nil {
			log.Fatalf("Failed to create data directory: %v", err)
		}

		if cfgFile == filepath.Join(cfg.DataDir, configName) {
			cfgFile = filepath.Join(result.DataDir, configName)
		}
		freshCfg, err := firstRunConfig(cfgFile, result.DataDir)
		if err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}
		*cfg = *freshCfg
		if port != 0 {
			cfg.Port = port
		}
	}

	d := initApp(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := tui.ServerDeps{
		Port:    cfg.Port,
		DataDir: cfg.DataDir,
		EnsureAllInstalled: func() {
			d.ensureInstalled()
		},
		InstallStatus: func() map[string]tui.ComponentInstallStatus {
			statuses := d.inst.Status()
			result := make(map[string]tui.ComponentInstallStatus, len(statuses))
			for k, s := range statuses {
				result[k] = tui.ComponentInstallStatus{
					Key:        s.Key,
					Installed:  s.Installed,
					Installing: s.Installing,
					Progress:   s.Progress,
					Error:      s.Error,
					Version:    s.Version,
				}
			}
			return result
		},
		StartAll: func() {
			d.pm.StartAll()
		},
		StartMonitors: func() {
			d.startMonitors(ctx)
		},
		ProcStatus: func() map[string]tui.ServiceInfo {
			statuses := d.pm.Status()
			healthResults := d.hm.Results()
			result := make(map[string]tui.ServiceInfo, len(statuses))
			for k, ps := range statuses {
				si := tui.ServiceInfo{
					Name:     ps.Name,
					Status:   ps.Status,
					PID:      ps.PID,
					Uptime:   ps.Uptime,
					Category: ps.Category,
				}
				if hr, ok := healthResults[k]; ok {
					si.Healthy = hr.Healthy
					si.Latency = hr.LatencyMs
				}
				result[k] = si
			}
			return result
		},
		StartProc: func(name string) error {
			return d.pm.Start(name)
		},
		StopProc: func(name string) error {
			return d.pm.Stop(name)
		},
		RestartProc: func(name string) error {
			return d.pm.Restart(name)
		},
		ProcLogs: func(name string, lines int) []string {
			logs, err := d.pm.Logs(name, lines)
			if err != nil {
				return nil
			}
			return logs
		},
		GPUInfo: func() tui.GPUInfo {
			info := d.gm.Info()
			return tui.GPUInfo{
				Name:        info.Name,
				MemoryTotal: info.MemoryTotal,
				MemoryUsed:  info.MemoryUsed,
				Utilization: info.Utilization,
				Available:   info.Available,
			}
		},
		HealthResults: func() map[string]tui.HealthResult {
			results := d.hm.Results()
			out := make(map[string]tui.HealthResult, len(results))
			for k, v := range results {
				out[k] = tui.HealthResult{
					Healthy:   v.Healthy,
					LatencyMs: v.LatencyMs,
					Error:     v.Error,
				}
			}
			return out
		},
		ServerLogs: func() []string {
			return logCapture.Lines(100)
		},
		QueueSnapshot: func() tui.QueueSnapshot {
			return gpuQueueSnapshot(d.q.Status())
		},
		QueueMove: func(id string, dir int) error {
			if !d.q.Move(id, dir) {
				return fmt.Errorf("queue move %s: %w", id, gpuqueue.ErrNotFound)
			}
			return nil
		},
		QueueCancel: func(id string) error {
			if !d.q.Cancel(id) {
				return fmt.Errorf("queue cancel %s: %w", id, gpuqueue.ErrNotFound)
			}
			return nil
		},
	}

	model := tui.NewAppModel(deps)
	p := tea.NewProgram(model, tea.WithAltScreen())

	d.inst.OnProgress = func(key, progress string) {
		p.Send(tui.InstallProgressMsg(key, progress))
	}

	d.pm.OnChange = func() {
		p.Send(tui.ServicesChangeMsg{})
	}

	d.gm.OnUpdate = func(info sdgpu.GPUInfo) {
		p.Send(tui.GPUUpdateMsgFunc(tui.GPUInfo{
			Name:        info.Name,
			MemoryTotal: info.MemoryTotal,
			MemoryUsed:  info.MemoryUsed,
			Utilization: info.Utilization,
			Available:   info.Available,
		}))
	}

	srv := d.newServer()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
		d.shutdown()
	}()

	mdns := d.registerMDNS()

	if _, err := p.Run(); err != nil {
		log.Fatalf("TUI error: %v", err)
	}

	go func() {
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}()

	cancel()
	d.shutdown()
	mdns.Shutdown()
	os.Exit(0)
}

func gpuQueueSnapshot(st gpuqueue.Status) tui.QueueSnapshot {
	snap := tui.QueueSnapshot{
		Budget: st.Budget,
	}
	for _, w := range st.Warnings {
		snap.Warnings = append(snap.Warnings, gpuqueue.SanitizeClient(w))
	}
	for _, j := range st.Running {
		snap.Running = append(snap.Running, queueJobDTO(j))
	}
	for _, j := range st.Queue {
		snap.Waiting = append(snap.Waiting, queueJobDTO(j))
	}
	return snap
}

func queueJobDTO(j gpuqueue.Job) tui.QueueJob {
	return tui.QueueJob{
		ID:            j.ID,
		Kind:          string(j.Kind),
		Client:        gpuqueue.SanitizeClient(j.Client),
		WeightMB:      j.WeightMB,
		Priority:      j.Priority,
		SubmittedAt:   j.SubmittedAt,
		LeaseDeadline: j.LeaseDeadline,
	}
}

const configName = "server-config.yaml"

func absDir(dir string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[1:])
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func firstRunConfig(cfgFile, dataDir string) (*config.Config, error) {
	cfg := config.NewDefault()
	cfg.DataDir = dataDir
	cfg.ApplyBackendToProcess()
	cfg.ApplyInstallDefaults()
	cfg.ResolvePaths()
	if err := saveConfig(cfgFile, &cfg); err != nil {
		return nil, err
	}
	if err := config.SaveDataDir(dataDir); err != nil {
		log.Printf("data dir not remembered: %v", err)
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *config.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	header := []byte("# Imagecaster Server Configuration\n\n")
	return os.WriteFile(path, append(header, data...), 0644)
}

func setupMux(handlers *Handlers, inst *installer.Installer, mm *models.ModelManager, bm *BackendManager, proxy *ProxyHandler) *http.ServeMux {
	mux := http.NewServeMux()
	handlers.RegisterRoutes(mux)
	mm.RegisterRoutes(mux)
	mm.RegisterDeleteRoutes(mux)
	bm.RegisterRoutes(mux)
	inst.RegisterRoutes(mux)

	mux.Handle("/api/sd/", proxy)
	mux.Handle("/api/llm/", proxy)

	mux.HandleFunc("/", handlers.handleRoot)

	return mux
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/sd/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
