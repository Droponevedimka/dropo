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
	Available        bool   `json:"available"`
	Version          string `json:"version"`
	CurrentVersion   string `json:"current_version"`
	Description      string `json:"description"`
	DownloadURL      string `json:"download_url"`
	ReleaseURL       string `json:"release_url"`
	PublishedAt      string `json:"published_at"`
	FileSize         int64  `json:"file_size"`
	AssetName        string `json:"asset_name"`
	SHA256           string `json:"sha256"`
	DistributionMode string `json:"distribution_mode"`
}

// CheckForUpdates checks the trusted Russian release gateway and the canonical
// GitHub metadata. The gateway can expose only assets that it mirrors (Android
// today), while Windows Setup/Portable remain available on GitHub.
func CheckForUpdates() (*UpdateInfo, error) {
	return checkForUpdatesWithClient(
		HTTPClient,
		[]string{ReleaseMirrorBaseURL, GitHubAPIBaseURL},
		GitHubRepo,
		Version,
		runtime.GOOS,
		runtime.GOARCH,
		currentDistributionMode(),
	)
}

func checkForUpdatesWithClient(client *http.Client, metadataBaseURLs []string, repo, appVersion, goos, goarch, distributionMode string) (*UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultHTTPTimeout)
	defer cancel()

	currentVersion := strings.TrimPrefix(strings.TrimSpace(appVersion), "v")
	if !isReleaseVersion(currentVersion) {
		return nil, fmt.Errorf("current application version is unavailable")
	}

	// Releases may be platform-specific. Do not use /latest: it can point to an
	// Android-only release. Merge recent releases from every successful source
	// and choose the newest installable asset for this exact distribution mode.
	type sourceResult struct {
		releases []GitHubRelease
		err      error
	}
	results := make(chan sourceResult, len(metadataBaseURLs))
	for _, baseURL := range metadataBaseURLs {
		baseURL := baseURL
		go func() {
			sourceReleases, err := fetchUpdateReleases(ctx, client, baseURL, repo, appVersion)
			results <- sourceResult{releases: sourceReleases, err: err}
		}()
	}

	var releases []GitHubRelease
	var sourceErrors []string
	successfulSources := 0
	remaining := len(metadataBaseURLs)
collectResults:
	for remaining > 0 {
		select {
		case result := <-results:
			remaining--
			if result.err != nil {
				sourceErrors = append(sourceErrors, result.err.Error())
				continue
			}
			successfulSources++
			releases = append(releases, result.releases...)
			if candidate, _, found := selectLatestInstallableReleaseForMode(releases, goos, goarch, distributionMode); found {
				candidateVersion := strings.TrimPrefix(strings.TrimSpace(candidate.TagName), "v")
				if compareVersions(candidateVersion, currentVersion) > 0 {
					// A confirmed compatible update is enough to notify the user.
					// Do not make startup wait for a blocked secondary endpoint.
					cancel()
					break collectResults
				}
			}
		case <-ctx.Done():
			sourceErrors = append(sourceErrors, ctx.Err().Error())
			break collectResults
		}
	}
	if successfulSources == 0 {
		return nil, fmt.Errorf("failed to check for updates: %s", strings.Join(sourceErrors, "; "))
	}

	release, asset, found := selectLatestInstallableReleaseForMode(releases, goos, goarch, distributionMode)
	if !found {
		// A partial gateway response (for example Android-only) is not proof that
		// Windows is current when the canonical source failed. Return an error so
		// startup performs its bounded retry instead of caching a false negative.
		if len(sourceErrors) > 0 {
			return nil, fmt.Errorf("no compatible update metadata: %s", strings.Join(sourceErrors, "; "))
		}
		return &UpdateInfo{
			Available:      false,
			CurrentVersion: currentVersion,
		}, nil
	}

	// Extract version from tag (remove 'v' prefix if present)
	latestVersion := strings.TrimPrefix(release.TagName, "v")
	// Compare versions
	available := compareVersions(latestVersion, currentVersion) > 0
	if !available && len(sourceErrors) > 0 {
		return nil, fmt.Errorf("canonical update metadata is incomplete: %s", strings.Join(sourceErrors, "; "))
	}

	return &UpdateInfo{
		Available:        available,
		Version:          latestVersion,
		CurrentVersion:   currentVersion,
		Description:      release.Body,
		DownloadURL:      asset.BrowserDownloadURL,
		ReleaseURL:       release.HTMLURL,
		PublishedAt:      release.PublishedAt.Format("02.01.2006"),
		FileSize:         asset.Size,
		AssetName:        asset.Name,
		SHA256:           normalizeGitHubSHA256(asset.Digest),
		DistributionMode: distributionMode,
	}, nil
}

func fetchUpdateReleases(ctx context.Context, client *http.Client, baseURL, repo, appVersion string) ([]GitHubRelease, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=100", strings.TrimRight(baseURL, "/"), repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", baseURL, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", AppName+"/"+appVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned status %d", baseURL, resp.StatusCode)
	}
	body, err := readHTTPBodyLimited(resp.Body, maxReleaseMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", baseURL, err)
	}
	var releases []GitHubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("%s returned invalid release metadata: %w", baseURL, err)
	}
	return releases, nil
}

func selectLatestInstallableRelease(releases []GitHubRelease, goos, goarch string) (GitHubRelease, GitHubReleaseAsset, bool) {
	return selectLatestInstallableReleaseForMode(releases, goos, goarch, distributionModeInstalled)
}

func selectLatestInstallableReleaseForMode(releases []GitHubRelease, goos, goarch, distributionMode string) (GitHubRelease, GitHubReleaseAsset, bool) {
	filtered := make([]GitHubRelease, 0, len(releases))
	for _, release := range releases {
		asset, ok := selectUpdateAssetForMode(release.Assets, goos, goarch, distributionMode)
		if !ok || asset.Size <= 0 || asset.Size > maxUpdateDownloadBytes || normalizeGitHubSHA256(asset.Digest) == "" {
			continue
		}
		if goos == "windows" && validateTrustedUpdateURL(asset.BrowserDownloadURL) != nil {
			continue
		}
		release.Assets = []GitHubReleaseAsset{asset}
		filtered = append(filtered, release)
	}
	return selectLatestCompatibleReleaseForMode(filtered, goos, goarch, distributionMode)
}

func selectLatestCompatibleRelease(releases []GitHubRelease, goos, goarch string) (GitHubRelease, GitHubReleaseAsset, bool) {
	return selectLatestCompatibleReleaseForMode(releases, goos, goarch, distributionModeInstalled)
}

func selectLatestCompatibleReleaseForMode(releases []GitHubRelease, goos, goarch, distributionMode string) (GitHubRelease, GitHubReleaseAsset, bool) {
	var selectedRelease GitHubRelease
	var selectedAsset GitHubReleaseAsset
	selectedVersion := ""
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		asset, ok := selectUpdateAssetForMode(release.Assets, goos, goarch, distributionMode)
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
	return selectUpdateAssetForMode(assets, goos, goarch, distributionModeInstalled)
}

func selectUpdateAssetForMode(assets []GitHubReleaseAsset, goos, goarch, distributionMode string) (GitHubReleaseAsset, bool) {
	target := PlatformTargetFor(goos, goarch)
	preferredAsset := target.AppAsset
	if goos == "windows" && distributionMode == distributionModePortable {
		preferredAsset = target.PortableAsset
	}
	if preferredAsset != "" {
		for _, asset := range assets {
			if strings.EqualFold(asset.Name, preferredAsset) {
				return asset, true
			}
		}
	}

	switch goos {
	case "windows":
		return selectWindowsUpdateAssetForMode(assets, distributionMode)
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
	return selectWindowsUpdateAssetForMode(assets, distributionModeInstalled)
}

func selectWindowsUpdateAssetForMode(assets []GitHubReleaseAsset, distributionMode string) (GitHubReleaseAsset, bool) {
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		if distributionMode == distributionModePortable {
			if strings.Contains(name, "windows") && strings.Contains(name, "portable") && strings.HasSuffix(name, ".zip") {
				return asset, true
			}
			continue
		}
		if strings.Contains(name, "windows") && strings.Contains(name, "setup") && strings.HasSuffix(name, ".exe") && !strings.Contains(name, "dependencies") {
			return asset, true
		}
	}
	// Compatibility with releases published before installer/portable assets
	// were split. New releases should always use the explicit Setup name.
	if distributionMode != distributionModePortable {
		for _, asset := range assets {
			name := strings.ToLower(asset.Name)
			if strings.Contains(name, "windows") && strings.HasSuffix(name, ".exe") && !strings.Contains(name, "dependencies") {
				return asset, true
			}
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
	if err := validateTrustedAppUpdateSourceURL(rawURL); err != nil {
		return err
	}
	if ext := updateFileExtension(rawURL); ext != ".exe" && ext != ".zip" {
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

func validateTrustedAppUpdateSourceURL(rawURL string) error {
	if err := validateTrustedUpdateHost(rawURL); err == nil {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "github.com" {
		return fmt.Errorf("untrusted application update URL")
	}
	expectedPrefix := "/" + strings.ToLower(GitHubRepo) + "/releases/download/"
	if !strings.HasPrefix(strings.ToLower(parsed.EscapedPath()), expectedPrefix) {
		return fmt.Errorf("untrusted GitHub update repository")
	}
	return nil
}

func validateTrustedUpdateRedirect(initialURL, finalURL string) error {
	if err := validateTrustedAppUpdateSourceURL(initialURL); err != nil {
		return err
	}
	if err := validateTrustedAppUpdateSourceURL(finalURL); err == nil {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(finalURL))
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "release-assets.githubusercontent.com" {
		return fmt.Errorf("untrusted update redirect")
	}
	// GitHub release asset URLs redirect to this signed CDN. The downloaded
	// bytes remain pinned by both the API-reported size and SHA-256 digest.
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
	if err := validateTrustedUpdateRedirect(downloadURL, resp.Request.URL.String()); err != nil {
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
