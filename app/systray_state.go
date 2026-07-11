package main

import (
	"os"
	"strings"
	"sync/atomic"
)

var systrayReadyFlag atomic.Bool

func isSystrayReady() bool {
	return systrayReadyFlag.Load()
}

func shouldHideToTrayOnClose(app *App) bool {
	return app != nil && !app.isShuttingDown() && isSystrayReady()
}

func getTrayDisplayName() string {
	if value := strings.TrimSpace(os.Getenv("DROPO_TRAY_LABEL")); value != "" {
		return value
	}
	return AppDisplayName
}
