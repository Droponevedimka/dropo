package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// initializeRuntimeVersionFromExecutable restores the packaged version from
// installation metadata only when a development/default value escaped the
// linker flags. Release builds normally never need this fallback.
func initializeRuntimeVersionFromExecutable() {
	if isReleaseVersion(Version) {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		return
	}
	if version := runtimeVersionFromExecutablePath(executable); version != "" {
		Version = version
	}
}

func runtimeVersionFromExecutablePath(executable string) string {
	dir := filepath.Dir(filepath.Clean(executable))
	candidates := []struct {
		path  string
		field string
	}{
		{filepath.Join(filepath.Dir(dir), "install-manifest.json"), "version"},
		{filepath.Join(dir, "install-manifest.json"), "version"},
		{filepath.Join(dir, "dependencies.json"), "appVersion"},
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate.path)
		if err != nil || len(data) == 0 || len(data) > 1<<20 {
			continue
		}
		var metadata map[string]interface{}
		if json.Unmarshal(data, &metadata) != nil {
			continue
		}
		version, _ := metadata[candidate.field].(string)
		version = strings.TrimPrefix(strings.TrimSpace(version), "v")
		if isReleaseVersion(version) {
			return version
		}
	}
	return ""
}

func isReleaseVersion(value string) bool {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if cut := strings.IndexAny(value, "-+"); cut >= 0 {
		value = value[:cut]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}
