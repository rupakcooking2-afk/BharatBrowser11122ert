//go:build windows

package compile

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// totalMemoryGB reports physical RAM via GlobalMemoryStatusEx
// (standard.py _windows_total_memory_gb).
func totalMemoryGB() (float64, bool) {
	var status memoryStatusEx
	status.dwLength = uint32(unsafe.Sizeof(status))
	ret, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ret == 0 {
		return 0, false
	}
	return float64(status.ullTotalPhys) / (1 << 30), true
}
