package health

import (
	"context"
	"log"
	"sync"
	"time"

	"imagecaster-server/config"
	"imagecaster-server/probe"
)

type HealthResult struct {
	Healthy   bool      `json:"healthy"`
	LatencyMs int64     `json:"latency_ms"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

type HealthMonitor struct {
	mu      sync.RWMutex
	results map[string]HealthResult
	config  *config.Config
}

func NewHealthMonitor(cfg *config.Config) *HealthMonitor {
	return &HealthMonitor{
		results: make(map[string]HealthResult),
		config:  cfg,
	}
}

func (hm *HealthMonitor) Start(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	hm.checkAll()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hm.checkAll()
		}
	}
}

func (hm *HealthMonitor) checkAll() {
	for name, pc := range hm.config.ProcessesCopy() {
		if pc.HealthURL == "" {
			continue
		}
		go hm.check(name, pc.HealthURL)
	}
}

func (hm *HealthMonitor) check(name, url string) {
	latency, err := probe.ProbeTimeout(url, 5*time.Second)
	result := HealthResult{
		Healthy:   err == nil,
		LatencyMs: latency.Milliseconds(),
		CheckedAt: time.Now(),
	}
	if err != nil {
		result.Error = err.Error()
	}
	hm.setResult(name, result)
}

func (hm *HealthMonitor) setResult(name string, r HealthResult) {
	hm.mu.Lock()
	prev := hm.results[name]
	hm.results[name] = r
	hm.mu.Unlock()

	if r.Healthy && !prev.Healthy {
		log.Printf("[%s] healthy (latency: %dms)", name, r.LatencyMs)
	}
}

func (hm *HealthMonitor) Results() map[string]HealthResult {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	result := make(map[string]HealthResult, len(hm.results))
	for k, v := range hm.results {
		result[k] = v
	}
	return result
}
