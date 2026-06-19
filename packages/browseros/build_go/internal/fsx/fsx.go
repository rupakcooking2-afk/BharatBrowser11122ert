// Package fsx holds filesystem helpers. RemoveAll mirrors
// build/common/utils.py safe_rmtree: on Windows, read-only files make plain
// removal fail, so a failed attempt clears write bits and retries.
package fsx

import (
	"os"
	"path/filepath"
)

// RemoveAll removes path like os.RemoveAll, retrying once after clearing
// read-only modes when the first attempt fails (Windows junk like read-only
// .pack files must not abort a clean).
func RemoveAll(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, statErr := os.Stat(p); statErr == nil {
			os.Chmod(p, info.Mode()|0o200)
		}
		return nil
	})
	return os.RemoveAll(path)
}
