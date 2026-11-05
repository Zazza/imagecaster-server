//go:build windows

package desktop

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2"
	wailsopts "github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"imagecaster-server/i18n"
)

//go:embed ui
var uiFS embed.FS

type Window struct {
	a   *app
	ctx context.Context
}

type Info struct {
	Version  string `json:"version"`
	Lang     string `json:"lang"`
	AskHome  bool   `json:"ask_home"`
	Home     string `json:"home"`
	NeedGB   int    `json:"need_gb"`
	GPU      string `json:"gpu"`
	VRAMGB   int    `json:"vram_gb"`
	GPUError string `json:"gpu_error"`
}

func runWindow(a *app) error {
	assets, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return err
	}
	w := &Window{a: a}
	return wails.Run(&wailsopts.App{
		Title:            i18n.T("desktop.window_title"),
		Width:            760,
		Height:           620,
		MinWidth:         640,
		MinHeight:        500,
		BackgroundColour: &wailsopts.RGBA{R: 12, G: 12, B: 16, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        func(ctx context.Context) { w.ctx = ctx },
		Bind:             []any{w},
	})
}

func (w *Window) Info() Info {
	inf := Info{Version: w.a.opts.Version, Lang: i18n.Lang(), NeedGB: w.a.opts.NeedGB}
	if w.a.isStarted() {
		inf.Home = w.a.dataDir
	} else {
		inf.AskHome = true
		inf.Home = w.a.opts.DefaultDir
	}
	if g, err := detectGPU(); err != nil {
		inf.GPUError = err.Error()
	} else {
		inf.GPU, inf.VRAMGB = g.Name, g.VRAMGB
	}
	return inf
}

func (w *Window) FreeGB(dir string) float64 {
	return freeGB(dir)
}

func (w *Window) ChooseFolder(current string) string {
	dir, err := runtime.OpenDirectoryDialog(w.ctx, runtime.OpenDialogOptions{
		Title:                i18n.T("desktop.choose_folder"),
		DefaultDirectory:     existingParent(current),
		CanCreateDirectories: true,
	})
	if err != nil || dir == "" {
		return current
	}
	return dir
}

func existingParent(dir string) string {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

func (w *Window) Start(dir string) string {
	if w.a.isStarted() {
		return ""
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return i18n.T("desktop.choose_folder")
	}
	if err := w.a.take(dir); err != nil {
		return err.Error()
	}
	return ""
}

func (w *Window) State() Snapshot {
	if !w.a.isStarted() {
		return Snapshot{}
	}
	return w.a.opts.Engine.Snapshot()
}

func (w *Window) Retry() {
	if w.a.isStarted() {
		w.a.opts.Engine.Retry()
	}
}

func (w *Window) OpenLog() {
	if w.a.isStarted() {
		openFile(w.a.opts.Engine.LogPath())
	}
}

func (w *Window) Copy(text string) {
	if err := runtime.ClipboardSetText(w.ctx, text); err != nil {
		log.Printf("copy to clipboard: %v", err)
	}
}

func (w *Window) Close() {
	runtime.Quit(w.ctx)
}
