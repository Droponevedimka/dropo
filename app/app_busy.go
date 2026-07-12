package main

import (
	"fmt"
	"sync/atomic"
)

func (a *App) beginBusy(message string) string {
	id := fmt.Sprintf("busy-%d", atomic.AddUint64(&a.busySeq, 1))
	a.emitBusy(id, true, message)
	return id
}

// beginBusyTagged starts a busy task under a STABLE id (e.g. "vpn-connect").
// The frontend recognises these and renders them on the power button's own
// loader/status bar instead of the global busy modal.
func (a *App) beginBusyTagged(id, message string) string {
	a.emitBusy(id, true, message)
	return id
}

func (a *App) updateBusy(id string, message string) {
	if id == "" {
		return
	}
	a.emitBusy(id, true, message)
}

func (a *App) endBusy(id string) {
	if id == "" {
		return
	}
	a.emitBusy(id, false, "")
}

func (a *App) emitBusy(id string, active bool, message string) {
	if a.isShuttingDown() {
		return
	}
	payload := map[string]interface{}{
		"id":      id,
		"active":  active,
		"message": message,
	}
	a.emitEvent("app-busy", payload)
}
