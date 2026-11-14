package main

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/mem"

	"imagecaster-server/config"
	"imagecaster-server/dwpose"
	sdgpu "imagecaster-server/gpu"
	"imagecaster-server/process"
	"imagecaster-server/sdcpp"
)

type processRestarter struct {
	pm  *process.ProcessManager
	cfg *config.Config
	key string
}

func (r *processRestarter) RestartWithArgs(args []string) error {
	pc, ok := r.cfg.Process(r.key)
	if !ok {
		return fmt.Errorf("process %q not configured", r.key)
	}
	pc.Args = args
	r.cfg.SetProcess(r.key, pc)
	r.pm.UpdateProcessConfig(r.key, pc)
	if err := r.pm.Stop(r.key); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !process.IsAlive(r.pm.Status()[r.key].Status) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("[sdcpp] restart %s: %s", r.key, strings.Join(args, " "))
	if err := r.pm.Start(r.key); err != nil {
		if process.IsAlive(r.pm.Status()[r.key].Status) {
			return nil
		}
		return err
	}
	return nil
}

func (r *processRestarter) Alive() bool {
	return process.IsAlive(r.pm.Status()[r.key].Status)
}

func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
	}
	return "", false
}

func effectiveDir(args []string, workDir, flag, dir string) string {
	if v, ok := flagValue(args, flag); ok {
		if !filepath.IsAbs(v) && workDir != "" {
			return filepath.Join(workDir, v)
		}
		return v
	}
	return dir
}

type gpuMemory struct {
	gm *sdgpu.GPUMonitor
}

func (g *gpuMemory) Memory() (sdcpp.MemoryStats, error) {
	var out sdcpp.MemoryStats
	if vm, err := mem.VirtualMemory(); err == nil {
		out.RAM = sdcpp.Pool{Free: float64(vm.Available), Used: float64(vm.Used), Total: float64(vm.Total)}
	}
	if g.gm != nil {
		info := g.gm.Info()
		if info.Available && info.MemoryTotal > 0 {
			const mb = 1024 * 1024
			out.CUDA = &sdcpp.CUDA{System: sdcpp.Pool{
				Free:  float64(info.MemoryFree) * mb,
				Used:  float64(info.MemoryUsed) * mb,
				Total: float64(info.MemoryTotal) * mb,
			}}
		}
	}
	return out, nil
}

func poseModelsDir(b *config.BackendConfig) string {
	if b.PoseModelsDir != "" {
		return b.PoseModelsDir
	}
	if b.ModelsDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(b.ModelsDir), "ControlNetPreprocessor", "openpose")
}

type sdcppRoutes struct {
	facade http.Handler
	detect http.Handler
	jobs   *sdcpp.Facade
}

func (h *sdcppRoutes) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.TrimPrefix(r.URL.Path, "/") == "controlnet/detect" {
		h.detect.ServeHTTP(w, r)
		return
	}
	h.facade.ServeHTTP(w, r)
}

func newSDCppHandler(cfg *config.Config, pm *process.ProcessManager, gm *sdgpu.GPUMonitor) *sdcppRoutes {
	f := newSDCppFacade(cfg, pm, gm)
	if f == nil {
		return nil
	}
	b := cfg.GetActiveBackend()
	est := dwpose.NewONNX(dwpose.Config{LibPath: b.OnnxRuntimeLib, ModelsDir: poseModelsDir(b)})
	return &sdcppRoutes{facade: f, detect: dwpose.NewHandler(est), jobs: f}
}

func wireSDJobs(f *sdcpp.Facade, g *Gate) {
	f.SetLeaser(g.LeaseSD)
	f.SetPositionFunc(g.SDPosition)
	g.SetSDLeaseLost(func(jobID string) { f.CancelJob(jobID) })
}

func sdJobsDir(cfg *config.Config) string {
	if cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(cfg.DataDir, "sdjobs")
}

func sdcppBaseArgs(args []string, vramMB int) []string {
	out := sdgpu.SDCppArgsForVRAM(args, vramMB)
	if len(out) != len(args) {
		log.Printf("[sdcpp] %d MB VRAM: sd-server args %s", vramMB, strings.Join(out[len(args):], " "))
	}
	return out
}

func newSDCppFacade(cfg *config.Config, pm *process.ProcessManager, gm *sdgpu.GPUMonitor) *sdcpp.Facade {
	b := cfg.GetActiveBackend()
	if b == nil || b.Kind != config.KindSDCpp {
		return nil
	}
	pc, ok := cfg.Process(b.ProcessKey)
	if !ok {
		return nil
	}
	return sdcpp.New(sdcpp.Config{
		BaseURL:        pc.TargetURL,
		ModelsDir:      b.ModelsDir,
		LoraDir:        effectiveDir(pc.Args, pc.WorkDir, "--lora-model-dir", b.LoraDir),
		VAEDir:         b.VaeDir,
		TextEncoderDir: b.TextEncoderDir,
		UpscalerDir:    effectiveDir(pc.Args, pc.WorkDir, "--hires-upscalers-dir", b.UpscalerDir),
		TokenizerDir:   b.TokenizerDir,
		BaseArgs:       sdcppBaseArgs(pc.Args, cfg.DetectedVRAMMB),
		Aliases:        b.Aliases,
		ReadyTimeout:   110 * time.Second,
		JobsDir:        sdJobsDir(cfg),
		JobTTL:         cfg.SDJobResultTTL(),
	}, &processRestarter{pm: pm, cfg: cfg, key: b.ProcessKey}, &gpuMemory{gm: gm})
}
