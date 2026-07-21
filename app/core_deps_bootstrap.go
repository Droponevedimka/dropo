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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const depsDownloadIdleTimeout = 45 * time.Second

const depsAntivirusMarkerName = ".deps-antivirus-blocked.json"

var antivirusOptionalDependencies = map[string]struct{}{
	"winws2.exe": {},
}

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
	Managed           bool     `json:"managed"`           // split build (manifest present)
	Ready             bool     `json:"ready"`             // trusted runtime is usable, possibly without an AV-blocked optional engine
	Degraded          bool     `json:"degraded"`          // optional packet engine is blocked by endpoint protection
	Required          string   `json:"required"`          // required depsVersion
	Installed         string   `json:"installed"`         // installed depsVersion (marker)
	SizeMB            int64    `json:"sizeMB"`            // download size, for the UI
	BlockedComponents []string `json:"blockedComponents"` // exact optional files unavailable to the runtime
	Warning           string   `json:"warning,omitempty"`
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
func (a *App) depsAntivirusMarkerPath() string {
	return filepath.Join(a.runtimeBasePath(), depsAntivirusMarkerName)
}

type depsAntivirusMarker struct {
	DepsVersion string   `json:"depsVersion"`
	Components  []string `json:"components"`
}

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
	ready, _ := a.depsPresentWithBlocked(required)
	return ready
}

func (a *App) depsPresentWithBlocked(required string) (bool, []string) {
	if a.installedDepsVersion() != required {
		return false, nil
	}
	if strings.TrimSpace(trustedDepsSHA256) == "" {
		return a.binLooksComplete(), nil
	}
	a.depsIntegrityMu.Lock()
	defer a.depsIntegrityMu.Unlock()
	if a.depsIntegrityOK && a.depsIntegrityFor == required {
		return true, append([]string(nil), a.depsIntegrityBlocked...)
	}
	m, ok := a.loadDepsManifest()
	if !ok || verifyFileSHA256AndSize(a.depsArchivePath(), m.SHA256, m.Size) != nil {
		a.setDepsIntegrityStateLocked(required, false, nil)
		return false, nil
	}
	allowed := a.loadDepsAntivirusMarker(required)
	blocked, err := extractedFilesMatchArchiveAllowBlocked(a.depsArchivePath(), a.binDir(), allowed)
	if err != nil {
		a.setDepsIntegrityStateLocked(required, false, nil)
		return false, nil
	}
	a.setDepsIntegrityStateLocked(required, true, blocked)
	if len(blocked) == 0 {
		_ = os.Remove(a.depsAntivirusMarkerPath())
	} else {
		_ = a.saveDepsAntivirusMarker(required, blocked)
	}
	return true, append([]string(nil), blocked...)
}

func (a *App) setDepsIntegrityStateLocked(required string, ok bool, blocked []string) {
	a.depsIntegrityFor = required
	a.depsIntegrityOK = ok
	a.depsIntegrityBlocked = append(a.depsIntegrityBlocked[:0], blocked...)
}

func (a *App) depsComponentBlocked(name string) bool {
	if a == nil {
		return false
	}
	a.depsIntegrityMu.Lock()
	defer a.depsIntegrityMu.Unlock()
	for _, component := range a.depsIntegrityBlocked {
		if strings.EqualFold(component, name) {
			return true
		}
	}
	return false
}

func (a *App) markDepsComponentBlocked(name string) {
	name = strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if _, ok := antivirusOptionalDependencies[name]; !ok || a == nil {
		return
	}
	m, ok := a.loadDepsManifest()
	if !ok {
		return
	}
	a.depsIntegrityMu.Lock()
	defer a.depsIntegrityMu.Unlock()
	blocked := uniqueSortedStrings(append(a.depsIntegrityBlocked, name))
	if a.depsIntegrityFor == "" {
		a.depsIntegrityFor = m.DepsVersion
	}
	a.depsIntegrityBlocked = blocked
	_ = a.saveDepsAntivirusMarker(m.DepsVersion, blocked)
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
	ready, blocked := a.depsPresentWithBlocked(m.DepsVersion)
	status := DepsStatus{
		Managed:           true,
		Ready:             ready,
		Degraded:          len(blocked) > 0,
		Required:          m.DepsVersion,
		Installed:         a.installedDepsVersion(),
		SizeMB:            m.Size / (1024 * 1024),
		BlockedComponents: blocked,
	}
	if status.Degraded {
		status.Warning = "Microsoft Defender заблокировал дополнительный движок zapret2; VPN-подписка продолжит работать, локальный обход будет временно отключён"
	}
	return status
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
	if ready, blocked := a.depsPresentWithBlocked(m.DepsVersion); ready {
		if len(blocked) == 0 {
			return nil
		}
		return a.retryAntivirusBlockedDependencies(m, blocked)
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

	// Status polling verifies the cached archive concurrently with first-run
	// download. Keep extraction, cache replacement and verification in one
	// integrity critical section so Windows never sees an archive being removed
	// while another goroutine still has it open.
	a.depsIntegrityMu.Lock()
	defer a.depsIntegrityMu.Unlock()
	a.setDepsIntegrityStateLocked(m.DepsVersion, false, nil)

	a.emitDepsProgress(m.Size, m.Size, "Распаковка…")
	productionRuntime := strings.TrimSpace(trustedDepsSHA256) != ""
	extractBlocked, err := extractZipAllowAntimalware(tmpPath, a.binDir(), productionRuntime)
	if err != nil {
		return a.failDepsDownload(m, fmt.Errorf("не удалось распаковать зависимости: %w", err))
	}
	if productionRuntime {
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
	blocked := append([]string(nil), extractBlocked...)
	if productionRuntime {
		allowed := make(map[string]struct{}, len(blocked))
		for _, component := range blocked {
			allowed[component] = struct{}{}
		}
		verifiedBlocked, err := extractedFilesMatchArchiveAllowBlocked(a.depsArchivePath(), a.binDir(), allowed)
		if err != nil {
			return a.failDepsDownload(m, fmt.Errorf("verify extracted dependencies: %w", err))
		}
		blocked = uniqueSortedStrings(append(blocked, verifiedBlocked...))
		if err := a.saveDepsAntivirusMarker(m.DepsVersion, blocked); err != nil && !os.IsNotExist(err) {
			return a.failDepsDownload(m, fmt.Errorf("record Defender compatibility state: %w", err))
		}
		a.setDepsIntegrityStateLocked(m.DepsVersion, true, blocked)
	} else if !a.binLooksComplete() {
		return a.failDepsDownload(m, fmt.Errorf("архив зависимостей распакован, но ключевые файлы отсутствуют"))
	}
	a.refreshSingBoxPath()
	if len(blocked) > 0 {
		a.writeLog(fmt.Sprintf("[Security][Defender] trusted dependency archive sha256=%s verified, but optional component(s) %v were blocked by endpoint protection; continuing in subscription-only degraded mode without exclusions", m.SHA256, blocked))
		a.emitEvent("deps-warning", map[string]interface{}{
			"warning":           "Microsoft Defender заблокировал zapret2. VPN/VLESS продолжит работать; локальный обход временно отключён. Обновите базы Defender и повторите проверку позже.",
			"blockedComponents": blocked,
		})
	}
	a.writeLog("[Deps] dependencies ready (depsVersion=" + m.DepsVersion + ")")
	a.emitDepsProgress(m.Size, m.Size, "Готово")
	return nil
}

func (a *App) retryAntivirusBlockedDependencies(m *DepsManifest, blocked []string) error {
	a.depsIntegrityMu.Lock()
	defer a.depsIntegrityMu.Unlock()
	if err := verifyFileSHA256AndSize(a.depsArchivePath(), m.SHA256, m.Size); err != nil {
		a.setDepsIntegrityStateLocked(m.DepsVersion, false, nil)
		return a.failDepsDownload(m, fmt.Errorf("verify cached dependencies before Defender retry: %w", err))
	}
	retryBlocked, err := extractSelectedDependencies(a.depsArchivePath(), a.binDir(), blocked)
	if err != nil {
		return a.failDepsDownload(m, fmt.Errorf("retry Defender-blocked components: %w", err))
	}
	allowed := make(map[string]struct{}, len(blocked)+len(retryBlocked))
	for _, component := range append(append([]string(nil), blocked...), retryBlocked...) {
		allowed[strings.ToLower(filepath.Base(component))] = struct{}{}
	}
	remaining, err := extractedFilesMatchArchiveAllowBlocked(a.depsArchivePath(), a.binDir(), allowed)
	if err != nil {
		return a.failDepsDownload(m, fmt.Errorf("verify dependencies after Defender retry: %w", err))
	}
	remaining = uniqueSortedStrings(remaining)
	if err := a.saveDepsAntivirusMarker(m.DepsVersion, remaining); err != nil && !os.IsNotExist(err) {
		return a.failDepsDownload(m, fmt.Errorf("update Defender compatibility state: %w", err))
	}
	a.setDepsIntegrityStateLocked(m.DepsVersion, true, remaining)
	if len(remaining) > 0 {
		a.writeLog(fmt.Sprintf("[Security][Defender] optional component(s) %v are still blocked; subscription-only degraded mode remains active", remaining))
		a.emitEvent("deps-warning", map[string]interface{}{
			"warning":           "Компонент zapret2 всё ещё блокируется Defender. Обновите базы Microsoft Defender и повторите проверку.",
			"blockedComponents": remaining,
		})
		return nil
	}
	a.writeLog("[Security][Defender] previously blocked zapret2 component was restored from the already verified dependency archive; full Windows Unified mode is available again")
	a.emitEvent("deps-progress", map[string]interface{}{"done": m.Size, "total": m.Size, "percent": 100, "phase": "Компонент восстановлен"})
	return nil
}

func extractSelectedDependencies(archivePath, destDir string, selected []string) ([]string, error) {
	wanted := make(map[string]struct{}, len(selected))
	for _, component := range selected {
		name := strings.ToLower(filepath.Base(component))
		if _, optional := antivirusOptionalDependencies[name]; optional {
			wanted[name] = struct{}{}
		}
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return nil, err
	}
	found := make(map[string]bool, len(wanted))
	stillBlocked := make([]string, 0, len(wanted))
	for _, entry := range zr.File {
		name := strings.ToLower(filepath.Base(entry.Name))
		if entry.FileInfo().IsDir() {
			continue
		}
		if _, ok := wanted[name]; !ok {
			continue
		}
		found[name] = true
		targetAbs, pathErr := filepath.Abs(filepath.Join(destDir, entry.Name))
		if pathErr != nil || (targetAbs != destAbs && !strings.HasPrefix(targetAbs, destAbs+string(os.PathSeparator))) {
			return nil, fmt.Errorf("archive entry escapes destination: %s", entry.Name)
		}
		if err := os.MkdirAll(filepath.Dir(targetAbs), 0755); err != nil {
			return nil, err
		}
		rc, openErr := entry.Open()
		if openErr != nil {
			return nil, openErr
		}
		out, createErr := os.OpenFile(targetAbs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if createErr != nil {
			rc.Close()
			if isAntimalwareBlockError(createErr) {
				stillBlocked = append(stillBlocked, name)
				continue
			}
			return nil, createErr
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil || closeErr != nil {
			writeErr := copyErr
			if writeErr == nil {
				writeErr = closeErr
			}
			if isAntimalwareBlockError(writeErr) {
				_ = os.Remove(targetAbs)
				stillBlocked = append(stillBlocked, name)
				continue
			}
			return nil, writeErr
		}
	}
	for name := range wanted {
		if !found[name] {
			return nil, fmt.Errorf("trusted archive is missing optional component %s", name)
		}
	}
	return uniqueSortedStrings(stillBlocked), nil
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
	_, err := extractedFilesMatchArchiveAllowBlocked(archivePath, destDir, nil)
	return err
}

func extractedFilesMatchArchiveAllowBlocked(archivePath, destDir string, previouslyBlocked map[string]struct{}) ([]string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return nil, err
	}
	blocked := make([]string, 0, 1)
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		targetAbs, err := filepath.Abs(filepath.Join(destDir, entry.Name))
		if err != nil || (targetAbs != destAbs && !strings.HasPrefix(targetAbs, destAbs+string(os.PathSeparator))) {
			return nil, fmt.Errorf("archive entry escapes destination: %s", entry.Name)
		}
		extracted, err := os.Open(targetAbs)
		if err != nil {
			name := strings.ToLower(filepath.Base(entry.Name))
			_, optional := antivirusOptionalDependencies[name]
			_, recorded := previouslyBlocked[name]
			if optional && (isAntimalwareBlockError(err) || (recorded && os.IsNotExist(err))) {
				blocked = append(blocked, name)
				continue
			}
			return nil, err
		}
		archived, err := entry.Open()
		if err != nil {
			extracted.Close()
			return nil, err
		}
		hExtracted, hArchived := sha256.New(), sha256.New()
		_, errExtracted := io.Copy(hExtracted, extracted)
		_, errArchived := io.Copy(hArchived, archived)
		extracted.Close()
		archived.Close()
		if errExtracted != nil || errArchived != nil {
			return nil, fmt.Errorf("hash archive entry %s", entry.Name)
		}
		if !bytes.Equal(hExtracted.Sum(nil), hArchived.Sum(nil)) {
			return nil, fmt.Errorf("extracted file differs from trusted archive: %s", entry.Name)
		}
	}
	sort.Strings(blocked)
	return blocked, nil
}

func isAntimalwareBlockError(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && (errno == syscall.Errno(225) || errno == syscall.Errno(226)) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"contains a virus or potentially unwanted software",
		"virus or potentially unwanted software",
		"содержит вирус",
		"нежелательное программное обеспечение",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func (a *App) loadDepsAntivirusMarker(required string) map[string]struct{} {
	allowed := make(map[string]struct{})
	data, err := os.ReadFile(a.depsAntivirusMarkerPath())
	if err != nil {
		return allowed
	}
	var marker depsAntivirusMarker
	if json.Unmarshal(data, &marker) != nil || marker.DepsVersion != required {
		return allowed
	}
	for _, component := range marker.Components {
		name := strings.ToLower(filepath.Base(component))
		if _, ok := antivirusOptionalDependencies[name]; ok {
			allowed[name] = struct{}{}
		}
	}
	return allowed
}

func (a *App) saveDepsAntivirusMarker(required string, blocked []string) error {
	if len(blocked) == 0 {
		return os.Remove(a.depsAntivirusMarkerPath())
	}
	marker := depsAntivirusMarker{DepsVersion: required, Components: append([]string(nil), blocked...)}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return os.WriteFile(a.depsAntivirusMarkerPath(), data, 0600)
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
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
	_, err := extractZipAllowAntimalware(src, destDir, false)
	return err
}

func extractZipAllowAntimalware(src, destDir string, allowOptional bool) ([]string, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, err
	}
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return nil, err
	}
	blocked := make([]string, 0, 1)
	for _, f := range zr.File {
		target := filepath.Join(destDir, f.Name)
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return nil, err
		}
		if absTarget != destAbs && !strings.HasPrefix(absTarget, destAbs+string(os.PathSeparator)) {
			return nil, fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return nil, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return nil, err
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			rc.Close()
			name := strings.ToLower(filepath.Base(f.Name))
			if _, optional := antivirusOptionalDependencies[name]; allowOptional && optional && isAntimalwareBlockError(err) {
				blocked = append(blocked, name)
				continue
			}
			return nil, err
		}
		_, cerr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if cerr != nil || closeErr != nil {
			writeErr := cerr
			if writeErr == nil {
				writeErr = closeErr
			}
			name := strings.ToLower(filepath.Base(f.Name))
			if _, optional := antivirusOptionalDependencies[name]; allowOptional && optional && isAntimalwareBlockError(writeErr) {
				_ = os.Remove(target)
				blocked = append(blocked, name)
				continue
			}
			return nil, writeErr
		}
	}
	sort.Strings(blocked)
	return blocked, nil
}
