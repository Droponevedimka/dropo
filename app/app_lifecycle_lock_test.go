package main

import (
	"testing"
	"time"
)

func TestGetStatusReleasesAppMutex(t *testing.T) {
	app := &App{initialized: true}

	result := app.GetStatus()
	if result["running"] != false {
		t.Fatalf("unexpected running status: %#v", result["running"])
	}

	locked := make(chan struct{})
	go func() {
		app.mu.Lock()
		app.mu.Unlock()
		close(locked)
	}()

	select {
	case <-locked:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("GetStatus left app mutex locked")
	}
}

func TestStartRejectsDuplicateInitialization(t *testing.T) {
	app := NewApp()
	app.initialized = true
	app.initializedReady.Store(true)
	app.isStarting = true

	result := app.Start()
	if result["success"] != false {
		t.Fatalf("duplicate Start success = %#v, want false", result["success"])
	}
	if result["error"] == "" {
		t.Fatalf("duplicate Start error is empty: %#v", result)
	}
	if !app.isStarting {
		t.Fatal("duplicate Start must not clear the in-progress start owned by another call")
	}
}

func TestWindowVisibilityDoesNotWaitForAppMutex(t *testing.T) {
	app := NewApp()
	app.mu.Lock()
	defer app.mu.Unlock()

	done := make(chan struct{})
	go func() {
		app.SetWindowVisible(false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("SetWindowVisible waited for app mutex")
	}
}

func TestShutdownFlagDoesNotWaitForAppMutex(t *testing.T) {
	app := NewApp()
	app.mu.Lock()
	defer app.mu.Unlock()

	done := make(chan struct{})
	go func() {
		app.requestShutdown()
		if !app.isShuttingDown() {
			t.Error("shutdown flag was not set")
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("shutdown flag waited for app mutex")
	}
}

func TestCloseButtonHidesOnlyWhenSystrayReady(t *testing.T) {
	previous := systrayReadyFlag.Load()
	t.Cleanup(func() {
		systrayReadyFlag.Store(previous)
	})

	app := NewApp()
	systrayReadyFlag.Store(false)
	if shouldHideToTrayOnClose(app) {
		t.Fatal("window close must not hide to tray when systray is unavailable")
	}

	systrayReadyFlag.Store(true)
	if !shouldHideToTrayOnClose(app) {
		t.Fatal("window close should hide to tray when systray is ready")
	}

	app.requestShutdown()
	if shouldHideToTrayOnClose(app) {
		t.Fatal("window close must not hide to tray during shutdown")
	}
}
