//go:build windows

package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"imagecaster-server/i18n"
)

type Engine interface {
	Start(dataDir string) error
	Snapshot() Snapshot
	Retry()
	Stop()
	LogPath() string
}

type Options struct {
	Version    string
	DataDir    string
	DefaultDir string
	ForgeURL   string
	ConsoleExe string
	NeedGB     int
	Engine     Engine
}

type app struct {
	opts Options

	mu       sync.Mutex
	starting bool
	started  bool
	dataDir  string
	lock     *os.File
}

func Run(o Options) error {
	a := &app{opts: o}
	if o.DataDir != "" {
		if err := a.take(o.DataDir); err != nil {
			return err
		}
		if Installed(o.Engine.Snapshot().Components) {
			return a.tray()
		}
	}
	if err := runWindow(a); err != nil {
		a.stop()
		return err
	}
	if !a.isStarted() {
		a.stop()
		return nil
	}
	return a.tray()
}

func (a *app) take(dir string) error {
	a.mu.Lock()
	if a.starting || a.started {
		a.mu.Unlock()
		return nil
	}
	a.starting = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.starting = false
		a.mu.Unlock()
	}()
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	lock, err := LockDir(dir)
	if errors.Is(err, ErrBusy) {
		return errors.New(i18n.T("desktop.already_running"))
	}
	if err != nil {
		return err
	}
	if err := a.opts.Engine.Start(dir); err != nil {
		lock.Close()
		return err
	}
	a.mu.Lock()
	a.started, a.dataDir, a.lock = true, dir, lock
	a.mu.Unlock()
	return nil
}

func (a *app) isStarted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started
}

func (a *app) stop() {
	a.mu.Lock()
	started, lock := a.started, a.lock
	a.started, a.lock = false, nil
	a.mu.Unlock()
	if started {
		a.opts.Engine.Stop()
	}
	if lock != nil {
		lock.Close()
	}
}
