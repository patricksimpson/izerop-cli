package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed appicon.png
var appIcon []byte

func main() {
	app := NewApp()

	// Strip the frontend/dist prefix so index.html is at root
	distFS, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		log.Fatal("Could not load frontend assets:", err)
	}

	err = wails.Run(&options.App{
		Title:  "izerop",
		Width:  900,
		Height: 600,
		AssetServer: &assetserver.Options{
			Assets: distFS,
		},
		OnStartup: app.startup,
		Linux: &linux.Options{
			Icon: appIcon,
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
