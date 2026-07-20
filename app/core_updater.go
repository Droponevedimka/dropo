package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const maxUpdateDownloadBytes int64 = 512 << 20

// GitHubRelease represents a GitHub release.
type GitHubRelease struct {
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	Body        string               `json:"body"`
	PublishedAt time.Time            `json:"published_at"`
	HTMLURL     string               `json:"html_url"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	Assets      []GitHubReleaseAsset `json:"assets"`
}

// GitHubReleaseAsset represents an asset attached to a GitHub release.
type GitHubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
}

// UpdateInfo contains information about available updates.
type UpdateInfo struct {
	Available      bool   `json:"available"`
	Version        string `json:"version"`
	CurrentVersion string `json:"current_version"`
	Description    string `json:"description"`
	DownloadURL    string `json:"download_url"`
	ReleaseURL     string `json:"release_url"`
	PublishedAt    string `json:"published_at"`
	FileSize       int64  `json:"file_size"`
	AssetName      string `json:"asset_name"`
	SHA256         string `json:"sha256"`
}

// CheckForUpdates checks the trusted Russian release mirror for updates.
func CheckForUpdates() (*UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultHTTPTimeout)
	defer cancel()

	// Releases may be platform-specific. The repository-wide /latest endpoint
	// can point to an Android-only release and must not make Windows offer an
	// update without a Windows asset, so inspect recent releases instead.
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=100", ReleaseMirrorBaseURL, GitHubRepo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", AppName+"/"+Version)

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		// No releases
		return &UpdateInfo{
			Available:      false,
			CurrentVersion: Version,
		}, nil
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("release mirror returned status %d", resp.StatusCode)
	}

	body, err := readHTTPBodyLimited(resp.Body, maxReleaseMetadataBytes)
	if err != nil {
		return nil, err
	}

	var releases []GitHubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("failed to parse release mirror response: %w", err)
	}
	release, asset, found := selectLatestCompatibleRelease(releases, runtime.GOOS, runtime.GOARCH)
	if !found {
		return &UpdateInfo{
			Available:      false,
			CurrentVersion: strings.TrimPrefix(Version, "v"),
		}, nil
	}

	// Extract version from tag (remove 'v' prefix if present)
	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentVersion := strings.TrimPrefix(Version, "v")

	// Compare versions
	available := compareVersions(latestVersion, currentVersion) > 0

	return &UpdateInfo{
		Available:      available,
		Version:        latestVersion,
		CurrentVersion: currentVersion,
		Description:    release.Body,
		DownloadURL:    asset.BrowserDownloadURL,
		ReleaseURL:     release.HTMLURL,
		PublishedAt:    release.PublishedAt.Format("02.01.2006"),
		FileSize:       asset.Size,
		AssetName:      asset.Name,
		SHA256:         normalizeGitHubSHA256(asset.Digest),
	}, nil
}

func selectLatestCompatibleRelease(releases []GitHubRelease, goos, goarch string) (GitHubRelease, GitHubReleaseAsset, bool) {
	var selectedRelease GitHubRelease
	var selectedAsset GitHubReleaseAsset
	selectedVersion := ""
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		asset, ok := selectUpdateAssetFor(release.Assets, goos, goarch)
		if !ok {
			continue
		}
		version := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
		if version == "" || (selectedVersion != "" && compareVersions(version, selectedVersion) <= 0) {
			continue
		}
		selectedRelease = release
		selectedAsset = asset
		selectedVersion = version
	}
	return selectedRelease, selectedAsset, selectedVersion != ""
}

func selectUpdateAsset(assets []GitHubReleaseAsset) (GitHubReleaseAsset, bool) {
	return selectUpdateAssetFor(assets, runtime.GOOS, runtime.GOARCH)
}

func selectUpdateAssetFor(assets []GitHubReleaseAsset, goos, goarch string) (GitHubReleaseAsset, bool) {
	target := PlatformTargetFor(goos, goarch)
	if target.AppAsset != "" {
		for _, asset := range assets {
			if strings.EqualFold(asset.Name, target.AppAsset) {
				return asset, true
			}
		}
	}

	switch goos {
	case "windows":
		return selectWindowsUpdateAsset(assets)
	case "linux":
		return selectAssetByPredicates(assets,
			func(name string) bool {
				return containsAll(name, "dropo", "linux") && strings.HasSuffix(name, ".appimage")
			},
			func(name string) bool { return containsAll(name, "dropo", "linux") && strings.HasSuffix(name, ".deb") },
			func(name string) bool {
				return containsAll(name, "dropo", "linux") && strings.HasSuffix(name, ".tar.gz")
			},
		)
	case "darwin":
		return selectAssetByPredicates(assets,
			func(name string) bool {
				return (strings.Contains(name, "macos") || strings.Contains(name, "darwin")) && strings.HasSuffix(name, ".dmg")
			},
			func(name string) bool {
				return (strings.Contains(name, "macos") || strings.Contains(name, "darwin")) && strings.HasSuffix(name, ".zip")
			},
		)
	case "android":
		return selectAssetByPredicates(assets,
			func(name string) bool { return strings.Contains(name, "android") && strings.HasSuffix(name, ".apk") },
		)
	case "ios":
		return selectAssetByPredicates(assets,
			func(name string) bool {
				return (strings.Contains(name, "ios") || strings.Contains(name, "iphone")) && strings.HasSuffix(name, ".ipa")
			},
		)
	default:
		return GitHubReleaseAsset{}, false
	}
}

func selectWindowsUpdateAsset(assets []GitHubReleaseAsset) (GitHubReleaseAsset, bool) {
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, "windows") && strings.HasSuffix(name, ".exe") && !strings.Contains(name, "dependencies") {
			return asset, true
		}
	}
	return GitHubReleaseAsset{}, false
}

func selectAssetByPredicates(assets []GitHubReleaseAsset, predicates ...func(string) bool) (GitHubReleaseAsset, bool) {
	for _, predicate := range predicates {
		for _, asset := range assets {
			name := strings.ToLower(asset.Name)
			if strings.Contains(name, "dependencies") {
				continue
			}
			if predicate(name) {
				return asset, true
			}
		}
	}
	return GitHubReleaseAsset{}, false
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}

func validateTrustedUpdateURL(rawURL string) error {
	if err := validateTrustedUpdateHost(rawURL); err != nil {
		return err
	}
	if ext := updateFileExtension(rawURL); ext != ".exe" {
		return fmt.Errorf("unsupported update asset type: %s", ext)
	}
	return nil
}

func validateTrustedUpdateHost(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid update URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("update URL must use HTTPS")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "downloads.droponevedimka.ru" {
		return fmt.Errorf("untrusted update host: %s", host)
	}
	return nil
}

// DownloadUpdate downloads a bounded update file to the temp directory.
func DownloadUpdate(downloadURL string, expectedSize int64, expectedSHA256 string, progressCallback func(downloaded, total int64)) (string, error) {
	if err := validateTrustedUpdateURL(downloadURL); err != nil {
		return "", err
	}
	if expectedSize <= 0 || expectedSize > maxUpdateDownloadBytes {
		return "", fmt.Errorf("invalid update size: %d", expectedSize)
	}
	expectedSHA256 = normalizeGitHubSHA256(expectedSHA256)
	if len(expectedSHA256) != 64 {
		return "", fmt.Errorf("release asset has no valid SHA-256 digest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), LongHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", AppName+"/"+Version)

	resp, err := LongHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}
	if err := validateTrustedUpdateHost(resp.Request.URL.String()); err != nil {
		return "", fmt.Errorf("update redirect rejected: %w", err)
	}
	if resp.ContentLength > 0 && resp.ContentLength != expectedSize {
		return "", fmt.Errorf("update size mismatch: got %d, expected %d", resp.ContentLength, expectedSize)
	}

	// Create temp file
	out, err := os.CreateTemp("", AppName+"-update-*"+updateFileExtension(downloadURL))
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tempFile := out.Name()
	keepFile := false
	defer func() {
		_ = out.Close()
		if !keepFile {
			_ = os.Remove(tempFile)
		}
	}()

	// Copy with progress
	total := expectedSize
	var downloaded int64
	h := sha256.New()

	buf := make([]byte, 32*1024) // 32KB buffer
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if downloaded+int64(n) > expectedSize {
				return "", fmt.Errorf("update exceeded expected size %d", expectedSize)
			}
			_, writeErr := out.Write(buf[:n])
			if writeErr != nil {
				return "", fmt.Errorf("failed to write: %w", writeErr)
			}
			downloaded += int64(n)
			_, _ = h.Write(buf[:n])
			if progressCallback != nil {
				progressCallback(downloaded, total)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("download interrupted: %w", err)
		}
	}
	if downloaded != expectedSize {
		return "", fmt.Errorf("update size mismatch: downloaded %d, expected %d", downloaded, expectedSize)
	}
	actualSHA256 := hex.EncodeToString(h.Sum(nil))
	if actualSHA256 != expectedSHA256 {
		return "", fmt.Errorf("update SHA-256 mismatch")
	}

	if err := out.Close(); err != nil {
		return "", fmt.Errorf("failed to finalize update: %w", err)
	}
	keepFile = true
	return tempFile, nil
}

func normalizeGitHubSHA256(digest string) string {
	digest = strings.ToLower(strings.TrimSpace(digest))
	digest = strings.TrimPrefix(digest, "sha256:")
	if len(digest) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return ""
	}
	return digest
}

func updateFileExtension(downloadURL string) string {
	path := strings.Split(downloadURL, "?")[0]
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".zip" || ext == ".exe" {
		return ext
	}
	return ".bin"
}

// compareVersions compares two semver-ish version strings.
// Returns: 1 if v1 > v2, -1 if v1 < v2, 0 if equal.
//
// It splits off any pre-release suffix (after '-', e.g. "1.13.0-alpha.27") before
// comparing the numeric core, then applies the SemVer rule that a version WITH a
// pre-release ranks below the same core WITHOUT one (1.13.0 > 1.13.0-alpha). This
// avoids the old parser silently treating "1.13.0-alpha.27" as equal to "1.13.0".
func compareVersions(v1, v2 string) int {
	core1, pre1 := splitPreRelease(v1)
	core2, pre2 := splitPreRelease(v2)

	if c := compareNumericCore(core1, core2); c != 0 {
		return c
	}

	// Cores equal: no pre-release outranks having one.
	switch {
	case pre1 == "" && pre2 == "":
		return 0
	case pre1 == "":
		return 1
	case pre2 == "":
		return -1
	default:
		return comparePreRelease(pre1, pre2)
	}
}

func splitPreRelease(v string) (core, pre string) {
	v = strings.TrimSpace(v)
	// Drop build metadata ("+..."), then separate the pre-release ("-...").
	if idx := strings.IndexByte(v, '+'); idx >= 0 {
		v = v[:idx]
	}
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		return v[:idx], v[idx+1:]
	}
	return v, ""
}

func compareNumericCore(core1, core2 string) int {
	parts1 := strings.Split(core1, ".")
	parts2 := strings.Split(core2, ".")
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}
	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			fmt.Sscanf(parts1[i], "%d", &n1)
		}
		if i < len(parts2) {
			fmt.Sscanf(parts2[i], "%d", &n2)
		}
		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}
	return 0
}

// comparePreRelease compares two dot-separated pre-release strings per SemVer:
// numeric identifiers compare numerically, alphanumerics lexically, and a larger
// set of fields outranks a smaller one when all preceding fields are equal.
func comparePreRelease(pre1, pre2 string) int {
	a := strings.Split(pre1, ".")
	b := strings.Split(pre2, ".")
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	for i := 0; i < maxLen; i++ {
		if i >= len(a) {
			return -1
		}
		if i >= len(b) {
			return 1
		}
		na, errA := strconv.Atoi(a[i])
		nb, errB := strconv.Atoi(b[i])
		switch {
		case errA == nil && errB == nil:
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		case errA == nil:
			return -1 // numeric identifiers rank lower than alphanumeric
		case errB == nil:
			return 1
		default:
			if a[i] != b[i] {
				if a[i] < b[i] {
					return -1
				}
				return 1
			}
		}
	}
	return 0
}
