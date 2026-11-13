package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"time"

	"imagecaster-server/api"
	"imagecaster-server/config"
	"imagecaster-server/installer"
	"imagecaster-server/llamacpp"
	"imagecaster-server/process"
)

func applyLlamaEnv(cfg *config.Config) {
	pc, ok := cfg.Process(config.LlamaCppProcessKey)
	if !ok || pc.ProxyPath == "" {
		return
	}
	pc.Env = installer.LlamaEnv(pc.Env, pc.Binary, installer.SDCppEngineDir(cfg), runtime.GOOS)
	cfg.SetProcess(config.LlamaCppProcessKey, pc)
}

type llamaReloader struct {
	pm     *process.ProcessManager
	key    string
	health string
	wait   time.Duration
}

func (r *llamaReloader) Reload(ctx context.Context) error {
	if r.pm == nil {
		return nil
	}
	st := r.pm.Status()[r.key].Status
	if st != "running" && st != "starting" {
		return nil
	}
	log.Printf("[llamacpp] model list changed, restarting llama-server")
	if err := r.pm.Restart(r.key); err != nil {
		return err
	}
	return waitHealthy(ctx, r.health, r.wait)
}

func waitHealthy(ctx context.Context, url string, timeout time.Duration) error {
	if url == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("llama-server not healthy after restart: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func newLlamaFacade(cfg *config.Config, pm *process.ProcessManager) *llamacpp.Facade {
	pc, ok := cfg.Process(config.LlamaCppProcessKey)
	if !ok || pc.ProxyPath == "" || pc.TargetURL == "" {
		return nil
	}
	s := cfg.LLMSettings()
	kaText := cfg.LlamaKeepAlive(cfg.DetectedVRAMMB)
	ka, err := llamacpp.ParseKeepAlive(kaText)
	if err != nil {
		log.Printf("[llamacpp] llm.keep_alive %q: %v, using 5m", kaText, err)
		ka = 5 * time.Minute
	}
	log.Printf("[llamacpp] default keep_alive %s (VRAM %dMB)", ka, cfg.DetectedVRAMMB)
	store := &llamacpp.Store{Root: cfg.LLMModelsDir()}
	var foreign []*llamacpp.Store
	for _, d := range cfg.ForeignOllamaDirs() {
		log.Printf("[llamacpp] reading models of the installed Ollama in %s (read-only)", d)
		foreign = append(foreign, &llamacpp.Store{Root: d, ReadOnly: true})
	}
	reload := &llamaReloader{pm: pm, key: config.LlamaCppProcessKey, health: pc.HealthURL, wait: 60 * time.Second}
	f := llamacpp.New(llamacpp.Config{
		Store:       store,
		Foreign:     foreign,
		RouterURL:   pc.TargetURL,
		PresetPath:  cfg.LlamaPresetPath(),
		ContextSize: s.ContextSize,
		KeepAlive:   ka,
		Reload:      reload.Reload,
		Puller:      &llamacpp.Puller{Store: store, HTTP: &http.Client{}},
	})
	if err := f.WritePreset(); err != nil {
		log.Printf("[llamacpp] write %s: %v", cfg.LlamaPresetPath(), err)
	}
	return f
}

type idleUnloader interface {
	UnloadIdle(ctx context.Context) error
}

func llmEvictor(u idleUnloader, budget func() int, llmWeightMB int) func(context.Context, int) {
	return func(ctx context.Context, sdWeightMB int) {
		if b := budget(); b > 0 && sdWeightMB+llmWeightMB <= b {
			return
		}
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := u.UnloadIdle(uctx); err != nil {
			log.Printf("[llamacpp] unload before SD job: %v", err)
		}
	}
}

const llmUnavailable = "LLM (llama.cpp) is not available on this server"

func withLLMServerRoutes(f *llamacpp.Facade, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f == nil && (llamacpp.IsServerRoute(r.URL.Path) || strings.HasPrefix(r.URL.Path, "/api/llm/")) {
			api.WriteError(w, llmUnavailable, http.StatusServiceUnavailable)
			return
		}
		if f != nil && llamacpp.IsServerRoute(r.URL.Path) {
			f.ServeServerModels(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
