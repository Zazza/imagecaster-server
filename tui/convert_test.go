package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"imagecaster-server/gpu"
	"imagecaster-server/gpuqueue"
	"imagecaster-server/health"
	"imagecaster-server/installer"
	"imagecaster-server/process"
)

func upper(s string) string { return strings.ToUpper(s) }

func TestServicesMergesHealthAndCleans(t *testing.T) {
	ps := map[string]process.ProcessStatus{
		"sd":  {Name: "sd", Status: "running", PID: 7, Uptime: "1m", Category: "image"},
		"llm": {Name: "llm", Status: "stopped"},
	}
	hr := map[string]health.HealthResult{"sd": {Healthy: true, LatencyMs: 12}}

	got := Services(ps, hr, upper)
	assert.Equal(t, ServiceInfo{Name: "SD", Status: "RUNNING", PID: 7, Uptime: "1M", Category: "IMAGE", Healthy: true, Latency: 12}, got["sd"])
	assert.Equal(t, ServiceInfo{Name: "LLM", Status: "STOPPED"}, got["llm"])

	plain := Services(ps, nil, nil)
	assert.Equal(t, "sd", plain["sd"].Name)
	assert.False(t, plain["sd"].Healthy)
}

func TestHealthGPUInstallsQueue(t *testing.T) {
	assert.Equal(t, map[string]HealthResult{"sd": {Healthy: false, LatencyMs: 3, Error: "STATUS 503"}},
		Health(map[string]health.HealthResult{"sd": {LatencyMs: 3, Error: "status 503"}}, upper))

	assert.Equal(t, GPUInfo{Name: "RTX", MemoryTotal: 8, MemoryUsed: 2, Utilization: 50, Available: true},
		GPU(gpu.GPUInfo{Name: "rtx", MemoryTotal: 8, MemoryUsed: 2, MemoryFree: 6, Utilization: 50, Available: true}, upper))

	assert.Equal(t, map[string]ComponentInstallStatus{"sd": {Key: "sd", Installing: true, Progress: "50%", Error: "E", Version: "V1"}},
		Installs(map[string]installer.InstallStatus{"sd": {Key: "sd", Installing: true, Progress: "50%", Error: "e", Version: "v1"}}, upper))

	q := Queue(gpuqueue.Status{Budget: 9, Warnings: []string{"w"}, Queue: []gpuqueue.Job{{ID: "a", Kind: gpuqueue.KindLLM, Client: "c", WeightMB: 3}}}, upper)
	assert.Equal(t, 9, q.Budget)
	assert.Equal(t, []string{"W"}, q.Warnings)
	assert.Empty(t, q.Running)
	assert.Equal(t, []QueueJob{{ID: "A", Kind: "LLM", Client: "C", WeightMB: 3}}, q.Waiting)
}
