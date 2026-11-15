package tui

import (
	"imagecaster-server/gpu"
	"imagecaster-server/gpuqueue"
	"imagecaster-server/health"
	"imagecaster-server/installer"
	"imagecaster-server/process"
)

type Cleaner func(string) string

func (c Cleaner) apply(s string) string {
	if c == nil {
		return s
	}
	return c(s)
}

func Services(ps map[string]process.ProcessStatus, hr map[string]health.HealthResult, clean Cleaner) map[string]ServiceInfo {
	out := make(map[string]ServiceInfo, len(ps))
	for k, p := range ps {
		si := ServiceInfo{
			Name:     clean.apply(p.Name),
			Status:   clean.apply(p.Status),
			PID:      p.PID,
			Uptime:   clean.apply(p.Uptime),
			Category: clean.apply(p.Category),
		}
		if h, ok := hr[k]; ok {
			si.Healthy = h.Healthy
			si.Latency = h.LatencyMs
		}
		out[k] = si
	}
	return out
}

func Health(hr map[string]health.HealthResult, clean Cleaner) map[string]HealthResult {
	out := make(map[string]HealthResult, len(hr))
	for k, v := range hr {
		out[k] = HealthResult{Healthy: v.Healthy, LatencyMs: v.LatencyMs, Error: clean.apply(v.Error)}
	}
	return out
}

func GPU(info gpu.GPUInfo, clean Cleaner) GPUInfo {
	return GPUInfo{
		Name:        clean.apply(info.Name),
		MemoryTotal: info.MemoryTotal,
		MemoryUsed:  info.MemoryUsed,
		Utilization: info.Utilization,
		Available:   info.Available,
	}
}

func Installs(st map[string]installer.InstallStatus, clean Cleaner) map[string]ComponentInstallStatus {
	out := make(map[string]ComponentInstallStatus, len(st))
	for k, s := range st {
		out[k] = ComponentInstallStatus{
			Key:        s.Key,
			Installed:  s.Installed,
			Installing: s.Installing,
			Progress:   clean.apply(s.Progress),
			Error:      clean.apply(s.Error),
			Version:    clean.apply(s.Version),
		}
	}
	return out
}

func Queue(st gpuqueue.Status, clean Cleaner) QueueSnapshot {
	snap := QueueSnapshot{Budget: st.Budget}
	for _, w := range st.Warnings {
		snap.Warnings = append(snap.Warnings, clean.apply(w))
	}
	for _, j := range st.Running {
		snap.Running = append(snap.Running, queueJob(j, clean))
	}
	for _, j := range st.Queue {
		snap.Waiting = append(snap.Waiting, queueJob(j, clean))
	}
	return snap
}

func queueJob(j gpuqueue.Job, clean Cleaner) QueueJob {
	return QueueJob{
		ID:            clean.apply(j.ID),
		Kind:          clean.apply(string(j.Kind)),
		Client:        clean.apply(j.Client),
		WeightMB:      j.WeightMB,
		Priority:      j.Priority,
		SubmittedAt:   j.SubmittedAt,
		LeaseDeadline: j.LeaseDeadline,
	}
}
