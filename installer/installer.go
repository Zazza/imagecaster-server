package installer

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"imagecaster-server/config"
	"imagecaster-server/process"
)

type InstallStatus struct {
	Key        string `json:"key"`
	Installed  bool   `json:"installed"`
	Installing bool   `json:"installing"`
	Progress   string `json:"progress"`
	Error      string `json:"error,omitempty"`
	Version    string `json:"version,omitempty"`
}

type Installer struct {
	config           *config.Config
	mu               sync.RWMutex
	statuses         map[string]*InstallStatus
	logs             map[string]*process.RingBuffer
	installMu        map[string]*sync.Mutex
	backendSetup     func(target string, lb *process.RingBuffer) error
	ReleaseArtifacts func() ([]Artifact, error)
	LlamaArtifacts   func() ([]Artifact, error)
	OnProgress       func(key, progress string)
	ctx              context.Context
	cancel           context.CancelFunc
	running          sync.Mutex
}

var _ process.Installer = (*Installer)(nil)

func (inst *Installer) resolveTarget(target string) string {
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(inst.config.DataDir, target)
}

func NewInstaller(cfg *config.Config) *Installer {
	inst := &Installer{
		config:    cfg,
		statuses:  make(map[string]*InstallStatus),
		logs:      make(map[string]*process.RingBuffer),
		installMu: make(map[string]*sync.Mutex),
	}
	inst.ctx, inst.cancel = context.WithCancel(context.Background())
	inst.backendSetup = inst.setupBackend
	inst.MigrateEngineDirs()

	for key, bc := range cfg.Backends {
		if bc.Install.Method != "" && key == cfg.ActiveSD {
			s := &InstallStatus{Key: key}
			s.Installed = inst.checkInstalled(bc.Install) && fileExists(bc.Binary)
			if s.Installed {
				s.Version = bc.Install.Version
			}
			inst.statuses[key] = s
			inst.logs[key] = process.NewRingBuffer(process.RingBufferSize)
		}
	}

	for key, pc := range cfg.Processes {
		if pc.Install.Method != "" {
			if _, exists := inst.statuses[key]; exists {
				continue
			}
			if inst.isBackendProcess(key) && inst.getConfig(cfg.ActiveSD) != nil {
				continue
			}
			s := &InstallStatus{Key: key}
			s.Installed = inst.checkInstalled(pc.Install)
			if s.Installed {
				s.Version = pc.Install.Version
			}
			inst.statuses[key] = s
			inst.logs[key] = process.NewRingBuffer(process.RingBufferSize)
		}
	}

	return inst
}

func (inst *Installer) IsInstalled(key string) bool {
	inst.mu.RLock()
	s, ok := inst.statuses[key]
	inst.mu.RUnlock()
	if !ok {
		return false
	}
	return s.Installed
}

func (inst *Installer) installPlan() []string {
	var plan []string
	if inst.backendAutoStarts() {
		plan = append(plan, inst.config.ActiveSD)
	}
	llm := inst.config.LLMProcessKey()
	if inst.config.ActiveLLM == "" {
		llm = "ollama"
	}
	if pc, ok := inst.config.Process(llm); ok && pc.AutoStart && (llm == "ollama" || pc.Install.Method != "") {
		plan = append(plan, llm)
	}
	return plan
}

func (inst *Installer) backendAutoStarts() bool {
	backend, ok := inst.config.Backends[inst.config.ActiveSD]
	if !ok {
		return false
	}
	pc, ok := inst.config.Process(backend.ProcessKey)
	return ok && pc.AutoStart
}

func (inst *Installer) isBackendProcess(key string) bool {
	backend, ok := inst.config.Backends[inst.config.ActiveSD]
	return ok && backend.ProcessKey == key
}

func (inst *Installer) EnsureAllInstalled() {
	for _, key := range inst.installPlan() {
		if key == "ollama" {
			if findOllamaBinary(inst.config.DataDir) != "" {
				inst.mu.Lock()
				if s, ok := inst.statuses[key]; ok {
					s.Installed = true
					s.Progress = "done"
					inst.statuses[key] = s
				}
				inst.mu.Unlock()
				log.Printf("[ollama] already installed, skipping")
				continue
			}
			log.Printf("[ollama] installing...")
			inst.mu.Lock()
			s, ok := inst.statuses[key]
			if !ok {
				s = &InstallStatus{Key: key}
				inst.statuses[key] = s
			}
			s.Installing = true
			s.Progress = "installing"
			inst.statuses[key] = s
			inst.mu.Unlock()
			inst.setProgress(key, "installing")
			if path, err := inst.installOllama(); err != nil {
				inst.mu.Lock()
				s = inst.statuses[key]
				s.Installing = false
				s.Error = err.Error()
				s.Progress = "failed"
				inst.statuses[key] = s
				inst.mu.Unlock()
				inst.setProgress(key, "failed")
				log.Printf("[ollama] install failed: %v", err)
			} else {
				inst.mu.Lock()
				s = inst.statuses[key]
				s.Installed = true
				s.Installing = false
				s.Progress = "done"
				inst.statuses[key] = s
				inst.mu.Unlock()
				inst.setProgress(key, "done")
				log.Printf("[ollama] installed at %s", path)
			}
			continue
		}
		if key == inst.config.ActiveSD && !inst.isRelease(key) && inst.backendBinaryExists() {
			log.Printf("[%s] backend binary present, skipping install", key)
			continue
		}
		ic := inst.getConfig(key)
		if ic == nil {
			continue
		}
		if inst.IsInstalled(key) {
			log.Printf("[%s] already installed, skipping", key)
			continue
		}
		log.Printf("[%s] installing...", key)
		if err := inst.Install(key); err != nil {
			log.Printf("[%s] install failed: %v (will retry on process start)", key, err)
		}
	}
}

func (inst *Installer) backendBinaryExists() bool {
	backend, ok := inst.config.Backends[inst.config.ActiveSD]
	if !ok {
		return false
	}
	binary := backend.Binary
	if pc, ok := inst.config.Process(backend.ProcessKey); ok && pc.Binary != "" {
		binary = pc.Binary
	}
	if binary == "" || !filepath.IsAbs(binary) {
		return false
	}
	_, err := os.Stat(binary)
	return err == nil
}

func (inst *Installer) EnsureInstalled(key, binary string) error {
	if inst.isBackendProcess(key) {
		if inst.getConfig(inst.config.ActiveSD) != nil {
			key = inst.config.ActiveSD
		}
	}
	if binary != "" && !inst.isRelease(key) {
		if strings.ContainsRune(binary, '/') {
			if _, err := os.Stat(binary); err == nil {
				return nil
			}
		} else {
			if _, err := exec.LookPath(binary); err == nil {
				return nil
			}
		}
	}
	return inst.ensureConfigured(key)
}

func (inst *Installer) ensureConfigured(key string) error {
	if inst.getConfig(key) == nil {
		return nil
	}
	return inst.Install(key)
}

func (inst *Installer) keyLock(key string) *sync.Mutex {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	lk, ok := inst.installMu[key]
	if !ok {
		lk = &sync.Mutex{}
		inst.installMu[key] = lk
	}
	return lk
}

func (inst *Installer) ensureStatus(key string) *process.RingBuffer {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	s, ok := inst.statuses[key]
	if !ok {
		s = &InstallStatus{Key: key}
		ic := inst.getConfig(key)
		if ic != nil {
			s.Installed = inst.checkInstalled(*ic)
		}
		inst.statuses[key] = s
	}

	lb, ok := inst.logs[key]
	if !ok {
		lb = process.NewRingBuffer(process.RingBufferSize)
		inst.logs[key] = lb
	}
	return lb
}

func (inst *Installer) getConfig(key string) *config.InstallConfig {
	if bc, ok := inst.config.Backends[key]; ok && bc.Install.Method != "" {
		return &bc.Install
	}
	if pc, ok := inst.config.Process(key); ok && pc.Install.Method != "" {
		return &pc.Install
	}
	return nil
}

func (inst *Installer) Install(key string) error {
	lk := inst.keyLock(key)
	lk.Lock()
	defer lk.Unlock()

	lb := inst.ensureStatus(key)

	inst.mu.Lock()
	s := inst.statuses[key]
	if s.Installing {
		inst.mu.Unlock()
		return fmt.Errorf("already installing: %s", key)
	}
	if s.Installed {
		inst.mu.Unlock()
		return nil
	}

	ic := inst.getConfig(key)
	if ic == nil {
		inst.mu.Unlock()
		return fmt.Errorf("no install config for: %s", key)
	}

	s.Installing = true
	s.Progress = "starting"
	s.Error = ""
	inst.statuses[key] = s
	inst.mu.Unlock()

	err := inst.doInstall(key, *ic, lb)

	inst.mu.Lock()
	s = inst.statuses[key]
	s.Installing = false
	if err != nil {
		s.Error = err.Error()
		s.Progress = "failed"
	} else {
		s.Installed = true
		s.Progress = "done"
		s.Version = ic.Version
	}
	inst.statuses[key] = s
	inst.mu.Unlock()

	if inst.OnProgress != nil {
		inst.OnProgress(key, s.Progress)
	}

	return err
}

func (inst *Installer) Logs(key string, n int) []string {
	inst.mu.Lock()
	lb := inst.logs[key]
	inst.mu.Unlock()
	if lb == nil {
		return nil
	}
	return lb.Lines(n)
}

func (inst *Installer) Status() map[string]InstallStatus {
	inst.mu.RLock()
	defer inst.mu.RUnlock()

	result := make(map[string]InstallStatus, len(inst.statuses))
	for k, s := range inst.statuses {
		result[k] = *s
	}
	return result
}

func (inst *Installer) doInstall(key string, ic config.InstallConfig, lb *process.RingBuffer) error {
	switch ic.Method {
	case config.InstallZip:
		return inst.installZip(key, ic, lb)
	case config.InstallRelease:
		return inst.installRelease(key, ic, lb)
	}
	return fmt.Errorf("unknown install method: %s", ic.Method)
}

func (inst *Installer) checkInstalled(ic config.InstallConfig) bool {
	switch ic.Method {
	case config.InstallZip:
		if ic.Target == "" {
			return false
		}
		target := inst.resolveTarget(ic.Target)
		st, err := os.Stat(target)
		return err == nil && st.IsDir()
	case config.InstallRelease:
		return ic.Target != "" && inst.releaseInstalled(ic)
	}
	return false
}

func (inst *Installer) isRelease(key string) bool {
	ic := inst.getConfig(key)
	return ic != nil && ic.Method == config.InstallRelease
}

func (inst *Installer) setProgress(key string, progress string) {
	inst.mu.Lock()
	if s, ok := inst.statuses[key]; ok {
		s.Progress = progress
		inst.statuses[key] = s
	}
	inst.mu.Unlock()
	if inst.OnProgress != nil {
		inst.OnProgress(key, progress)
	}
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func (inst *Installer) Close() {
	inst.cancel()
	done := make(chan struct{})
	go func() {
		inst.running.Lock()
		inst.running.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("installer: setup commands still running after shutdown")
	}
}
