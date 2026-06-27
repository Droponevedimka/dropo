package main

import (
	"context"
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed config/template.json
var embeddedTemplate []byte

var appInstance *App

func copyEmbeddedTemplate(destPath string) error {
	return os.WriteFile(destPath, embeddedTemplate, 0644)
}

func main() {
	releaseSingleInstance, alreadyRunning := acquireSingleInstance()
	if alreadyRunning {
		log.Println("Application already running, activating existing window")
		return
	}
	if releaseSingleInstance != nil {
		defer releaseSingleInstance()
	}

	appInstance = NewApp()
	if err := appInstance.openLogFile(); err != nil {
		log.Printf("failed to open session log: %v", err)
	}

	startPlatformTray()
	runWails()
}

func runWails() {
	appOptions := &options.App{
		Title:     AppDisplayName,
		Width:     WindowWidth,
		Height:    WindowHeight,
		MinWidth:  MinWindowWidth,
		MinHeight: MinWindowHeight,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        appInstance.startup,
		OnBeforeClose: func(ctx context.Context) bool {
			if appInstance != nil && !appInstance.isShuttingDown() {
				if shouldHideToTrayOnClose(appInstance) {
					appInstance.writeLog("Window close requested; hiding to tray")
					appInstance.HideWindow()
					return true
				}
				appInstance.writeLog("Window close requested while systray is unavailable; closing application to avoid hidden background process")
				appInstance.requestShutdown()
			}
			return false
		},
		OnShutdown: appInstance.shutdown,
		Bind: []interface{}{
			appInstance,
		},
		Frameless:         false,
		HideWindowOnClose: false,
	}
	applyPlatformWailsOptions(appOptions)

	if err := wails.Run(appOptions); err != nil {
		log.Fatal(err)
	}
}
