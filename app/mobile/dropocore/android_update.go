package dropocore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const androidReleaseMirrorBaseURL = "https://downloads.droponevedimka.ru"

type androidGitHubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

func checkAndroidUpdates() string {
	mu.Lock()
	currentVersion := strings.TrimPrefix(strings.TrimSpace(current.Version.Version), "v")
	repo := strings.TrimSpace(current.Config.GithubRepo)
	if repo == "" {
		repo = "Droponevedimka/dropo"
	}
	mu.Unlock()

	if !isAndroidReleaseVersion(currentVersion) {
		return encode(map[string]interface{}{
			"success":        false,
			"currentVersion": currentVersion,
			"error":          "current Android application version is unavailable",
		})
	}
	client := &http.Client{Timeout: 30 * time.Second}
	return checkAndroidUpdatesWithClient(client, androidReleaseMirrorBaseURL, repo, currentVersion)
}

func checkAndroidUpdatesWithClient(client *http.Client, baseURL, repo, currentVersion string) string {
	requestURL := strings.TrimRight(baseURL, "/") + "/repos/" + repo + "/releases?per_page=100"
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "dropo-android-update-check/"+currentVersion)

	response, err := client.Do(request)
	if err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return encode(map[string]interface{}{"success": false, "error": fmt.Sprintf("release mirror returned %s", response.Status)})
	}

	body, err := readHTTPBodyLimited(response.Body, maxAndroidMetadataBytes)
	if err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	var releases []androidGitHubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	release, latestVersion, assetName, downloadURL, fileSize, found := selectLatestAndroidRelease(releases, baseURL)
	if !found {
		return encode(map[string]interface{}{
			"success":        true,
			"hasUpdate":      false,
			"currentVersion": currentVersion,
			"latestVersion":  currentVersion,
			"platform":       "android",
			"selfUpdate":     false,
		})
	}
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

func selectLatestAndroidRelease(releases []androidGitHubRelease, baseURL string) (androidGitHubRelease, string, string, string, int64, bool) {
	var selected androidGitHubRelease
	var selectedVersion, selectedAsset, selectedURL string
	var selectedSize int64
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		version := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
		if version == "" {
			version = strings.TrimPrefix(strings.TrimSpace(release.Name), "v")
		}
		if !isAndroidReleaseVersion(version) || (selectedVersion != "" && compareAndroidVersions(version, selectedVersion) <= 0) {
			continue
		}
		assetName, downloadURL, fileSize := androidUpdateAsset(release)
		if assetName == "" || fileSize <= 0 || !trustedAndroidDownloadURL(downloadURL, baseURL) {
			continue
		}
		selected = release
		selectedVersion = version
		selectedAsset = assetName
		selectedURL = downloadURL
		selectedSize = fileSize
	}
	return selected, selectedVersion, selectedAsset, selectedURL, selectedSize, selectedVersion != ""
}

func trustedAndroidDownloadURL(rawURL, baseURL string) bool {
	candidate, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || candidate.User != nil || !strings.EqualFold(path.Ext(candidate.Path), ".apk") {
		return false
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return candidate.Scheme == base.Scheme && strings.EqualFold(candidate.Host, base.Host)
}

func androidUpdateAsset(release androidGitHubRelease) (string, string, int64) {
	for _, asset := range release.Assets {
		if strings.EqualFold(asset.Name, "dropo-Android-arm64.apk") {
			return asset.Name, asset.BrowserDownloadURL, asset.Size
		}
	}
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, "android") && strings.HasSuffix(name, ".apk") {
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

func isAndroidReleaseVersion(value string) bool {
	return len(androidVersionParts(value)) == 3
}

func androidVersionParts(value string) []int {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	value = strings.SplitN(value, "-", 2)[0]
	value = strings.SplitN(value, "+", 2)[0]
	raw := strings.Split(value, ".")
	if len(raw) != 3 {
		return nil
	}
	parts := make([]int, 0, len(raw))
	for _, part := range raw {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 {
			return nil
		}
		parts = append(parts, n)
	}
	return parts
}
