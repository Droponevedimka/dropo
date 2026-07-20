package main

// Dependency bootstrap. The single-file app payload ships WITHOUT the heavy bin/ (sing-box,
// winws2+WinDivert+Lua, wireguard, xray, tg-ws-proxy, filters). Those live in a
// separate release asset `dependencies-<depsVersion>.zip` and are downloaded
// once on first run (or when depsVersion changes), verified by sha256, and
// extracted into bin/. A `dependencies.json` manifest next to dropo.exe declares
// the required depsVersion.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const depsDownloadIdleTimeout = 45 * time.Second

// App-only release builds inject these values from deps-lock.json into the
// signed core. In that mode dependencies.json is informational only: changing
// it cannot redirect an elevated core or weaken the required archive hash.
var (
	trustedDepsVersion  string
	trustedDepsAsset    string
	trustedDepsSHA256   string
	trustedDepsSize     string
	trustedDepsRequired string
)

// DepsManifest mirrors dependencies.json written by build.ps1.
type DepsManifest struct {
	DepsVersion string   `json:"depsVersion"`
	Platform    string   `json:"platform,omitempty"`
	Arch        string   `json:"arch,omitempty"`
	Asset       string   `json:"asset"`
	SHA256      string   `json:"sha256"`
	Size        int64    `json:"size"`
	AppVersion  string   `json:"appVersion"`
	Repo        string   `json:"repo"`
	URL         string   `json:"url,omitempty"` // optional direct override
	Required    []string `json:"requiredFiles,omitempty"`
}

// DepsStatus is reported to the frontend so it can gate first-run download.
type DepsStatus struct {
	Managed   bool   `json:"managed"`   // split build (manifest present)
	Ready     bool   `json:"ready"`     // bin/ present and matches required version
	Required  string `json:"required"`  // required depsVersion
	Installed string `json:"installed"` // installed depsVersion (marker)
	SizeMB    int64  `json:"sizeMB"`    // download size, for the UI
}

func (a *App) depsManifestPath() string { return filepath.Join(a.basePath, "dependencies.json") }
func (a *App) binDir() string {
	if base := a.runtimeBasePath(); base != "" {
		return filepath.Join(base, "bin")
	}
	return ""
}
func (a *App) depsMarkerPath() string  { return filepath.Join(a.binDir(), ".deps-version") }
func (a *App) depsArchivePath() string { return filepath.Join(a.runtimeBasePath(), "dependencies.zip") }

func (a *App) loadDepsManifest() (*DepsManifest, bool) {
	if strings.TrimSpace(trustedDepsSHA256) != "" {
		size, err := strconv.ParseInt(trustedDepsSize, 10, 64)
		sha := strings.ToLower(strings.TrimSpace(trustedDepsSHA256))
		if err != nil || size <= 0 || len(sha) != sha256.Size*2 ||
			trustedDepsVersion == "" || trustedDepsAsset == "" {
			return nil, false
		}
		if _, err := hex.DecodeString(sha); err != nil {
			return nil, false
		}
		required := make([]string, 0)
		for _, name := range strings.Split(trustedDepsRequired, ",") {
			if name = strings.TrimSpace(name); name != "" {
				required = append(required, name)
			}
		}
		return &DepsManifest{
			DepsVersion: trustedDepsVersion,
			Platform:    "windows",
			Arch:        "x64",
			Asset:       trustedDepsAsset,
			SHA256:      sha,
			Size:        size,
			Repo:        GitHubRepo,
			Required:    required,
		}, true
	}
	data, err := os.ReadFile(a.depsManifestPath())
	if err != nil {
		return nil, false
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // tolerate UTF-8 BOM
	var m DepsManifest
	if json.Unmarshal(data, &m) != nil || m.DepsVersion == "" {
		return nil, false
	}
	return &m, true
}

func (a *App) installedDepsVersion() string {
	data, err := os.ReadFile(a.depsMarkerPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (a *App) binLooksComplete() bool {
	required := requiredDependencyFiles()
	if m, ok := a.loadDepsManifest(); ok && len(m.Required) > 0 {
		required = m.Required
	}
	for _, name := range required {
		if !fileExists(filepath.Join(a.binDir(), name)) {
			return false
		}
	}
	return true
}

func (a *App) depsPresent(required string) bool {
	if a.installedDepsVersion() != required || !a.binLooksComplete() {
		return false
	}
	if strings.TrimSpace(trustedDepsSHA256) == "" {
		return true
	}
	a.depsIntegrityMu.Lock()
	defer a.depsIntegrityMu.Unlock()
	if a.depsIntegrityOK && a.depsIntegrityFor == required {
		return true
	}
	m, ok := a.loadDepsManifest()
	verified := ok && verifyFileSHA256AndSize(a.depsArchivePath(), m.SHA256, m.Size) == nil &&
		extractedFilesMatchArchive(a.depsArchivePath(), a.binDir()) == nil
	a.depsIntegrityFor = required
	a.depsIntegrityOK = verified
	return verified
}

// DependenciesStatus reports whether the bundled binaries are ready. A build that
// still ships bin/ (no manifest) is "unmanaged" and ready if the binaries exist.
func (a *App) DependenciesStatus() DepsStatus {
	m, ok := a.loadDepsManifest()
	if !ok {
		return DepsStatus{Managed: false, Ready: a.binLooksComplete()}
	}
	if a.runtimePathErr != nil {
		return DepsStatus{Managed: true, Ready: false, Required: m.DepsVersion, SizeMB: m.Size / (1024 * 1024)}
	}
	return DepsStatus{
		Managed:   true,
		Ready:     a.depsPresent(m.DepsVersion),
		Required:  m.DepsVersion,
		Installed: a.installedDepsVersion(),
		SizeMB:    m.Size / (1024 * 1024),
	}
}

func (a *App) emitDepsProgress(done, total int64, phase string) {
	pct := 0
	if total > 0 {
		pct = int(done * 100 / total)
	}
	a.emitEvent("deps-progress", map[string]interface{}{
		"done": done, "total": total, "percent": pct, "phase": phase,
	})
}

func (a *App) failDepsDownload(m *DepsManifest, err error) error {
	message := fmt.Sprintf("Не удалось загрузить компоненты: %v", err)
	a.writeLog("[Deps] " + message)
	if m != nil {
		a.emitDepsProgress(0, m.Size, "Ошибка загрузки")
	}
	a.emitEvent("deps-error", map[string]interface{}{
		"error":        message,
		"telegramName": TelegramUpdatesName,
		"telegramURL":  TelegramUpdatesURL,
	})
	return fmt.Errorf("%s. Если ошибка повторяется, обратитесь к администратору: %s", message, TelegramUpdatesName)
}

// DownloadDependencies fetches, verifies and extracts the dependencies asset.
// Idempotent: a no-op if bin/ already matches the required version. Safe to call
// from the frontend; emits `deps-progress`.
func (a *App) DownloadDependencies() error {
	a.depsDownloadMu.Lock()
	defer a.depsDownloadMu.Unlock()

	m, ok := a.loadDepsManifest()
	if !ok {
		return nil // bundled build — nothing to fetch
	}
	if a.runtimePathErr != nil || a.runtimeBasePath() == "" {
		return a.failDepsDownload(m, fmt.Errorf("protected dependency runtime is unavailable: %v", a.runtimePathErr))
	}
	if a.depsPresent(m.DepsVersion) {
		return nil
	}

	url, err := a.resolveDepsURL(m)
	if err != nil {
		return a.failDepsDownload(m, fmt.Errorf("не найден архив зависимостей %s: %w", m.Asset, err))
	}
	a.writeLog(fmt.Sprintf("[Deps] downloading %s (%d MB) from trusted Russian release mirror", m.Asset, m.Size/(1024*1024)))
	a.emitDepsProgress(0, m.Size, "Загрузка компонентов…")

	tmp, err := os.CreateTemp(a.runtimeBasePath(), "dropo-deps-*.zip")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := a.downloadVerified(url, tmp, m); err != nil {
		tmp.Close()
		return a.failDepsDownload(m, err)
	}
	tmp.Close()

	a.emitDepsProgress(m.Size, m.Size, "Распаковка…")
	if err := extractZip(tmpPath, a.binDir()); err != nil {
		return a.failDepsDownload(m, fmt.Errorf("не удалось распаковать зависимости: %w", err))
	}
	if strings.TrimSpace(trustedDepsSHA256) != "" {
		if err := os.Remove(a.depsArchivePath()); err != nil && !os.IsNotExist(err) {
			return a.failDepsDownload(m, fmt.Errorf("replace dependencies cache: %w", err))
		}
		if err := os.Rename(tmpPath, a.depsArchivePath()); err != nil {
			return a.failDepsDownload(m, fmt.Errorf("cache verified dependencies archive: %w", err))
		}
	}
	if err := os.WriteFile(a.depsMarkerPath(), []byte(m.DepsVersion), 0644); err != nil {
		a.writeLog(fmt.Sprintf("[Deps] failed to write marker: %v", err))
	}
	if !a.binLooksComplete() {
		return a.failDepsDownload(m, fmt.Errorf("архив зависимостей распакован, но ключевые файлы отсутствуют"))
	}
	if strings.TrimSpace(trustedDepsSHA256) != "" {
		if err := extractedFilesMatchArchive(a.depsArchivePath(), a.binDir()); err != nil {
			return a.failDepsDownload(m, fmt.Errorf("verify extracted dependencies: %w", err))
		}
		a.depsIntegrityMu.Lock()
		a.depsIntegrityFor = m.DepsVersion
		a.depsIntegrityOK = true
		a.depsIntegrityMu.Unlock()
	}
	a.refreshSingBoxPath()
	a.writeLog("[Deps] dependencies ready (depsVersion=" + m.DepsVersion + ")")
	a.emitDepsProgress(m.Size, m.Size, "Готово")
	return nil
}

func (a *App) downloadVerified(url string, out *os.File, m *DepsManifest) error {
	productionDownload := strings.TrimSpace(trustedDepsSHA256) != ""
	if productionDownload {
		if err := validateTrustedUpdateHost(url); err != nil {
			return fmt.Errorf("untrusted dependencies URL: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", AppName+"/"+Version)
	resp, err := LongHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if productionDownload {
		if err := validateTrustedUpdateHost(resp.Request.URL.String()); err != nil {
			return fmt.Errorf("dependencies redirect rejected: %w", err)
		}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("сервер вернул статус %d", resp.StatusCode)
	}

	total := m.Size
	if total <= 0 {
		total = resp.ContentLength
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	var done int64
	lastEmit := time.Now()
	var stalled atomic.Bool
	var lastProgress atomic.Int64
	lastProgress.Store(time.Now().UnixNano())
	watchdogDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if time.Since(time.Unix(0, lastProgress.Load())) > depsDownloadIdleTimeout {
					stalled.Store(true)
					cancel()
					return
				}
			case <-watchdogDone:
				return
			}
		}
	}()
	defer close(watchdogDone)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			h.Write(buf[:n])
			done += int64(n)
			lastProgress.Store(time.Now().UnixNano())
			if time.Since(lastEmit) > 200*time.Millisecond {
				a.emitDepsProgress(done, total, "Загрузка компонентов…")
				lastEmit = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if stalled.Load() {
				return fmt.Errorf("загрузка не получила данных больше %s", depsDownloadIdleTimeout)
			}
			return fmt.Errorf("загрузка прервана: %w", rerr)
		}
	}
	if m.Size > 0 && done != m.Size {
		return fmt.Errorf("download size mismatch (expected %d, got %d)", m.Size, done)
	}
	if m.SHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, m.SHA256) {
			return fmt.Errorf("контрольная сумма не совпала (ожидалось %s, получено %s)", m.SHA256, got)
		}
	}
	return nil
}

func verifyFileSHA256AndSize(path, expected string, expectedSize int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if expectedSize > 0 && info.Size() != expectedSize {
		return fmt.Errorf("size mismatch")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), strings.TrimSpace(expected)) {
		return fmt.Errorf("SHA-256 mismatch")
	}
	return nil
}

func extractedFilesMatchArchive(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		targetAbs, err := filepath.Abs(filepath.Join(destDir, entry.Name))
		if err != nil || (targetAbs != destAbs && !strings.HasPrefix(targetAbs, destAbs+string(os.PathSeparator))) {
			return fmt.Errorf("archive entry escapes destination: %s", entry.Name)
		}
		extracted, err := os.Open(targetAbs)
		if err != nil {
			return err
		}
		archived, err := entry.Open()
		if err != nil {
			extracted.Close()
			return err
		}
		hExtracted, hArchived := sha256.New(), sha256.New()
		_, errExtracted := io.Copy(hExtracted, extracted)
		_, errArchived := io.Copy(hArchived, archived)
		extracted.Close()
		archived.Close()
		if errExtracted != nil || errArchived != nil {
			return fmt.Errorf("hash archive entry %s", entry.Name)
		}
		if !bytes.Equal(hExtracted.Sum(nil), hArchived.Sum(nil)) {
			return fmt.Errorf("extracted file differs from trusted archive: %s", entry.Name)
		}
	}
	return nil
}

// resolveDepsURL searches releases from newest to oldest for the freshest asset
// matching the signed name, size and SHA-256. Production builds never trust a
// fixed release tag or an adjacent manifest URL.
func (a *App) resolveDepsURL(m *DepsManifest) (string, error) {
	repo := m.Repo
	if repo == "" {
		repo = GitHubRepo
	}
	if url := findReleaseAssetURL(repo, m.Asset, m.Size, m.SHA256); url != "" {
		return url, nil
	}
	// Direct URLs remain available only to unsigned development builds and tests.
	if strings.TrimSpace(trustedDepsSHA256) == "" && m.URL != "" && httpResourceLooksUsable(m.URL, m.Size) {
		return m.URL, nil
	}
	return "", fmt.Errorf("asset %s with expected size %d not found in releases of %s", m.Asset, m.Size, repo)
}

func httpResourceLooksUsable(url string, expectedSize int64) bool {
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", AppName+"/"+Version)
	resp, err := ShortHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	if expectedSize > 0 && resp.ContentLength > 0 && resp.ContentLength != expectedSize {
		return false
	}
	return true
}

// findReleaseAssetURL scans every published release through the trusted Russian
// mirror in newest-first order. There is intentionally no direct GitHub fallback.
func findReleaseAssetURL(repo, asset string, expectedSize int64, expectedSHA256 string) string {
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", ReleaseMirrorBaseURL, repo, page)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return ""
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", AppName+"/"+Version)
		resp, err := HTTPClient.Do(req)
		if err != nil {
			break
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			break
		}
		body, err := readHTTPBodyLimited(resp.Body, maxReleaseMetadataBytes)
		resp.Body.Close()
		if err != nil {
			break
		}
		var releases []GitHubRelease
		if json.Unmarshal(body, &releases) != nil {
			break
		}
		if url := findReleaseAssetURLIn(releases, asset, expectedSize, expectedSHA256); url != "" {
			return url
		}
		if len(releases) < 100 {
			break
		}
	}
	return ""
}

func findReleaseAssetURLIn(releases []GitHubRelease, asset string, expectedSize int64, expectedSHA256 string) string {
	for _, release := range releases {
		for _, candidate := range release.Assets {
			if releaseAssetMatches(candidate, asset, expectedSize, expectedSHA256) {
				return candidate.BrowserDownloadURL
			}
		}
	}
	return ""
}

func releaseAssetMatches(as GitHubReleaseAsset, asset string, expectedSize int64, expectedSHA256 string) bool {
	if as.Name != asset {
		return false
	}
	if expectedSize > 0 && as.Size > 0 && as.Size != expectedSize {
		return false
	}
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if expectedSHA256 != "" && as.Digest != "" && normalizeGitHubSHA256(as.Digest) != expectedSHA256 {
		return false
	}
	return validateTrustedUpdateHost(as.BrowserDownloadURL) == nil
}

// extractZip unpacks src into destDir, guarding against path traversal.
func extractZip(src, destDir string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		target := filepath.Join(destDir, f.Name)
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if absTarget != destAbs && !strings.HasPrefix(absTarget, destAbs+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			rc.Close()
			return err
		}
		_, cerr := io.Copy(out, rc)
		out.Close()
		rc.Close()
		if cerr != nil {
			return cerr
		}
	}
	return nil
}
