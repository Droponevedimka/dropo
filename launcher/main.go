package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	root := filepath.Dir(exePath)
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

	cmd := exec.Command(uiPath, os.Args[1:]...)
	cmd.Dir = filepath.Dir(uiPath)
	if err := cmd.Run(); err != nil {
		_ = showLauncherError(fmt.Sprintf("Не удалось запустить dropo:\n%v", err))
	}
}
