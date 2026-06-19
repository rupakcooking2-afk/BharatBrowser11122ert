// Package paths locates the browseros package root (packages/browseros/),
// mirroring build/common/paths.py. The Python tool walks up from its own
// source file; a static binary instead walks up from the working directory,
// with BROWSEROS_ROOT as an explicit override.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var nameRe = regexp.MustCompile(`(?m)^name\s*=\s*["']browseros["']`)

// Root finds the browseros package root: $BROWSEROS_ROOT if set, otherwise
// the nearest ancestor of the working directory whose pyproject.toml declares
// name = "browseros".
func Root() (string, error) {
	if override := os.Getenv("BROWSEROS_ROOT"); override != "" {
		if !isPackageRoot(override) {
			return "", fmt.Errorf(
				"BROWSEROS_ROOT=%s is not the browseros package root (no pyproject.toml with name = 'browseros')",
				override)
		}
		return override, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return RootFrom(cwd)
}

// RootFrom walks up from start looking for the package-root marker.
func RootFrom(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if isPackageRoot(current) {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", fmt.Errorf(
		"could not find browseros package root: expected a pyproject.toml with name = 'browseros' "+
			"in ancestors of %s (run from inside the BrowserOS repo or set BROWSEROS_ROOT)", start)
}

func isPackageRoot(dir string) bool {
	content, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if err != nil {
		return false
	}
	return nameRe.Match(content)
}
