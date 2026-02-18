package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:            "Athanor Epub Converter",
		Width:            920,
		Height:           700,
		MinWidth:         640,
		MinHeight:        480,
		BackgroundColour: &options.RGBA{R: 9, G: 9, B: 11, A: 255},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.Shutdown,
		Bind: []interface{}{
			app,
		},
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: true,
			CSSDropProperty:    "--wails-drop-target",
			CSSDropValue:       "drop",
		},
	})

	if err != nil {
		println("FATAL: Wails application failed to start:", err.Error())
	}
}
