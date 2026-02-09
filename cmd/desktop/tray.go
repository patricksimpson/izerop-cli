package main

import (
	_ "embed"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed appicon.png
var trayIcon []byte

// startTray sets up the system tray icon and menu.
// Must be called from a goroutine — systray.Run blocks.
func (a *App) startTray() {
	systray.Run(a.onTrayReady, a.onTrayExit)
}

func (a *App) onTrayReady() {
	systray.SetIcon(trayIcon)
	systray.SetTitle("izerop")
	systray.SetTooltip("izerop sync")

	mShow := systray.AddMenuItem("Show izerop", "Show the main window")
	systray.AddSeparator()
	mSyncNow := systray.AddMenuItem("Sync Now", "Run a sync immediately")
	mWatch := systray.AddMenuItemCheckbox("Watching", "Toggle file watching", false)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit izerop")

	// Update watch checkbox state when notified
	go func() {
		for range a.traySyncCh {
			a.watchMu.Lock()
			watching := a.watcher != nil
			a.watchMu.Unlock()

			if watching {
				mWatch.Check()
			} else {
				mWatch.Uncheck()
			}
		}
	}()

	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				if a.ctx != nil {
					runtime.WindowShow(a.ctx)
					runtime.WindowUnminimise(a.ctx)
					runtime.WindowSetAlwaysOnTop(a.ctx, true)
					runtime.WindowSetAlwaysOnTop(a.ctx, false)
				}

			case <-mSyncNow.ClickedCh:
				go a.RunSync()

			case <-mWatch.ClickedCh:
				a.watchMu.Lock()
				watching := a.watcher != nil
				a.watchMu.Unlock()
				if watching {
					go a.StopWatch()
				} else {
					go a.StartWatch()
				}

			case <-mQuit.ClickedCh:
				a.StopWatch()
				systray.Quit()
				if a.ctx != nil {
					runtime.Quit(a.ctx)
				}
				return
			}
		}
	}()
}

func (a *App) onTrayExit() {
	// cleanup if needed
}
