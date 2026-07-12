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

func TestSelectUpdateAssetRejectsWindowsExeInstaller(t *testing.T) {
	_, ok := selectUpdateAssetFor([]GitHubReleaseAsset{
		{Name: "dropo-Windows-Dependencies-x64.zip", BrowserDownloadURL: "deps"},
		{Name: "dropo-Windows-Setup.exe", BrowserDownloadURL: "installer"},
	}, "windows", "amd64")
	if ok {
		t.Fatal("installer executable must not be copied over the portable launcher")
	}
}

func TestSelectLatestCompatibleReleaseSkipsAndroidOnlyReleaseForWindows(t *testing.T) {
	release, asset, ok := selectLatestCompatibleRelease([]GitHubRelease{
		{
			TagName: "v2.2.2",
			Assets: []GitHubReleaseAsset{
				{Name: "dropo-Android-arm64.apk", BrowserDownloadURL: "android-2.2.2"},
			},
		},
		{
			TagName: "v2.2.1",
			Assets: []GitHubReleaseAsset{
				{Name: "dropo-Windows-Portable-x64.zip", BrowserDownloadURL: "windows-2.2.1"},
			},
		},
	}, "windows", "amd64")
	if !ok {
		t.Fatal("expected a compatible Windows release")
	}
	if release.TagName != "v2.2.1" || asset.BrowserDownloadURL != "windows-2.2.1" {
		t.Fatalf("selected release=%s asset=%s", release.TagName, asset.BrowserDownloadURL)
	}
}

func TestSelectLatestCompatibleReleaseUsesNewestMatchingVersion(t *testing.T) {
	release, _, ok := selectLatestCompatibleRelease([]GitHubRelease{
		{TagName: "v2.2.0", Assets: []GitHubReleaseAsset{{Name: "dropo-Android-arm64.apk"}}},
		{TagName: "v2.3.0", Prerelease: true, Assets: []GitHubReleaseAsset{{Name: "dropo-Android-arm64.apk"}}},
		{TagName: "v2.2.2", Assets: []GitHubReleaseAsset{{Name: "dropo-Android-arm64.apk"}}},
	}, "android", "arm64")
	if !ok || release.TagName != "v2.2.2" {
		t.Fatalf("selected release=%s, want v2.2.2", release.TagName)
	}
}

func TestSelectUpdateAssetForFuturePlatforms(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "dropo-Windows-Portable-x64.zip", BrowserDownloadURL: "windows"},
		{Name: "dropo-Linux-Dependencies-x64.zip", BrowserDownloadURL: "linux-deps"},
		{Name: "dropo-Linux-x64.AppImage", BrowserDownloadURL: "linux"},
		{Name: "dropo-macOS-arm64.dmg", BrowserDownloadURL: "macos"},
		{Name: "dropo-Android-arm64.apk", BrowserDownloadURL: "android"},
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
		"https://example.test/dropo-Windows-Setup.exe":              ".bin",
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
	// The script must live in a private per-run dir, not a predictable shared
	// path, to avoid a TOCTOU swap before the elevated launch (review.md §4).
	if dir := filepath.Dir(scriptPath); !strings.Contains(filepath.Base(dir), "dropo-update-") {
		t.Fatalf("script dir = %q, want a randomized dropo-update-* dir", dir)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		v1, v2 string
		want   int
	}{
		{"2.1.2", "2.1.1", 1},
		{"2.1.1", "2.1.2", -1},
		{"2.1.0", "2.1.0", 0},
		{"2.2.0", "2.1.9", 1},
		{"v2.1.2", "2.1.2", 0}, // callers strip 'v', but be robust anyway
		// pre-release ranks below the same release core
		{"1.13.0", "1.13.0-alpha.27", 1},
		{"1.13.0-alpha.27", "1.13.0", -1},
		{"1.13.0-alpha.1", "1.13.0-alpha.2", -1},
		{"1.13.0-alpha.2", "1.13.0-alpha.10", -1}, // numeric, not lexical
		{"1.13.0-alpha", "1.13.0-beta", -1},
		{"1.13.0-alpha.1", "1.13.0-alpha.1", 0},
		{"2.1.2+build5", "2.1.2+build9", 0}, // build metadata ignored
	}
	for _, tc := range cases {
		v1 := strings.TrimPrefix(tc.v1, "v")
		v2 := strings.TrimPrefix(tc.v2, "v")
		if got := compareVersions(v1, v2); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.v1, tc.v2, got, tc.want)
		}
	}
}

func TestValidateTrustedUpdateURL(t *testing.T) {
	allowed := []string{
		"https://github.com/Droponevedimka/dropo/releases/download/v2.2.0/dropo-Windows-Portable-x64.zip",
		"https://release-assets.githubusercontent.com/github-production-release-asset/file",
	}
	if err := validateTrustedUpdateURL(allowed[0]); err != nil {
		t.Fatalf("trusted GitHub asset rejected: %v", err)
	}
	if err := validateTrustedUpdateHost(allowed[1]); err != nil {
		t.Fatalf("trusted GitHub redirect rejected: %v", err)
	}
	for _, rawURL := range []string{
		"http://github.com/Droponevedimka/dropo/releases/download/v2.2.0/update.zip",
		"https://example.com/update.zip",
		"https://github.com/Droponevedimka/dropo/releases/download/v2.2.0/update.bin",
	} {
		if err := validateTrustedUpdateURL(rawURL); err == nil {
			t.Errorf("untrusted update URL accepted: %s", rawURL)
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
