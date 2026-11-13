//go:build windows

package desktop

import (
	_ "embed"
	"time"

	"fyne.io/systray"

	"imagecaster-server/i18n"
)

//go:embed ui/icon.ico
var trayIcon []byte

func (a *app) tray() error {
	systray.Run(a.trayReady, a.stop)
	return nil
}

func serviceText(name, state string) string {
	return name + ": " + i18n.T("desktop.svc."+state)
}

func (a *app) trayReady() {
	e := a.opts.Engine
	systray.SetIcon(trayIcon)
	systray.SetTooltip("Imagecaster Server")

	mSD := systray.AddMenuItem(serviceText("Stable Diffusion", ServiceStarting), "")
	mSD.Disable()
	mLLM := systray.AddMenuItem(serviceText("LLM", ServiceStarting), "")
	mLLM.Disable()
	systray.AddSeparator()
	mAddr := systray.AddMenuItem("", i18n.T("desktop.tray.copy_hint"))
	var mConsole *systray.MenuItem
	if a.opts.ConsoleExe != "" {
		mConsole = systray.AddMenuItem(i18n.T("desktop.tray.console"), i18n.T("desktop.tray.console_hint"))
	}
	mLog := systray.AddMenuItem(i18n.T("desktop.tray.log"), "")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem(i18n.T("desktop.tray.quit"), i18n.T("desktop.tray.quit_hint"))

	refresh := func() Snapshot {
		s := e.Snapshot()
		mSD.SetTitle(serviceText("Stable Diffusion", sdTrayState(s)))
		mLLM.SetTitle(serviceText("LLM", s.LLM))
		mAddr.SetTitle(i18n.T("desktop.tray.addr") + " " + s.Addr)
		systray.SetTooltip("Imagecaster Server — " + serviceText("Stable Diffusion", sdTrayState(s)))
		return s
	}
	refresh()

	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			refresh()
		}
	}()

	consoleCh := make(<-chan struct{})
	if mConsole != nil {
		consoleCh = mConsole.ClickedCh
	}
	go func() {
		for {
			select {
			case <-mAddr.ClickedCh:
				copyText(e.Snapshot().Addr)
			case <-consoleCh:
				if err := openConsole(a.opts.ConsoleExe, "attach"); err != nil {
					ShowError(err.Error())
				}
			case <-mLog.ClickedCh:
				openFile(e.LogPath())
			case <-mQuit.ClickedCh:
				if e.Snapshot().GPUJobs > 0 && !askYesNo(i18n.T("desktop.tray.quit_busy")) {
					continue
				}
				systray.Quit()
				return
			}
		}
	}()
}

func sdTrayState(s Snapshot) string {
	switch s.Phase {
	case PhaseInstalling:
		return "installing"
	case PhaseFailed:
		return ServiceFailed
	}
	return s.SD
}
