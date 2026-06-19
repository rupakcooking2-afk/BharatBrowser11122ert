//go:build darwin

package platform

import "golang.org/x/sys/unix"

// nativeArch reports the machine's real CPU architecture.
// hw.optional.arm64 is 1 on Apple Silicon even when the process runs under
// Rosetta — deliberately stronger than Python's platform.machine(), which
// reports the translated arch there. Pass --arch to target x64 explicitly.
func nativeArch() string {
	if v, err := unix.SysctlUint32("hw.optional.arm64"); err == nil && v == 1 {
		return "arm64"
	}
	return ""
}
