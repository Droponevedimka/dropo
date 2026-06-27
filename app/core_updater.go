package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GitHubRelease represents a GitHub release.
type GitHubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
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

	// Find suitable asset for download
	var downloadURL string
	var fileSize int64
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, "windows") && strings.HasSuffix(name, ".exe") {
			downloadURL = asset.BrowserDownloadURL
			fileSize = asset.Size
			break
		}
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
	}, nil
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
	tempFile := filepath.Join(tempDir, AppName+"_update.exe")

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
