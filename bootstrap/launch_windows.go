//go:build windows

package main

import "os/exec"

// Delegate launching to the interactive Explorer process. This is important
// for self-update: the elevated core starts the update helper, but the new UI
// must return to the user's normal desktop token instead of inheriting admin.
func launchInstalledApplication(path string, _ []string) error {
	return exec.Command("explorer.exe", path).Start()
}
