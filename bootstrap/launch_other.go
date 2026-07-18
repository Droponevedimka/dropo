//go:build !windows

package main

import "os/exec"

func launchInstalledApplication(path string, args []string) error {
	cmd := exec.Command(path, args...)
	cmd.Dir = filepathDir(path)
	return cmd.Start()
}

func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
