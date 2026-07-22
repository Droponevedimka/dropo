package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	distributionModeInstalled = "installed"
	distributionModePortable  = "portable"
	installModeMarkerName     = "install-mode.json"
)

type installModeMarker struct {
	Mode string `json:"mode"`
}

// currentDistributionMode distinguishes an installer-managed copy from the
// portable ZIP. The marker is written only by the Windows installer and lives
// next to the user-facing launcher under Program Files.
func currentDistributionMode() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	executable, err := os.Executable()
	if err != nil {
		return distributionModePortable
	}
	root, _ := resolvePortableInstallRoot(filepath.Dir(executable))
	return distributionModeAt(root)
}

func distributionModeAt(root string) string {
	data, err := os.ReadFile(filepath.Join(filepath.Clean(root), installModeMarkerName))
	if err != nil {
		return distributionModePortable
	}
	var marker installModeMarker
	if json.Unmarshal(data, &marker) != nil || !strings.EqualFold(strings.TrimSpace(marker.Mode), distributionModeInstalled) {
		return distributionModePortable
	}
	return distributionModeInstalled
}
