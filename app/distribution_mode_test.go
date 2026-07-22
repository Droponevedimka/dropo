package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDistributionModeAtDefaultsToPortable(t *testing.T) {
	if got := distributionModeAt(t.TempDir()); got != distributionModePortable {
		t.Fatalf("mode = %q, want portable", got)
	}
}

func TestDistributionModeAtRequiresValidInstallerMarker(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, installModeMarkerName)
	if err := os.WriteFile(marker, []byte(`{"mode":"installed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := distributionModeAt(root); got != distributionModeInstalled {
		t.Fatalf("mode = %q, want installed", got)
	}
	if err := os.WriteFile(marker, []byte(`{"mode":"unknown"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := distributionModeAt(root); got != distributionModePortable {
		t.Fatalf("unknown marker mode = %q, want portable", got)
	}
}
