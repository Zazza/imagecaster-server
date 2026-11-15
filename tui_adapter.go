package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"imagecaster-server/config"
	sdgpu "imagecaster-server/gpu"
	"imagecaster-server/gpuqueue"
	"imagecaster-server/process"
	"imagecaster-server/tui"
)

func runTUI(cfg *config.Config, cfgFile string, firstRun bool, port int) {
	logCapture := process.NewLogCapture(300)
	log.SetOutput(logCapture)

	if firstRun {
		result := tui.RunWizard(cfg.DataDir)
		if result.DataDir == "" {
			return
		}
		result.DataDir = absDir(result.DataDir)

		if err := os.MkdirAll(result.DataDir, 0755); err != nil {
			log.Fatalf("Failed to create data directory: %v", err)
		}

		if cfgFile == filepath.Join(cfg.DataDir, configName) {
			cfgFile = filepath.Join(result.DataDir, configName)
		}
		freshCfg, err := firstRunConfig(cfgFile, result.DataDir)
		if err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}
		*cfg = *freshCfg
		if port != 0 {
			cfg.Port = port
		}
	}

	ln, err := listen(cfg.Port)
	if err != nil {
		log.SetOutput(os.Stderr)
		log.Fatalf("Server error: %v", err)
	}

	r := startRuntime(cfg, ln, 5*time.Second)
	p := tea.NewProgram(tui.NewAppModel(tuiDeps(r, logCapture)), tea.WithAltScreen())

	r.d.inst.OnProgress = func(key, progress string) {
		p.Send(tui.InstallProgressMsg(key, progress))
	}
	r.d.pm.OnChange = func() {
		p.Send(tui.ServicesChangeMsg{})
	}
	r.d.gm.OnUpdate = func(info sdgpu.GPUInfo) {
		p.Send(tui.GPUUpdateMsgFunc(tui.GPU(info, nil)))
	}

	if _, err := p.Run(); err != nil {
		log.Fatalf("TUI error: %v", err)
	}

	go func() {
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}()
	r.stopBackendsFirst()
	os.Exit(0)
}

func tuiDeps(r *appRuntime, logCapture *process.LogCapture) tui.ServerDeps {
	d := r.d
	return tui.ServerDeps{
		Port:               d.cfg.Port,
		DataDir:            d.cfg.DataDir,
		EnsureAllInstalled: d.ensureInstalled,
		InstallStatus: func() map[string]tui.ComponentInstallStatus {
			return tui.Installs(d.inst.Status(), nil)
		},
		StartAll: d.pm.StartAll,
		StartMonitors: func() {
			d.startMonitors(r.ctx)
		},
		ProcStatus: func() map[string]tui.ServiceInfo {
			return tui.Services(d.pm.Status(), d.hm.Results(), nil)
		},
		StartProc:   d.pm.Start,
		StopProc:    d.pm.Stop,
		RestartProc: d.pm.Restart,
		ProcLogs: func(name string, lines int) []string {
			logs, err := d.pm.Logs(name, lines)
			if err != nil {
				return nil
			}
			return logs
		},
		GPUInfo: func() tui.GPUInfo {
			return tui.GPU(d.gm.Info(), nil)
		},
		HealthResults: func() map[string]tui.HealthResult {
			return tui.Health(d.hm.Results(), nil)
		},
		ServerLogs: func() []string {
			return logCapture.Lines(100)
		},
		QueueSnapshot: func() tui.QueueSnapshot {
			return gpuQueueSnapshot(d.q.Status())
		},
		QueueMove: func(id string, dir int) error {
			if !d.q.Move(id, dir) {
				return fmt.Errorf("queue move %s: %w", id, gpuqueue.ErrNotFound)
			}
			return nil
		},
		QueueCancel: func(id string) error {
			if !d.q.Cancel(id) {
				return fmt.Errorf("queue cancel %s: %w", id, gpuqueue.ErrNotFound)
			}
			return nil
		},
	}
}

func gpuQueueSnapshot(st gpuqueue.Status) tui.QueueSnapshot {
	return tui.Queue(st, gpuqueue.SanitizeClient)
}
