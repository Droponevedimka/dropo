package main

// Dependency bootstrap. The portable app ships WITHOUT the heavy bin/ (sing-box,
// winws+WinDivert, wireguard, xray, tg-ws-proxy, filters). Those live in a
// separate release asset `dependencies-<depsVersion>.zip` and are downloaded
// once on first run (or when depsVersion changes), verified by sha256, and
// extracted into bin/. A `dependencies.json` manifest next to dropo.exe declares
// the required depsVersion. See docs/UPDATE.md.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// requiredDepBinaries are the key files that must exist for bin/ to count as
// complete (cheap integrity check before trusting the marker).
var requiredDepBinaries = []string{"sing-box.exe", "winws.exe", "WinDivert.dll"}

// DepsManifest mirrors dependencies.json written by build.ps1.
type DepsManifest struct {
	DepsVersion string `json:"depsVersion"`
	Asset       string `json:"asset"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	AppVersion  string `json:"appVersion"`
	Repo        string `json:"repo"`
	URL         string `json:"url,omitempty"` // optional direct override
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
func (a *App) binDir() string           { return filepath.Join(a.basePath, "bin") }
func (a *App) depsMarkerPath() string   { return filepath.Join(a.binDir(), ".deps-version") }

func (a *App) loadDepsManifest() (*DepsManifest, bool) {
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
	for _, name := range requiredDepBinaries {
		if !fileExists(filepath.Join(a.binDir(), name)) {
			return false
		}
	}
	return true
}

func (a *App) depsPresent(required string) bool {
	return a.installedDepsVersion() == required && a.binLooksComplete()
}

// DependenciesStatus reports whether the bundled binaries are ready. A build that
// still ships bin/ (no manifest) is "unmanaged" and ready if the binaries exist.
func (a *App) DependenciesStatus() DepsStatus {
	m, ok := a.loadDepsManifest()
	if !ok {
		return DepsStatus{Managed: false, Ready: a.binLooksComplete()}
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
	if a.ctx == nil {
		return
	}
	pct := 0
	if total > 0 {
		pct = int(done * 100 / total)
	}
	wailsRuntime.EventsEmit(a.ctx, "deps-progress", map[string]interface{}{
		"done": done, "total": total, "percent": pct, "phase": phase,
	})
}

// DownloadDependencies fetches, verifies and extracts the dependencies asset.
// Idempotent: a no-op if bin/ already matches the required version. Safe to call
// from the frontend; emits `deps-progress`.
func (a *App) DownloadDependencies() error {
	m, ok := a.loadDepsManifest()
	if !ok {
		return nil // bundled build — nothing to fetch
	}
	if a.depsPresent(m.DepsVersion) {
		return nil
	}

	url, err := a.resolveDepsURL(m)
	if err != nil {
		return fmt.Errorf("не найден архив зависимостей %s: %w", m.Asset, err)
	}
	a.writeLog(fmt.Sprintf("[Deps] downloading %s (%d MB) from %s", m.Asset, m.Size/(1024*1024), url))
	a.emitDepsProgress(0, m.Size, "Загрузка компонентов…")

	tmp, err := os.CreateTemp("", "dropo-deps-*.zip")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := a.downloadVerified(url, tmp, m); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	a.emitDepsProgress(m.Size, m.Size, "Распаковка…")
	if err := extractZip(tmpPath, a.binDir()); err != nil {
		return fmt.Errorf("не удалось распаковать зависимости: %w", err)
	}
	if err := os.WriteFile(a.depsMarkerPath(), []byte(m.DepsVersion), 0644); err != nil {
		a.writeLog(fmt.Sprintf("[Deps] failed to write marker: %v", err))
	}
	if !a.binLooksComplete() {
		return fmt.Errorf("архив зависимостей распакован, но ключевые файлы отсутствуют")
	}
	a.refreshSingBoxPath()
	a.writeLog("[Deps] dependencies ready (depsVersion=" + m.DepsVersion + ")")
	a.emitDepsProgress(m.Size, m.Size, "Готово")
	return nil
}

func (a *App) downloadVerified(url string, out *os.File, m *DepsManifest) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", AppName+"/"+Version)
	resp, err := LongHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
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
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			h.Write(buf[:n])
			done += int64(n)
			if time.Since(lastEmit) > 200*time.Millisecond {
				a.emitDepsProgress(done, total, "Загрузка компонентов…")
				lastEmit = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("загрузка прервана: %w", rerr)
		}
	}
	if m.SHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, m.SHA256) {
			return fmt.Errorf("контрольная сумма не совпала (ожидалось %s, получено %s)", m.SHA256, got)
		}
	}
	return nil
}

// resolveDepsURL prefers an explicit manifest URL, then a direct release-download
// URL, then a search across the repo's releases for the asset by name.
func (a *App) resolveDepsURL(m *DepsManifest) (string, error) {
	if m.URL != "" {
		return m.URL, nil
	}
	repo := m.Repo
	if repo == "" {
		repo = GitHubRepo
	}
	if m.AppVersion != "" {
		direct := fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", repo, m.AppVersion, m.Asset)
		if httpResourceExists(direct) {
			return direct, nil
		}
	}
	if url := findReleaseAssetURL(repo, m.Asset); url != "" {
		return url, nil
	}
	return "", fmt.Errorf("asset %s not found in releases of %s", m.Asset, repo)
}

func httpResourceExists(url string) bool {
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
	return resp.StatusCode == http.StatusOK
}

// findReleaseAssetURL scans recent releases for an asset with the given name.
func findReleaseAssetURL(repo, asset string) string {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=30", repo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", AppName+"/"+Version)
	resp, err := ShortHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var releases []GitHubRelease
	if json.NewDecoder(resp.Body).Decode(&releases) != nil {
		return ""
	}
	for _, r := range releases {
		for _, as := range r.Assets {
			if as.Name == asset {
				return as.BrowserDownloadURL
			}
		}
	}
	return ""
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
