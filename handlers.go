package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"imagecaster-server/api"
	"imagecaster-server/config"
	sdgpu "imagecaster-server/gpu"
	sdhealth "imagecaster-server/health"
	"imagecaster-server/installer"
	"imagecaster-server/process"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

type Handlers struct {
	manager      *process.ProcessManager
	health       *sdhealth.HealthMonitor
	gpu          *sdgpu.GPUMonitor
	config       *config.Config
	installer    *installer.Installer
	llmRunning   func() []string
	sdCheckpoint func() string
}

type sysJSON struct {
	CPUPercent float64 `json:"cpu_percent"`
	RAMUsage   float64 `json:"ram_usage"`
	RAMUsed    uint64  `json:"ram_used"`
	RAMTotal   uint64  `json:"ram_total"`
}

func NewHandlers(pm *process.ProcessManager, hm *sdhealth.HealthMonitor, gm *sdgpu.GPUMonitor, cfg *config.Config, inst *installer.Installer) *Handlers {
	return &Handlers{
		manager:   pm,
		health:    hm,
		gpu:       gm,
		config:    cfg,
		installer: inst,
	}
}

func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/server/status", h.handleStatus)
	mux.HandleFunc("/api/server/start/", h.procAction("/api/server/start/", "started", h.manager.Start))
	mux.HandleFunc("/api/server/stop/", h.procAction("/api/server/stop/", "stopped", h.manager.Stop))
	mux.HandleFunc("/api/server/restart/", h.procAction("/api/server/restart/", "restarted", h.manager.Restart))
	mux.HandleFunc("/api/server/logs/", h.handleLogs)
}

func (h *Handlers) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := h.manager.Status()
	healthResults := h.health.Results()
	gpuInfo := h.gpu.Info()

	models := h.fetchModels()
	resp := map[string]interface{}{
		"processes": statuses,
		"health":    healthResults,
		"gpu":       gpuInfo,
		"installs":  h.installer.Status(),
		"models":    models,
		"sys":       hostSysStats(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handlers) procAction(prefix, done string, act func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			api.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		if name == "" {
			api.WriteError(w, "process name required", http.StatusBadRequest)
			return
		}
		if err := act(name); err != nil {
			api.WriteError(w, err.Error(), procErrorStatus(err))
			return
		}
		api.WriteJSON(w, map[string]string{"status": done, "process": name})
	}
}

func procErrorStatus(err error) int {
	switch {
	case errors.Is(err, process.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, process.ErrAlreadyRunning):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (h *Handlers) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/server/logs/")
	name := strings.TrimSuffix(path, "/")
	if name == "" {
		api.WriteError(w, "process name required", http.StatusBadRequest)
		return
	}

	lines := 50
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}

	logs, err := h.manager.Logs(name, lines)
	if err != nil {
		api.WriteError(w, err.Error(), http.StatusNotFound)
		return
	}

	api.WriteJSON(w, map[string]interface{}{
		"process": name,
		"lines":   lines,
		"logs":    logs,
	})
}

func hostSysStats() sysJSON {
	var s sysJSON

	percentages, err := cpu.Percent(0, false)
	if err == nil && len(percentages) > 0 {
		s.CPUPercent = percentages[0]
	}

	vmStat, err := mem.VirtualMemory()
	if err == nil {
		s.RAMUsage = vmStat.UsedPercent
		s.RAMUsed = vmStat.Used
		s.RAMTotal = vmStat.Total
	}

	return s
}

func (h *Handlers) fetchModels() map[string]interface{} {
	models := make(map[string]interface{})
	if h.sdCheckpoint != nil {
		if cp := h.sdCheckpoint(); cp != "" {
			models["sd_checkpoint"] = cp
		}
	}

	if h.llmRunning != nil {
		if names := h.llmRunning(); len(names) > 0 {
			models["llm_running"] = names
		}
	}

	return models
}

func (h *Handlers) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	api.WriteJSON(w, map[string]interface{}{
		"name":     "Imagecaster Server",
		"version":  version,
		"status":   "running",
		"features": h.features(),
	})
}

func (h *Handlers) features() []string {
	out := []string{}
	if b := h.config.GetActiveBackend(); b != nil && b.Kind == config.KindSDCpp {
		out = append(out, "sdcpp", "sd_jobs", "sd_cache")
	}
	if h.llmRunning != nil {
		out = append(out, config.LLMLlamaCpp)
	}
	out = append(out, "download_stream")
	return out
}
