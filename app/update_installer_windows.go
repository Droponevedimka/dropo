//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var safeUpdateVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)

// stageInstalledUpdate moves the already verified download out of the user's
// writable temporary directory and into the protected installation tree. This
// closes the validate-then-elevate replacement window.
func stageInstalledUpdate(downloadPath, version string, expectedSize int64, expectedSHA256 string) (string, error) {
	if !safeUpdateVersion.MatchString(version) {
		return "", fmt.Errorf("invalid update version")
	}
	if err := verifyFileSHA256AndSize(downloadPath, expectedSHA256, expectedSize); err != nil {
		return "", fmt.Errorf("verify downloaded installer: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	installRoot, _ := resolvePortableInstallRoot(filepath.Dir(executable))
	if _, err := os.Stat(filepath.Join(installRoot, installModeMarkerName)); err != nil {
		return "", fmt.Errorf("installed-mode marker is unavailable: %w", err)
	}
	updatesDir := filepath.Join(installRoot, "updates")
	if err := os.Mkdir(updatesDir, 0755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("create protected updates directory: %w", err)
	}
	if err := rejectWindowsReparsePoint(updatesDir); err != nil {
		return "", err
	}

	target := filepath.Join(updatesDir, "dropo-update-"+version+".exe")
	temporary, err := os.CreateTemp(updatesDir, ".dropo-update-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create staged installer: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	source, err := os.Open(downloadPath)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(temporary, source)
	closeSourceErr := source.Close()
	syncErr := temporary.Sync()
	closeTargetErr := temporary.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeSourceErr != nil {
		return "", closeSourceErr
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeTargetErr != nil {
		return "", closeTargetErr
	}
	if err := verifyFileSHA256AndSize(temporaryPath, expectedSHA256, expectedSize); err != nil {
		return "", fmt.Errorf("verify staged installer: %w", err)
	}
	_ = os.Remove(target)
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", fmt.Errorf("commit staged installer: %w", err)
	}
	committed = true
	_ = os.Remove(downloadPath)
	return target, nil
}

// startInstalledUpdate delegates the protected setup executable to Explorer.
// Explorer starts it with the interactive user's token, so the installer owns
// the UAC prompt and can later restart the UI unelevated.
func startInstalledUpdate(packagePath string, expectedSize int64, expectedSHA256 string) error {
	packagePath = filepath.Clean(packagePath)
	if !strings.EqualFold(filepath.Ext(packagePath), ".exe") {
		return fmt.Errorf("installed updates require a setup executable")
	}
	if err := verifyFileSHA256AndSize(packagePath, expectedSHA256, expectedSize); err != nil {
		return fmt.Errorf("update package is unavailable or changed: %w", err)
	}
	return exec.Command("explorer.exe", packagePath).Start()
}
