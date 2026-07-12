//go:build !windows

package main

// systemInspectorSupported is false until a native external-VPN inspector exists
// for this platform. The SystemInspector seam is detectExternalVPNConflicts:
// future Linux (netlink/`ip`) and macOS (SystemConfiguration/`scutil`) builds
// should implement it in their own *_linux.go / *_darwin.go files and flip this
// flag, mirroring vpn_conflict_windows.go.
const systemInspectorSupported = false

func detectExternalVPNConflicts() ([]ExternalVPNConflict, error) {
	return nil, nil
}
