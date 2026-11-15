package sdcpp

import (
	"context"
	"fmt"
	"imagecaster-server/fsutil"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (f *Facade) modelArgs(m ModelSpec) ([]string, error) {
	args, err := ModelArgs(f.cfg, m.Checkpoint, m.Modules)
	if err != nil {
		return nil, err
	}
	if usesSingleFile(args) && !slices.Contains(args, "--vae") {
		if v := strings.TrimSpace(m.VAE); v != "" {
			p := v
			if !filepath.IsAbs(p) {
				p = filepath.Join(f.cfg.VAEDir, v)
			}
			if !within(p, f.cfg.VAEDir, f.cfg.ModelsDir) {
				return nil, fmt.Errorf("vae %q is outside %s", v, f.cfg.VAEDir)
			}
			if !fsutil.FileExists(p) {
				return nil, fmt.Errorf("vae %q not found on the server", v)
			}
			args = append(args, "--vae", p)
		}
	}
	return args, nil
}

func (f *Facade) checkModel(m ModelSpec) ([]string, error) {
	name := cleanName(m.Checkpoint)
	if name == "" {
		return nil, apiErr(http.StatusBadRequest, "invalid_params", "model.checkpoint is required")
	}
	if _, ok := lookupAlias(f.cfg, name); !ok {
		p, err := checkpointPath(f.cfg, name)
		if err != nil || !fsutil.FileExists(p) {
			return nil, apiErr(http.StatusBadRequest, "model_not_found", "checkpoint %q not found on the server", m.Checkpoint)
		}
	}
	args, err := f.modelArgs(m)
	if err != nil {
		return nil, apiErr(http.StatusBadRequest, "invalid_params", "%v", err)
	}
	return args, nil
}

func (f *Facade) ensureModel(ctx context.Context, m ModelSpec) error {
	args, err := f.checkModel(m)
	if err != nil {
		return err
	}
	f.mu.Lock()
	same := f.args != nil && slices.Equal(args, f.args)
	f.mu.Unlock()
	if !same {
		if err := f.load(ctx, args); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.loadedName = m.Checkpoint
	f.loaded = ModelSpec{Checkpoint: m.Checkpoint, VAE: m.VAE, Modules: slices.Clone(m.Modules)}
	f.mu.Unlock()
	return nil
}

func (f *Facade) load(ctx context.Context, args []string) error {
	full := append(slices.Clone(f.cfg.BaseArgs), args...)
	f.mu.Lock()
	f.args = nil
	f.loadedName = ""
	f.loaded = ModelSpec{}
	f.mu.Unlock()
	if f.restart == nil {
		return apiErr(http.StatusInternalServerError, "sd_failed", "sd-server restart is not configured")
	}
	f.mu.Lock()
	resume := slices.Equal(f.pending, args) && time.Since(f.pendingAt) < loadGrace
	f.mu.Unlock()
	if ac, ok := f.restart.(aliveChecker); ok && resume && !ac.Alive() {
		resume = false
	}
	if !resume {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.restart.RestartWithArgs(full); err != nil {
			return apiErr(http.StatusBadGateway, "sd_failed", "restart sd-server: %v", err)
		}
		f.mu.Lock()
		f.pending, f.pendingAt = slices.Clone(args), time.Now()
		f.mu.Unlock()
	}
	if err := f.waitReady(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	f.args = args
	f.pending = nil
	f.mu.Unlock()
	return nil
}

const loadGrace = 10 * time.Minute

func (f *Facade) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(f.cfg.ReadyTimeout)
	var last string
	for {
		code, body, err := f.do(ctx, http.MethodGet, "/sdcpp/v1/capabilities", nil, 5*time.Second)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			last = err.Error()
		case code == http.StatusOK:
			return nil
		default:
			last = fmt.Sprintf("capabilities HTTP %d: %s", code, truncate(body, 200))
		}
		if time.Now().After(deadline) {
			return apiErr(http.StatusBadGateway, "sd_failed", "sd-server not ready after %s: %s", f.cfg.ReadyTimeout, last)
		}
		t := time.NewTimer(f.cfg.PollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
