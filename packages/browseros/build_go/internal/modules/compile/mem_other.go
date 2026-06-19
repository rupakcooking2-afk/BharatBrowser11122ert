//go:build !windows

package compile

// totalMemoryGB is only consulted on Windows (the RAM cap exists because
// Windows has no overcommit); other platforms use autoninja defaults.
func totalMemoryGB() (float64, bool) { return 0, false }
