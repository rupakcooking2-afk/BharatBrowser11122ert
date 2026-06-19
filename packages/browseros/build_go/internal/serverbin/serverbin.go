// Package serverbin ports build/common/server_binaries.py: shared sign
// metadata for BrowserOS Server binaries, consumed by both the build signing
// path and the OTA release path.
package serverbin

import (
	"path/filepath"
	"strings"
)

// SignSpec is per-binary codesign metadata. Entitlements is the plist
// filename under resources/entitlements/ ("" = none).
type SignSpec struct {
	IdentifierSuffix string
	Options          string
	Entitlements     string
}

// MacOSServerBinaries maps binary stems to their sign specs.
var MacOSServerBinaries = map[string]SignSpec{
	"browseros_server": {"browseros_server", "runtime", "browseros-executable-entitlements.plist"},
	"bun":              {"bun", "runtime", "browseros-executable-entitlements.plist"},
	"codex":            {"codex", "runtime", ""},
	"claude":           {"claude", "runtime", ""},
	"rg":               {"rg", "runtime", ""},
}

// WindowsServerBinaries are the relative paths under resources/bin.
var WindowsServerBinaries = []string{
	"browseros_server.exe",
	"third_party/codex.exe",
	"third_party/claude.exe",
}

// MacOSSignSpecFor looks up sign metadata by file stem (e.g. "codex").
func MacOSSignSpecFor(binaryPath string) (SignSpec, bool) {
	stem := strings.TrimSuffix(filepath.Base(binaryPath), filepath.Ext(binaryPath))
	spec, ok := MacOSServerBinaries[stem]
	return spec, ok
}

// ExpectedWindowsBinaryPaths resolves the relative list against a
// resources/bin dir.
func ExpectedWindowsBinaryPaths(serverBinDir string) []string {
	paths := make([]string, 0, len(WindowsServerBinaries))
	for _, rel := range WindowsServerBinaries {
		paths = append(paths, filepath.Join(serverBinDir, filepath.FromSlash(rel)))
	}
	return paths
}
