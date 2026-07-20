package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeVersionFromInstallMetadata(t *testing.T) {
	root := t.TempDir()
	resources := filepath.Join(root, "resources")
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "install-manifest.json"), []byte(`{"version":"3.0.4","files":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runtimeVersionFromExecutablePath(filepath.Join(resources, "dropo-core.exe")); got != "3.0.4" {
		t.Fatalf("runtime version = %q, want 3.0.4", got)
	}
}

func TestRuntimeVersionRejectsDevelopmentOrMalformedMetadata(t *testing.T) {
	root := t.TempDir()
	resources := filepath.Join(root, "resources")
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "install-manifest.json"), []byte(`{"version":"dev"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "dependencies.json"), []byte(`{"appVersion":"not-a-version"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runtimeVersionFromExecutablePath(filepath.Join(resources, "dropo-core.exe")); got != "" {
		t.Fatalf("runtime version = %q, want empty", got)
	}
}
