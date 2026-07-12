package main

import (
	"os"
	"path/filepath"
	"runtime"
)

func resolveUserDataPath() string {
	if override := os.Getenv("DROPO_DATA_DIR"); override != "" {
		return filepath.Clean(override)
	}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, AppDataDirName)
		}
	}
	if configDir, err := os.UserConfigDir(); err == nil && configDir != "" {
		return filepath.Join(configDir, AppDataDirName)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "."+AppDataDirName)
	}
	return filepath.Join(os.TempDir(), AppDataDirName)
}
