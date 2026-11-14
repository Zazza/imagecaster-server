package gpu

import (
	"context"
	"log"
	"os"
	"os/exec"
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
	timeout  time.Duration
	OnUpdate func(GPUInfo)
}

const (
	pollInterval    = 5 * time.Second
	maxPollInterval = 60 * time.Second
	smiTimeout      = 5 * time.Second
)

func nextInterval(cur time.Duration, ok bool) time.Duration {
	if ok {
		return pollInterval
	}
	next := cur * 2
	if next > maxPollInterval {
		next = maxPollInterval
	}
	return next
}

func NewGPUMonitor() *GPUMonitor {
	gm := &GPUMonitor{
		binary:  "nvidia-smi",
		timeout: smiTimeout,
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
	interval := nextInterval(pollInterval, gm.poll(ctx))
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			interval = nextInterval(interval, gm.poll(ctx))
			timer.Reset(interval)
		}
	}
}

func (gm *GPUMonitor) poll(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, gm.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gm.binary,
		"--query-gpu=name,memory.total,memory.used,memory.free,utilization.gpu",
		"--format=csv,noheader,nounits",
	)
	hide.Window(cmd)
	cmd.WaitDelay = time.Second

	output, err := cmd.Output()
	if err != nil {
		gm.mu.Lock()
		gm.info = GPUInfo{Available: false}
		gm.mu.Unlock()
		return false
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
		return false
	}

	gm.mu.Lock()
	gm.info = info
	gm.mu.Unlock()

	if gm.OnUpdate != nil {
		gm.OnUpdate(info)
	}
	return true
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
	gm.poll(context.Background())
	return gm.Info()
}

func DetectVRAMMB() int {
	return NewGPUMonitor().Detect().MemoryTotal
}
