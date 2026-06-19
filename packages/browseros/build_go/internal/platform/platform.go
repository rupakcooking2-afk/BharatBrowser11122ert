// Package platform identifies the host OS/arch using the same naming the
// Python build tool uses (common/utils.py: get_platform, get_platform_arch).
package platform

import "runtime"

// Platform holds the build-tool view of an OS/arch pair.
// OS is one of "macos", "linux", "windows"; Arch is "x64" or "arm64".
type Platform struct {
	OS   string
	Arch string
}

// Current returns the host platform.
func Current() Platform {
	return Platform{OS: currentOS(), Arch: currentArch()}
}

func currentOS() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	case "linux":
		return "linux"
	}
	return "unknown"
}

func currentArch() string {
	// Python's get_platform_arch hardcodes x64 on Windows and maps machine
	// names elsewhere; unknown machines fall back to x64. Prefer the probed
	// machine arch (platform.machine() equivalent) over runtime.GOARCH so an
	// emulated binary (e.g. amd64 under Rosetta) still reports the host.
	if runtime.GOOS == "windows" {
		return "x64"
	}
	if native := nativeArch(); native != "" {
		return native
	}
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	case "amd64":
		return "x64"
	}
	return "x64"
}

func (p Platform) IsWindows() bool { return p.OS == "windows" }
func (p Platform) IsMacOS() bool   { return p.OS == "macos" }
func (p Platform) IsLinux() bool   { return p.OS == "linux" }

// ExeExt returns the executable extension for the platform.
func (p Platform) ExeExt() string {
	if p.IsWindows() {
		return ".exe"
	}
	return ""
}
