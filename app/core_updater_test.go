package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectUpdateAssetPrefersWindowsPortableZip(t *testing.T) {
	asset, ok := selectUpdateAssetFor([]GitHubReleaseAsset{
		{Name: "dropo-Windows-Dependencies-x64.zip", BrowserDownloadURL: "deps"},
		{Name: "dropo-Linux-x64.AppImage", BrowserDownloadURL: "linux"},
		{Name: "dropo-Windows-Portable-x64.zip", BrowserDownloadURL: "portable", Size: 123},
		{Name: "dropo-Windows-Setup.exe", BrowserDownloadURL: "installer"},
	}, "windows", "amd64")
	if !ok {
		t.Fatal("expected update asset")
	}
	if asset.Name != "dropo-Windows-Portable-x64.zip" || asset.BrowserDownloadURL != "portable" || asset.Size != 123 {
		t.Fatalf("unexpected selected asset: %+v", asset)
	}
}

func TestSelectUpdateAssetFallsBackToWindowsExe(t *testing.T) {
	asset, ok := selectUpdateAssetFor([]GitHubReleaseAsset{
		{Name: "dropo-Windows-Dependencies-x64.zip", BrowserDownloadURL: "deps"},
		{Name: "dropo-Windows-Setup.exe", BrowserDownloadURL: "installer"},
	}, "windows", "amd64")
	if !ok {
		t.Fatal("expected fallback update asset")
	}
	if asset.Name != "dropo-Windows-Setup.exe" {
		t.Fatalf("unexpected selected asset: %+v", asset)
	}
}

func TestSelectUpdateAssetForFuturePlatforms(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "dropo-Windows-Portable-x64.zip", BrowserDownloadURL: "windows"},
		{Name: "dropo-Linux-Dependencies-x64.zip", BrowserDownloadURL: "linux-deps"},
		{Name: "dropo-Linux-x64.AppImage", BrowserDownloadURL: "linux"},
		{Name: "dropo-macOS-arm64.dmg", BrowserDownloadURL: "macos"},
		{Name: "dropo-Android-universal.apk", BrowserDownloadURL: "android"},
		{Name: "dropo-iOS.ipa", BrowserDownloadURL: "ios"},
	}

	cases := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"linux", "amd64", "linux"},
		{"darwin", "arm64", "macos"},
		{"android", "arm64", "android"},
		{"ios", "arm64", "ios"},
	}
	for _, tc := range cases {
		asset, ok := selectUpdateAssetFor(assets, tc.goos, tc.goarch)
		if !ok {
			t.Fatalf("%s/%s: expected update asset", tc.goos, tc.goarch)
		}
		if asset.BrowserDownloadURL != tc.want {
			t.Fatalf("%s/%s selected %+v, want url %q", tc.goos, tc.goarch, asset, tc.want)
		}
	}
}

func TestUpdateFileExtension(t *testing.T) {
	cases := map[string]string{
		"https://example.test/dropo-Windows-Portable-x64.zip":       ".zip",
		"https://example.test/dropo-Windows-Portable-x64.zip?token": ".zip",
		"https://example.test/dropo-Windows-Setup.exe":              ".exe",
		"https://example.test/download":                             ".bin",
	}
	for input, want := range cases {
		if got := updateFileExtension(input); got != want {
			t.Fatalf("updateFileExtension(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMakeUpdateScriptForZip(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "dropo_update.zip")
	scriptPath, script, err := makeUpdateScript(tempFile, `C:\dropo\dropo.exe`, `C:\dropo`)
	if err != nil {
		t.Fatalf("makeUpdateScript: %v", err)
	}
	if filepath.Ext(scriptPath) != ".ps1" {
		t.Fatalf("script path = %q, want .ps1", scriptPath)
	}
	for _, part := range []string{"Expand-Archive", "Copy-Item", "Start-Process"} {
		if !strings.Contains(script, part) {
			t.Fatalf("zip update script missing %q:\n%s", part, script)
		}
	}
}

func TestResolvePortableInstallRootFromResourcesRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dropo")
	runtime := filepath.Join(root, "resources")

	installDir, launchExe := resolvePortableInstallRoot(runtime)

	if installDir != root {
		t.Fatalf("installDir = %q, want %q", installDir, root)
	}
	if launchExe != filepath.Join(root, "dropo.exe") {
		t.Fatalf("launchExe = %q, want root launcher", launchExe)
	}
}

func TestResolvePortableInstallRootFromLegacyNestedRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dropo")
	nestedRuntime := filepath.Join(root, "resources", "app")

	installDir, launchExe := resolvePortableInstallRoot(nestedRuntime)

	if installDir != root {
		t.Fatalf("installDir = %q, want %q", installDir, root)
	}
	if launchExe != filepath.Join(root, "dropo.exe") {
		t.Fatalf("launchExe = %q, want root launcher", launchExe)
	}
}
