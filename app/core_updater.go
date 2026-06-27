package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// GitHubRelease represents a GitHub release.
type GitHubRelease struct {
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	Body        string               `json:"body"`
	PublishedAt time.Time            `json:"published_at"`
	HTMLURL     string               `json:"html_url"`
	Assets      []GitHubReleaseAsset `json:"assets"`
}

// GitHubReleaseAsset represents an asset attached to a GitHub release.
type GitHubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
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
}

// CheckForUpdates checks for updates on GitHub.
func CheckForUpdates() (*UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ShortHTTPTimeout)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", GitHubRepo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", AppName+"/"+Version)

	resp, err := ShortHTTPClient.Do(req)
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
		return nil, fmt.Errorf("GitHub returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var release GitHubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub response: %w", err)
	}

	// Extract version from tag (remove 'v' prefix if present)
	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentVersion := strings.TrimPrefix(Version, "v")

	// Compare versions
	available := compareVersions(latestVersion, currentVersion) > 0

	asset, hasAsset := selectUpdateAsset(release.Assets)
	var downloadURL string
	var fileSize int64
	var assetName string
	if hasAsset {
		downloadURL = asset.BrowserDownloadURL
		fileSize = asset.Size
		assetName = asset.Name
	}

	return &UpdateInfo{
		Available:      available,
		Version:        latestVersion,
		CurrentVersion: currentVersion,
		Description:    release.Body,
		DownloadURL:    downloadURL,
		ReleaseURL:     release.HTMLURL,
		PublishedAt:    release.PublishedAt.Format("02.01.2006"),
		FileSize:       fileSize,
		AssetName:      assetName,
	}, nil
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
		if strings.Contains(name, "windows") && strings.Contains(name, "portable") && strings.HasSuffix(name, ".zip") && !strings.Contains(name, "dependencies") {
			return asset, true
		}
	}
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

// DownloadUpdate downloads the update file to temp directory.
func DownloadUpdate(downloadURL string, progressCallback func(downloaded, total int64)) (string, error) {
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

	// Create temp file
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, AppName+"_update"+updateFileExtension(downloadURL))

	out, err := os.Create(tempFile)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer out.Close()

	// Copy with progress
	total := resp.ContentLength
	var downloaded int64

	buf := make([]byte, 32*1024) // 32KB buffer
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := out.Write(buf[:n])
			if writeErr != nil {
				return "", fmt.Errorf("failed to write: %w", writeErr)
			}
			downloaded += int64(n)
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

	return tempFile, nil
}

func updateFileExtension(downloadURL string) string {
	path := strings.Split(downloadURL, "?")[0]
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".zip" || ext == ".exe" {
		return ext
	}
	return ".bin"
}

// compareVersions compares two version strings.
// Returns: 1 if v1 > v2, -1 if v1 < v2, 0 if equal.
func compareVersions(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

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
