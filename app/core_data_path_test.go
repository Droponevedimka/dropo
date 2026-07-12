package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveUserDataPathOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "private-state")
	t.Setenv("DROPO_DATA_DIR", want)
	if got := resolveUserDataPath(); got != want {
		t.Fatalf("resolveUserDataPath = %q, want %q", got, want)
	}
}

func TestMigrateUnifiedSettingsIfMissing(t *testing.T) {
	oldRoot := t.TempDir()
	oldPath := filepath.Join(oldRoot, ResourcesFolder, SettingsFileName)
	if err := os.MkdirAll(filepath.Dir(oldPath), 0755); err != nil {
		t.Fatal(err)
	}
	want := SettingsFile{Version: SettingsVersion, App: GlobalAppSettings{ActiveProfileID: 1}}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	storage := NewStorage(t.TempDir())
	if err := storage.MigrateUnifiedSettingsIfMissing(oldPath); err != nil {
		t.Fatalf("migrate settings: %v", err)
	}
	got, err := os.ReadFile(storage.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("migrated settings differ: %s", got)
	}

	if err := os.WriteFile(storage.settingsPath, []byte(`{"version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.MigrateUnifiedSettingsIfMissing(oldPath); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(storage.settingsPath)
	if string(got) != `{"version":99}` {
		t.Fatalf("existing per-user settings were overwritten: %s", got)
	}
}
