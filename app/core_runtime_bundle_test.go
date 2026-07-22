package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func createRuntimeBundleFixture(t *testing.T, version string, files map[string][]byte) (string, string) {
	t.Helper()
	root := t.TempDir()
	manifest := bundledRuntimeManifest{Version: version}
	for path, data := range files {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, data, 0644); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, bundledRuntimeManifestFile{
			Path: path, Size: int64(len(data)), SHA256: hashBytes(data),
		})
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, bundledRuntimeManifestName), manifestData, 0644); err != nil {
		t.Fatal(err)
	}
	return root, hashBytes(manifestData)
}

func TestInstallBundledRuntimeVerifiesAndRepairsDestination(t *testing.T) {
	const version = "runtime-123"
	source, manifestSHA := createRuntimeBundleFixture(t, version, map[string][]byte{
		"bin/sing-box.exe":  []byte("sing-box"),
		"bin/WinDivert.dll": []byte("windivert"),
		"bin/filters/a.srs": []byte("rules"),
	})
	destination := t.TempDir()
	if err := installBundledRuntime(source, destination, version, manifestSHA); err != nil {
		t.Fatalf("install bundled runtime: %v", err)
	}
	if err := verifyBundledRuntime(source, destination, version, manifestSHA); err != nil {
		t.Fatalf("verify bundled runtime: %v", err)
	}
	tampered := filepath.Join(destination, "bin", "WinDivert.dll")
	if err := os.WriteFile(tampered, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundledRuntime(source, destination, version, manifestSHA); err == nil {
		t.Fatal("tampered runtime was accepted")
	}
	if err := installBundledRuntime(source, destination, version, manifestSHA); err != nil {
		t.Fatalf("repair bundled runtime: %v", err)
	}
	if err := verifyBundledRuntime(source, destination, version, manifestSHA); err != nil {
		t.Fatalf("verify repaired runtime: %v", err)
	}
}

func TestBundledRuntimeRejectsManifestTamperingAndTraversal(t *testing.T) {
	source, manifestSHA := createRuntimeBundleFixture(t, "v1", map[string][]byte{
		"bin/tool.exe": []byte("tool"),
	})
	manifestPath := filepath.Join(source, bundledRuntimeManifestName)
	if err := os.WriteFile(manifestPath, []byte(`{"version":"v1","files":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := installBundledRuntime(source, t.TempDir(), "v1", manifestSHA); err == nil {
		t.Fatal("modified manifest was accepted")
	}
	if _, err := safeBundledRuntimePath("../outside.exe"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if _, err := safeBundledRuntimePath("tool.exe"); err == nil {
		t.Fatal("path outside bin/ was accepted")
	}
}
