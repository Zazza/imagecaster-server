package main

import (
	"context"
	"fmt"
	"imagecaster-server/config"
	sdgpu "imagecaster-server/gpu"
	"imagecaster-server/gpuqueue"
	sdhealth "imagecaster-server/health"
	"imagecaster-server/installer"
	"imagecaster-server/llamacpp"
	"imagecaster-server/models"
	"imagecaster-server/process"
	"imagecaster-server/train"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

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
	train  *train.Manager
	stop   sync.Once
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
	sdw := newSDWeight(gateCfg, dirResolver(modelsDir))
	gate := NewGate(q, gateCfg, sdw)

	proxy := NewProxyHandler(cfg, gate)
	sdRoutes := newSDCppHandler(cfg, pm, gm)
	if sdRoutes != nil {
		sdw.resolve = sdRoutes.jobs.CheckpointFiles
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
	var tm *train.Manager
	if sdRoutes != nil {
		handlers.sdCheckpoint = sdRoutes.jobs.LoadedCheckpoint
		tm = newTrainManager(cfg, gate, sdRoutes.jobs)
	}
	if tm != nil {
		handlers.trainAvailable = tm.Available
	}
	if lf != nil {
		handlers.llmRunning = lf.RunningNames
	}
	mux := setupMux(handlers, inst, mm, bm, proxy)
	gpuqueue.NewAPI(q).RegisterRoutes(mux)
	if tm != nil {
		mux.Handle("/api/train/", tm)
	}

	for _, msg := range guardWarns {
		q.Warn(msg)
	}
	guards := startHeldPortGuards(guardSpecs, guardHeld, proxy, q)

	return &appDeps{
		cfg: cfg, inst: inst, pm: pm, hm: hm, gm: gm,
		guards: guards, mux: withLLMServerRoutes(lf, mux), q: q, llm: lf, train: tm,
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

const (
	readHeaderTimeout = 30 * time.Second
	idleTimeout       = 120 * time.Second
)

func (d *appDeps) newServer() *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", d.cfg.Port),
		Handler:           corsMiddleware(d.mux),
		ReadTimeout:       0,
		WriteTimeout:      0,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
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
	d.stop.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.guards.shutdown(ctx)
		if d.train != nil {
			d.train.Close()
		}
		d.inst.Close()
		d.pm.Close()
	})
}

type appRuntime struct {
	d       *appDeps
	ctx     context.Context
	cancel  context.CancelFunc
	srv     *http.Server
	mdns    *MDNSService
	grace   time.Duration
	serveCh chan error
	stop    sync.Once
}

func listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf(":%d", port))
}

func startRuntime(cfg *config.Config, ln net.Listener, grace time.Duration) *appRuntime {
	d := initApp(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	r := &appRuntime{d: d, ctx: ctx, cancel: cancel, srv: d.newServer(), grace: grace, serveCh: make(chan error, 1)}
	go func() {
		err := r.srv.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
			r.serveCh <- err
		}
	}()
	r.mdns = d.registerMDNS()
	return r
}

func (r *appRuntime) serveErr() <-chan error {
	return r.serveCh
}

func (r *appRuntime) launch(withMonitors bool) {
	r.d.ensureInstalled()
	if r.ctx.Err() != nil {
		return
	}
	r.d.pm.StartAll()
	if withMonitors {
		r.d.startMonitors(r.ctx)
	}
}

func (r *appRuntime) stopBackendsFirst() {
	r.cancel()
	r.d.shutdown()
	r.mdns.Shutdown()
	r.Stop()
}

func (r *appRuntime) Stop() {
	r.stop.Do(func() {
		r.cancel()
		ctx, done := context.WithTimeout(context.Background(), r.grace)
		defer done()
		if err := r.srv.Shutdown(ctx); err != nil {
			log.Printf("HTTP shutdown: %v", err)
		}
		r.d.shutdown()
		r.mdns.Shutdown()
	})
}

func runHeadless(cfg *config.Config) {
	log.Printf("Imagecaster Server starting on port %d (backend: %s)", cfg.Port, cfg.ActiveSD)
	log.Printf("Data directory: %s", cfg.DataDir)

	ln, err := listen(cfg.Port)
	if err != nil {
		log.Fatalf("Server error: %v", err)
	}
	r := startRuntime(cfg, ln, 30*time.Second)
	go r.launch(true)
	log.Printf("HTTP server listening on :%d", cfg.Port)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-r.serveErr():
		r.Stop()
		log.Fatalf("Server error: %v", err)
	case <-sigCh:
	}
	log.Println("Shutting down...")
	r.Stop()
	log.Println("Server stopped")
}
