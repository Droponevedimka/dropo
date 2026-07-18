package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	installManifestName = "install-manifest.json"
	maxPayloadFiles     = 20000
	maxExtractedBytes   = int64(1 << 30)
)

var (
	appVersion             = "dev"
	expectedPayloadSHA256  = ""
	expectedManifestSHA256 = ""
)

type installManifest struct {
	Version string                `json:"version"`
	Files   []installManifestFile `json:"files"`
}

type installManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func main() {
	if err := installAndLaunch(); err != nil {
		writeBootstrapError(err)
		showBootstrapError("Не удалось подготовить приложение:\n\n" + err.Error())
		os.Exit(1)
	}
}

func writeBootstrapError(installErr error) {
	root := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if root == "" {
		return
	}
	dir := filepath.Join(root, "dropo")
	if os.MkdirAll(dir, 0755) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "bootstrap-error.log"), []byte(installErr.Error()+"\n"), 0600)
}

func installAndLaunch() error {
	if len(embeddedPayload) == 0 {
		return errors.New("установочный пакет не встроен")
	}
	if !validVersion(appVersion) {
		return errors.New("некорректная версия установочного пакета")
	}
	if !hashMatches(embeddedPayload, expectedPayloadSHA256) {
		return errors.New("нарушена целостность встроенного пакета")
	}

	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if localAppData == "" {
		userConfig, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("не найден AppData: %w", err)
		}
		localAppData = userConfig
	}
	appRoot := filepath.Join(localAppData, "dropo", "app")
	installDir := filepath.Join(appRoot, appVersion)
	if err := verifyInstallation(installDir); err != nil {
		if err := installPayload(appRoot, installDir); err != nil {
			return err
		}
		if err := verifyInstallation(installDir); err != nil {
			return fmt.Errorf("проверка установленного приложения не пройдена: %w", err)
		}
	}
	if hasArgument("--install-only") {
		return nil
	}

	launcher := filepath.Join(installDir, "dropo.exe")
	if err := launchInstalledApplication(launcher, forwardedArgs()); err != nil {
		return fmt.Errorf("не удалось запустить %s: %w", launcher, err)
	}
	return nil
}

func installPayload(appRoot, installDir string) error {
	if err := os.MkdirAll(appRoot, 0755); err != nil {
		return fmt.Errorf("не удалось создать каталог приложения: %w", err)
	}
	tempDir, err := os.MkdirTemp(appRoot, ".install-"+appVersion+"-")
	if err != nil {
		return fmt.Errorf("не удалось создать временный каталог: %w", err)
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.RemoveAll(tempDir)
		}
	}()

	if err := extractPayload(tempDir, embeddedPayload); err != nil {
		return err
	}
	if err := verifyInstallation(tempDir); err != nil {
		return fmt.Errorf("встроенный пакет повреждён: %w", err)
	}

	backupDir := installDir + ".old"
	_ = os.RemoveAll(backupDir)
	hadPrevious := false
	if _, err := os.Stat(installDir); err == nil {
		if err := os.Rename(installDir, backupDir); err != nil {
			return fmt.Errorf("не удалось заменить предыдущую установку: %w", err)
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tempDir, installDir); err != nil {
		if hadPrevious {
			_ = os.Rename(backupDir, installDir)
		}
		return fmt.Errorf("не удалось активировать установку: %w", err)
	}
	keepTemp = true
	if hadPrevious {
		_ = os.RemoveAll(backupDir)
	}
	return nil
}

func extractPayload(destination string, payload []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return fmt.Errorf("не удалось открыть встроенный архив: %w", err)
	}
	if len(reader.File) == 0 || len(reader.File) > maxPayloadFiles {
		return fmt.Errorf("некорректное число файлов в пакете: %d", len(reader.File))
	}

	var extracted int64
	for _, entry := range reader.File {
		rel, err := safeRelativePath(entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("символические ссылки запрещены: %s", entry.Name)
		}
		target := filepath.Join(destination, rel)
		// PowerShell Compress-Archive can emit Windows directory entries whose
		// external attributes do not set the Unix directory bit. Preserve the
		// trailing separator check so such entries never become zero-byte files.
		if entry.FileInfo().IsDir() || strings.HasSuffix(entry.Name, "/") || strings.HasSuffix(entry.Name, "\\") {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		extracted += int64(entry.UncompressedSize64)
		if extracted > maxExtractedBytes {
			return errors.New("распакованный пакет превышает допустимый размер")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		in, err := entry.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(in, int64(entry.UncompressedSize64)+1))
		closeErr := out.Close()
		_ = in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func verifyInstallation(installDir string) error {
	manifestPath := filepath.Join(installDir, installManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if !hashMatches(data, expectedManifestSHA256) {
		return errors.New("манифест установки изменён")
	}
	var manifest installManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if manifest.Version != appVersion || len(manifest.Files) == 0 || len(manifest.Files) > maxPayloadFiles {
		return errors.New("манифест установки не соответствует версии")
	}
	launcherFound := false
	for _, item := range manifest.Files {
		rel, err := safeRelativePath(item.Path)
		if err != nil {
			return err
		}
		if strings.EqualFold(filepath.ToSlash(rel), "dropo.exe") {
			launcherFound = true
		}
		path := filepath.Join(installDir, rel)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
			return fmt.Errorf("файл отсутствует или изменён: %s", item.Path)
		}
		if err := verifyFile(path, item.SHA256); err != nil {
			return fmt.Errorf("файл изменён: %s", item.Path)
		}
	}
	if !launcherFound {
		return errors.New("в манифесте отсутствует dropo.exe")
	}
	return nil
}

func verifyFile(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected) {
		return errors.New("SHA-256 mismatch")
	}
	return nil
}

func hashMatches(data []byte, expected string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if len(expected) != sha256.Size*2 {
		return false
	}
	actual := sha256.Sum256(data)
	return hex.EncodeToString(actual[:]) == expected
}

func safeRelativePath(name string) (string, error) {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	clean := filepath.Clean(filepath.FromSlash(name))
	if name == "" || strings.HasPrefix(name, "/") || clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.VolumeName(clean) != "" {
		return "", fmt.Errorf("небезопасный путь в пакете: %q", name)
	}
	return clean, nil
}

func validVersion(version string) bool {
	if version == "" || len(version) > 64 {
		return false
	}
	for _, char := range version {
		if (char < '0' || char > '9') && char != '.' && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func forwardedArgs() []string {
	args := make([]string, 0, len(os.Args)-1)
	for _, arg := range os.Args[1:] {
		if arg == "--from-update" || arg == "--install-only" {
			continue
		}
		args = append(args, arg)
	}
	return args
}

func hasArgument(want string) bool {
	for _, arg := range os.Args[1:] {
		if strings.EqualFold(arg, want) {
			return true
		}
	}
	return false
}
