//go:build !darwin && !linux

package platform

// nativeArch is unused on other platforms (Windows hardcodes x64 for parity
// with Python's get_platform_arch).
func nativeArch() string { return "" }
