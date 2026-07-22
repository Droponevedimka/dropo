package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Injected by build.ps1 after the core and UI have been signed. A release
// launcher refuses to elevate or execute files that do not match these hashes.
var (
	expectedCoreSHA256 string
	expectedUISHA256   string
)

func main() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	root := filepath.Dir(exePath)
	if len(os.Args) > 1 && os.Args[1] == "--start-core" {
		corePath := filepath.Join(root, "resources", "dropo-core.exe")
		if err := verifyFileSHA256(corePath, expectedCoreSHA256); err != nil {
			_ = showLauncherError(fmt.Sprintf("Проверка целостности dropo-core.exe не пройдена:\n%v", err))
			os.Exit(1)
		}
		if err := startElevatedCore(corePath); err != nil {
			_ = showLauncherError(fmt.Sprintf("Не удалось запустить dropo core с правами администратора:\n%v", err))
			os.Exit(1)
		}
		return
	}

	uiPath := filepath.Join(root, "resources", "dropo-ui.exe")
	if _, err := os.Stat(uiPath); err != nil {
		legacyPath := filepath.Join(root, "resources", "app", "dropo-ui.exe")
		if _, legacyErr := os.Stat(legacyPath); legacyErr == nil {
			uiPath = legacyPath
		}
	}
	if _, err := os.Stat(uiPath); err != nil {
		_ = showLauncherError(fmt.Sprintf("dropo UI не найден:\n%s", uiPath))
		return
	}
	if err := verifyFileSHA256(uiPath, expectedUISHA256); err != nil {
		_ = showLauncherError(fmt.Sprintf("Проверка целостности dropo-ui.exe не пройдена:\n%v", err))
		return
	}

	uiArgs := os.Args[1:]
	if len(uiArgs) > 0 && uiArgs[0] == "--autostart" {
		uiArgs = uiArgs[1:]
	}
	cmd := exec.Command(uiPath, uiArgs...)
	cmd.Dir = filepath.Dir(uiPath)
	if err := cmd.Run(); err != nil {
		_ = showLauncherError(fmt.Sprintf("Не удалось запустить dropo:\n%v", err))
	}
}

func hasLauncherArg(want string) bool {
	for _, arg := range os.Args[1:] {
		if arg == want {
			return true
		}
	}
	return false
}
