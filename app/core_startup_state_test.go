package main

import "testing"

func TestDefaultStorageEnablesAutoStart(t *testing.T) {
	storage := NewStorage(t.TempDir())
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init: %v", err)
	}

	settings := storage.GetAppSettings()
	if !settings.AutoStart {
		t.Fatal("AutoStart default = false, want true")
	}
	if settings.RestoreVPNOnStartup {
		t.Fatal("RestoreVPNOnStartup default = true, want false")
	}
}

func TestRestoreVPNStartupStatePersists(t *testing.T) {
	storage := NewStorage(t.TempDir())
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init: %v", err)
	}

	app := NewApp()
	app.storage = storage
	app.setRestoreVPNOnStartup(true)
	if !storage.GetAppSettings().RestoreVPNOnStartup {
		t.Fatal("restore state was not saved")
	}

	app.setRestoreVPNOnStartup(false)
	if storage.GetAppSettings().RestoreVPNOnStartup {
		t.Fatal("restore state was not cleared")
	}
}
