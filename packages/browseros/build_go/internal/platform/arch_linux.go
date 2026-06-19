//go:build linux

package platform

import "golang.org/x/sys/unix"

// nativeArch reports the machine's real CPU architecture via uname(2), like
// Python's platform.machine() — robust when the binary runs under emulation.
func nativeArch() string {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return ""
	}
	machine := unix.ByteSliceToString(uts.Machine[:])
	switch machine {
	case "x86_64", "AMD64":
		return "x64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return ""
}
