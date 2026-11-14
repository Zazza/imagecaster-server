package gpu

import (
	"context"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagecaster-server/hide"
)

type GPUInfo struct {
	Name        string `json:"name,omitempty"`
	MemoryTotal int    `json:"memory_total_mb,omitempty"`
	MemoryUsed  int    `json:"memory_used_mb,omitempty"`
	MemoryFree  int    `json:"memory_free_mb,omitempty"`
	Utilization int    `json:"utilization_percent,omitempty"`
	Available   bool   `json:"available"`
}

type GPUMonitor struct {
	mu       sync.RWMutex
	info     GPUInfo
	binary   string
	lastBad  string
	OnUpdate func(GPUInfo)
}

func NewGPUMonitor() *GPUMonitor {
	gm := &GPUMonitor{
		binary: "nvidia-smi",
	}

	if runtime.GOOS == "windows" {
		if path := os.Getenv("ProgramFiles"); path != "" {
			candidate := path + `\NVIDIA Corporation\NVSMI\nvidia-smi.exe`
			if _, err := os.Stat(candidate); err == nil {
				gm.binary = candidate
			}
		}
	}

	return gm
}

func (gm *GPUMonitor) Start(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	gm.poll()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gm.poll()
		}
	}
}

func (gm *GPUMonitor) poll() {
	cmd := hide.Command(gm.binary,
		"--query-gpu=name,memory.total,memory.used,memory.free,utilization.gpu",
		"--format=csv,noheader,nounits",
	)

	output, err := cmd.Output()
	if err != nil {
		gm.mu.Lock()
		gm.info = GPUInfo{Available: false}
		gm.mu.Unlock()
		return
	}

	info, ok := parseNvidiaSMI(string(output))
	if !ok {
		raw := strings.TrimSpace(string(output))
		gm.mu.Lock()
		gm.info = GPUInfo{Available: false}
		report := raw != "" && raw != gm.lastBad
		gm.lastBad = raw
		gm.mu.Unlock()
		if report {
			log.Printf("[gpu] unexpected nvidia-smi output: %q", raw)
		}
		return
	}

	gm.mu.Lock()
	gm.info = info
	gm.mu.Unlock()

	if gm.OnUpdate != nil {
		gm.OnUpdate(info)
	}
}

func (gm *GPUMonitor) Info() GPUInfo {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	return gm.info
}

func parseNvidiaSMI(output string) (GPUInfo, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return parseNvidiaSMILine(line)
	}
	return GPUInfo{}, false
}

func parseNvidiaSMILine(line string) (GPUInfo, bool) {
	fields := strings.Split(line, ",")
	if len(fields) != 5 {
		return GPUInfo{}, false
	}
	nums := make([]int, 4)
	for i, f := range fields[1:] {
		n, ok := smiNumber(f)
		if !ok {
			return GPUInfo{}, false
		}
		nums[i] = n
	}
	return GPUInfo{
		Name:        strings.TrimSpace(fields[0]),
		MemoryTotal: nums[0],
		MemoryUsed:  nums[1],
		MemoryFree:  nums[2],
		Utilization: nums[3],
		Available:   true,
	}, true
}

func smiNumber(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "[N/A]" || s == "N/A" || s == "[Not Supported]" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (gm *GPUMonitor) Detect() GPUInfo {
	gm.poll()
	return gm.Info()
}

func DetectVRAMMB() int {
	return NewGPUMonitor().Detect().MemoryTotal
}
