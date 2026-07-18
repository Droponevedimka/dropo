//go:build !windows

package main

func resetWindowsSystemProxyNativeForPorts(_ []int) (bool, string, error) {
	return false, "", nil
}

func killWindowsDropoManagedSidecarsNative(_ []string, _ []string) ([]int, error) {
	return nil, nil
}
