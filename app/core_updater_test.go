package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectUpdateAssetPrefersWindowsSingleExecutable(t *testing.T) {
	asset, ok := selectUpdateAssetFor([]GitHubReleaseAsset{
		{Name: "dropo-Windows-Dependencies-x64.zip", BrowserDownloadURL: "deps"},
		{Name: "dropo-Linux-x64.AppImage", BrowserDownloadURL: "linux"},
		{Name: "dropo-Windows-x64.exe", BrowserDownloadURL: "single-exe", Size: 123},
	}, "windows", "amd64")
	if !ok {
		t.Fatal("expected update asset")
	}
	if asset.Name != "dropo-Windows-x64.exe" || asset.BrowserDownloadURL != "single-exe" || asset.Size != 123 {
		t.Fatalf("unexpected selected asset: %+v", asset)
	}
}

func TestSelectUpdateAssetRejectsLegacyWindowsZip(t *testing.T) {
	_, ok := selectUpdateAssetFor([]GitHubReleaseAsset{
		{Name: "dropo-Windows-Dependencies-x64.zip", BrowserDownloadURL: "deps"},
		{Name: "dropo-Windows-Portable-x64.zip", BrowserDownloadURL: "legacy"},
	}, "windows", "amd64")
	if ok {
		t.Fatal("legacy ZIP must not be selected after switching to the single executable")
	}
}

func TestSelectWindowsUpdateAssetByDistributionMode(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "dropo-Windows-Portable-x64.zip", BrowserDownloadURL: "portable"},
		{Name: "dropo-Windows-Setup-x64.exe", BrowserDownloadURL: "setup"},
	}
	installed, ok := selectUpdateAssetForMode(assets, "windows", "amd64", distributionModeInstalled)
	if !ok || installed.BrowserDownloadURL != "setup" {
		t.Fatalf("installed selected %+v, ok=%v", installed, ok)
	}
	portable, ok := selectUpdateAssetForMode(assets, "windows", "amd64", distributionModePortable)
	if !ok || portable.BrowserDownloadURL != "portable" {
		t.Fatalf("portable selected %+v, ok=%v", portable, ok)
	}
}

func TestLatestPortableReleaseUsesPortableArchive(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	releases := []GitHubRelease{{
		TagName: "v3.1.0",
		Assets: []GitHubReleaseAsset{
			{Name: "dropo-Windows-Setup-x64.exe", BrowserDownloadURL: "https://downloads.droponevedimka.ru/releases/download/v3.1.0/dropo-Windows-Setup-x64.exe", Size: 100, Digest: digest},
			{Name: "dropo-Windows-Portable-x64.zip", BrowserDownloadURL: "https://downloads.droponevedimka.ru/releases/download/v3.1.0/dropo-Windows-Portable-x64.zip", Size: 100, Digest: digest},
		},
	}}
	_, asset, ok := selectLatestInstallableReleaseForMode(releases, "windows", "amd64", distributionModePortable)
	if !ok || asset.Name != "dropo-Windows-Portable-x64.zip" {
		t.Fatalf("selected %+v, ok=%v", asset, ok)
	}
}

func TestWindowsUpdateCheckFallsBackFromAndroidOnlyGatewayToGitHub(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	writeReleases := func(t *testing.T, releases []GitHubRelease) http.HandlerFunc {
		t.Helper()
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/Droponevedimka/dropo/releases" || r.URL.Query().Get("per_page") != "100" {
				t.Errorf("unexpected metadata request: %s", r.URL.String())
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(releases); err != nil {
				t.Errorf("encode releases: %v", err)
			}
		}
	}

	gateway := httptest.NewServer(writeReleases(t, []GitHubRelease{{
		TagName: "v3.0.18",
		Assets: []GitHubReleaseAsset{{
			Name:               "dropo-Android-arm64.apk",
			BrowserDownloadURL: "https://downloads.droponevedimka.ru/releases/download/v3.0.18/dropo-Android-arm64.apk",
			Size:               100,
			Digest:             digest,
		}},
	}}))
	defer gateway.Close()

	github := httptest.NewServer(writeReleases(t, []GitHubRelease{{
		TagName: "v3.0.18",
		HTMLURL: "https://github.com/Droponevedimka/dropo/releases/tag/v3.0.18",
		Assets: []GitHubReleaseAsset{
			{Name: "dropo-Windows-Setup-x64.exe", BrowserDownloadURL: "https://github.com/Droponevedimka/dropo/releases/download/v3.0.18/dropo-Windows-Setup-x64.exe", Size: 101, Digest: digest},
			{Name: "dropo-Windows-Portable-x64.zip", BrowserDownloadURL: "https://github.com/Droponevedimka/dropo/releases/download/v3.0.18/dropo-Windows-Portable-x64.zip", Size: 102, Digest: digest},
		},
	}}))
	defer github.Close()

	for _, tc := range []struct {
		mode      string
		assetName string
	}{
		{distributionModeInstalled, "dropo-Windows-Setup-x64.exe"},
		{distributionModePortable, "dropo-Windows-Portable-x64.zip"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			info, err := checkForUpdatesWithClient(
				gateway.Client(),
				[]string{gateway.URL, github.URL},
				"Droponevedimka/dropo",
				"3.0.17",
				"windows",
				"amd64",
				tc.mode,
			)
			if err != nil {
				t.Fatalf("checkForUpdatesWithClient: %v", err)
			}
			if !info.Available || info.Version != "3.0.18" || info.AssetName != tc.assetName {
				t.Fatalf("update info = %+v, want available GitHub %s", info, tc.assetName)
			}
			if !strings.HasPrefix(info.DownloadURL, "https://github.com/Droponevedimka/dropo/releases/download/") {
				t.Fatalf("download URL = %q, want exact repository release URL", info.DownloadURL)
			}
		})
	}
}

func TestWindowsUpdateCheckRetriesWhenCanonicalMetadataFails(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]GitHubRelease{{
			TagName: "v3.0.18",
			Assets: []GitHubReleaseAsset{{
				Name: "dropo-Android-arm64.apk", BrowserDownloadURL: "https://downloads.droponevedimka.ru/releases/download/v3.0.18/dropo-Android-arm64.apk", Size: 100, Digest: digest,
			}},
		}})
	}))
	defer gateway.Close()
	canonical := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
	}))
	defer canonical.Close()

	_, err := checkForUpdatesWithClient(
		gateway.Client(), []string{gateway.URL, canonical.URL}, "Droponevedimka/dropo", "3.0.17", "windows", "amd64", distributionModeInstalled,
	)
	if err == nil {
		t.Fatal("Android-only gateway plus failed canonical metadata must return an error for the startup retry")
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
				{Name: "dropo-Windows-x64.exe", BrowserDownloadURL: "windows-2.2.1"},
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

func TestSelectLatestInstallableReleaseAcceptsExactGitHubRepository(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	releases := []GitHubRelease{
		{
			TagName: "v3.0.6",
			Assets: []GitHubReleaseAsset{{
				Name:               "dropo-Windows-Setup-x64.exe",
				BrowserDownloadURL: "https://github.com/example/dropo/releases/download/v3.0.6/dropo-Windows-Setup-x64.exe",
				Size:               100,
				Digest:             digest,
			}},
		},
		{
			TagName: "v3.0.5",
			Assets: []GitHubReleaseAsset{{
				Name:               "dropo-Windows-Setup-x64.exe",
				BrowserDownloadURL: "https://github.com/Droponevedimka/dropo/releases/download/v3.0.5/dropo-Windows-Setup-x64.exe",
				Size:               100,
				Digest:             digest,
			}},
		},
		{
			TagName: "v3.0.4",
			Assets: []GitHubReleaseAsset{{
				Name:               "dropo-Windows-Setup-x64.exe",
				BrowserDownloadURL: "https://downloads.droponevedimka.ru/releases/download/v3.0.4/dropo-Windows-Setup-x64.exe",
				Size:               100,
				Digest:             digest,
			}},
		},
	}
	release, asset, ok := selectLatestInstallableRelease(releases, "windows", "amd64")
	if !ok || release.TagName != "v3.0.5" || !strings.Contains(asset.BrowserDownloadURL, "github.com/Droponevedimka/dropo") {
		t.Fatalf("selected release=%q asset=%q ok=%v, want exact GitHub repository", release.TagName, asset.BrowserDownloadURL, ok)
	}
}

func TestSelectUpdateAssetForFuturePlatforms(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "dropo-Windows-x64.exe", BrowserDownloadURL: "windows"},
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
		"https://example.test/dropo-Windows-x64.exe":       ".exe",
		"https://example.test/dropo-Windows-x64.exe?token": ".exe",
		"https://example.test/download":                    ".bin",
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

func TestMakeUpdateScriptForSingleExecutable(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "dropo_update.exe")
	scriptPath, script, err := makeUpdateScript(tempFile, `C:\dropo\dropo.exe`, `C:\dropo`)
	if err != nil {
		t.Fatalf("makeUpdateScript: %v", err)
	}
	if filepath.Ext(scriptPath) != ".ps1" {
		t.Fatalf("script path = %q, want .ps1", scriptPath)
	}
	for _, part := range []string{"--from-update", "WaitForExit", "Remove-Item"} {
		if !strings.Contains(script, part) {
			t.Fatalf("single-executable update script missing %q:\n%s", part, script)
		}
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
	for _, allowed := range []string{
		"https://downloads.droponevedimka.ru/releases/download/v3.0.3/dropo-Windows-Setup-x64.exe",
		"https://github.com/Droponevedimka/dropo/releases/download/v3.0.18/dropo-Windows-Setup-x64.exe",
		"https://github.com/Droponevedimka/dropo/releases/download/v3.0.18/dropo-Windows-Portable-x64.zip",
	} {
		if err := validateTrustedUpdateURL(allowed); err != nil {
			t.Fatalf("trusted release asset rejected: %v", err)
		}
	}
	for _, rawURL := range []string{
		"http://github.com/Droponevedimka/dropo/releases/download/v2.2.0/update.exe",
		"https://example.com/update.exe",
		"https://github.com/example/dropo/releases/download/v2.2.0/update.zip",
		"https://github.com/Droponevedimka/dropo/actions/download/update.exe",
		"https://release-assets.githubusercontent.com/github-production-release-asset/update.exe",
	} {
		if err := validateTrustedUpdateURL(rawURL); err == nil {
			t.Errorf("untrusted update URL accepted: %s", rawURL)
		}
	}
}

func TestValidateTrustedUpdateRedirect(t *testing.T) {
	initial := "https://github.com/Droponevedimka/dropo/releases/download/v3.0.18/dropo-Windows-Setup-x64.exe"
	if err := validateTrustedUpdateRedirect(initial, "https://release-assets.githubusercontent.com/github-production-release-asset/123/signed?token=value"); err != nil {
		t.Fatalf("GitHub signed asset redirect rejected: %v", err)
	}
	for _, finalURL := range []string{
		"http://release-assets.githubusercontent.com/github-production-release-asset/123/signed",
		"https://release-assets.githubusercontent.com.example.test/payload.exe",
		"https://example.test/payload.exe",
	} {
		if err := validateTrustedUpdateRedirect(initial, finalURL); err == nil {
			t.Errorf("untrusted redirect accepted: %s", finalURL)
		}
	}
	if err := validateTrustedUpdateRedirect("https://example.test/update.exe", "https://release-assets.githubusercontent.com/payload"); err == nil {
		t.Fatal("untrusted initial URL was allowed to escalate through GitHub CDN")
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
