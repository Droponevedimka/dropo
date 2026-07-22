package main

import "runtime"

type PlatformTarget struct {
	GOOS              string
	GOARCH            string
	ReleaseOS         string
	ReleaseArch       string
	AppAsset          string
	PortableAsset     string
	DependenciesAsset string
	RequiredDeps      []string
	SplitDeps         bool
	SelfUpdate        bool
	Mobile            bool
}

func CurrentPlatformTarget() PlatformTarget {
	return PlatformTargetFor(runtime.GOOS, runtime.GOARCH)
}

func PlatformTargetFor(goos, goarch string) PlatformTarget {
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	switch goos {
	case "windows":
		return PlatformTarget{
			GOOS:              goos,
			GOARCH:            goarch,
			ReleaseOS:         "Windows",
			ReleaseArch:       normalizeReleaseArch(goarch),
			AppAsset:          "dropo-Windows-Setup-" + normalizeReleaseArch(goarch) + ".exe",
			PortableAsset:     "dropo-Windows-Portable-" + normalizeReleaseArch(goarch) + ".zip",
			DependenciesAsset: "dropo-Windows-Dependencies-" + normalizeReleaseArch(goarch) + ".zip",
			RequiredDeps:      []string{"sing-box.exe", "xray.exe", "wireguard.exe", "wg.exe", "wintun.dll", "WinDivert.dll", "WinDivert64.sys"},
			SplitDeps:         false,
			SelfUpdate:        false,
		}
	case "linux":
		return PlatformTarget{
			GOOS:              goos,
			GOARCH:            goarch,
			ReleaseOS:         "Linux",
			ReleaseArch:       normalizeReleaseArch(goarch),
			AppAsset:          "dropo-Linux-" + normalizeReleaseArch(goarch) + ".AppImage",
			DependenciesAsset: "dropo-Linux-Dependencies-" + normalizeReleaseArch(goarch) + ".zip",
			RequiredDeps:      []string{"sing-box"},
			SplitDeps:         true,
		}
	case "darwin":
		return PlatformTarget{
			GOOS:              goos,
			GOARCH:            goarch,
			ReleaseOS:         "macOS",
			ReleaseArch:       normalizeReleaseArch(goarch),
			AppAsset:          "dropo-macOS-" + normalizeReleaseArch(goarch) + ".dmg",
			DependenciesAsset: "dropo-macOS-Dependencies-" + normalizeReleaseArch(goarch) + ".zip",
			RequiredDeps:      []string{"sing-box"},
			SplitDeps:         true,
		}
	case "android":
		return PlatformTarget{
			GOOS:        goos,
			GOARCH:      goarch,
			ReleaseOS:   "Android",
			ReleaseArch: "arm64",
			AppAsset:    "dropo-Android-arm64.apk",
			Mobile:      true,
		}
	case "ios":
		return PlatformTarget{
			GOOS:        goos,
			GOARCH:      goarch,
			ReleaseOS:   "iOS",
			ReleaseArch: "universal",
			AppAsset:    "dropo-iOS.ipa",
			Mobile:      true,
		}
	default:
		return PlatformTarget{
			GOOS:        goos,
			GOARCH:      goarch,
			ReleaseOS:   goos,
			ReleaseArch: normalizeReleaseArch(goarch),
			AppAsset:    "dropo-" + goos + "-" + normalizeReleaseArch(goarch) + ".zip",
		}
	}
}

func normalizeReleaseArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	default:
		return goarch
	}
}

func singBoxBinaryName() string {
	if runtime.GOOS == "windows" {
		return "sing-box.exe"
	}
	return "sing-box"
}

func requiredDependencyFiles() []string {
	target := CurrentPlatformTarget()
	if len(target.RequiredDeps) == 0 {
		return []string{singBoxBinaryName()}
	}
	return append([]string(nil), target.RequiredDeps...)
}
