package dropocore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type androidGitHubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

func checkAndroidUpdates() string {
	mu.Lock()
	currentVersion := strings.TrimSpace(current.Version.Version)
	if currentVersion == "" {
		currentVersion = "dev"
	}
	repo := strings.TrimSpace(current.Config.GithubRepo)
	if repo == "" {
		repo = "Droponevedimka/dropo"
	}
	mu.Unlock()

	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	client := &http.Client{Timeout: 8 * time.Second}
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "dropo-android-update-check")

	response, err := client.Do(request)
	if err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return encode(map[string]interface{}{"success": false, "error": fmt.Sprintf("GitHub returned %s", response.Status)})
	}

	body, err := readHTTPBodyLimited(response.Body, maxAndroidMetadataBytes)
	if err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	var release androidGitHubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	latestVersion := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	if latestVersion == "" {
		latestVersion = strings.TrimPrefix(strings.TrimSpace(release.Name), "v")
	}
	assetName, downloadURL, fileSize := androidUpdateAsset(release)
	hasUpdate := compareAndroidVersions(latestVersion, currentVersion) > 0

	mu.Lock()
	appendLogLocked(fmt.Sprintf("android update check: current=%s latest=%s update=%v", currentVersion, latestVersion, hasUpdate))
	_ = saveLocked()
	mu.Unlock()

	return encode(map[string]interface{}{
		"success":        true,
		"hasUpdate":      hasUpdate,
		"currentVersion": currentVersion,
		"latestVersion":  latestVersion,
		"downloadURL":    downloadURL,
		"releaseNotes":   release.Body,
		"publishedAt":    release.PublishedAt.Format(time.RFC3339),
		"releaseURL":     release.HTMLURL,
		"fileSize":       fileSize,
		"assetName":      assetName,
		"platform":       "android",
		"selfUpdate":     false,
	})
}

func androidUpdateAsset(release androidGitHubRelease) (string, string, int64) {
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, "android") && strings.HasSuffix(name, ".apk") {
			return asset.Name, asset.BrowserDownloadURL, asset.Size
		}
	}
	for _, asset := range release.Assets {
		if strings.HasSuffix(strings.ToLower(asset.Name), ".apk") {
			return asset.Name, asset.BrowserDownloadURL, asset.Size
		}
	}
	return "", "", 0
}

func compareAndroidVersions(a, b string) int {
	ap := androidVersionParts(a)
	bp := androidVersionParts(b)
	for i := 0; i < len(ap) || i < len(bp); i++ {
		av, bv := 0, 0
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return 0
}

func androidVersionParts(value string) []int {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	value = strings.SplitN(value, "-", 2)[0]
	value = strings.SplitN(value, "+", 2)[0]
	raw := strings.Split(value, ".")
	parts := make([]int, 0, len(raw))
	for _, part := range raw {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			break
		}
		parts = append(parts, n)
	}
	return parts
}
