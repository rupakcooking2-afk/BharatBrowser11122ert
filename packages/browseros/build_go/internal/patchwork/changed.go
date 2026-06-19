package patchwork

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// PatchChange is one changed chromium_patches/ file in the browseros repo
// (apply/apply_changed.py PatchChange).
type PatchChange struct {
	PatchPath    string // path relative to the browseros repo
	ChromiumPath string // path inside chromium
	ChangeType   string // A/M/D/R/C
}

func rootGit(ctx *buildctx.Context, args ...string) execx.Result {
	res, _ := ctx.Runner.Run(execx.Cmd{Args: append([]string{"git"}, args...), Dir: ctx.RootDir})
	return res
}

// ChangedFilesInCommit lists (status, path) pairs for one browseros-repo
// commit (apply_changed.py get_changed_files_in_commit).
func ChangedFilesInCommit(ctx *buildctx.Context, commit string) ([][2]string, error) {
	res := rootGit(ctx, "diff-tree", "--no-commit-id", "--name-status", "-r", commit)
	if res.Code != 0 {
		return nil, fmt.Errorf("failed to get changed files for commit %s: %s", commit, res.Stderr)
	}
	return parseStatusPairs(res.Stdout), nil
}

// ChangedFilesInRange lists (status, path) pairs for start..end.
func ChangedFilesInRange(ctx *buildctx.Context, start, end string) ([][2]string, error) {
	res := rootGit(ctx, "diff", "--name-status", start+".."+end)
	if res.Code != 0 {
		return nil, fmt.Errorf("failed to get changed files for range %s..%s: %s", start, end, res.Stderr)
	}
	return parseStatusPairs(res.Stdout), nil
}

func parseStatusPairs(out string) [][2]string {
	var changes [][2]string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			changes = append(changes, [2]string{string(parts[0][0]), parts[len(parts)-1]})
		}
	}
	return changes
}

// FilterPatchChanges keeps chromium_patches/ entries and maps them to
// chromium paths (apply_changed.py filter_patch_changes).
func FilterPatchChanges(changes [][2]string) []PatchChange {
	const prefix = "chromium_patches/"
	var out []PatchChange
	for _, change := range changes {
		status, filePath := change[0], change[1]
		if !strings.HasPrefix(filePath, prefix) {
			continue
		}
		chromiumPath := strings.TrimPrefix(filePath, prefix)
		if chromiumPath == "" {
			continue
		}
		switch status {
		case "A", "M", "D", "R", "C":
		default:
			status = "M"
		}
		out = append(out, PatchChange{PatchPath: filePath, ChromiumPath: chromiumPath, ChangeType: status})
	}
	return out
}

// ApplyChangedPatches applies the changed patches with a mandatory reset-to
// base (apply_changed.py apply_changed_patches). Returns (applied,
// resetOnly, failed).
func ApplyChangedPatches(ctx *buildctx.Context, changes []PatchChange, resetTo string, dryRun bool) (int, int, []string) {
	applied, resetOnly := 0, 0
	var failed []string

	for _, change := range changes {
		chromiumPath := change.ChromiumPath
		patchPath := ctx.PatchPathForFile(chromiumPath)

		if change.ChangeType == "D" {
			// Patch deleted → restore the chromium file to base.
			if dryRun {
				logx.Info("  Would reset (patch deleted): " + chromiumPath)
				resetOnly++
				continue
			}
			logx.Info("  Resetting (patch deleted): " + chromiumPath)
			if FileExistsInCommit(ctx, chromiumPath, resetTo) {
				if ResetFileToCommit(ctx, chromiumPath, resetTo) {
					logx.Success(fmt.Sprintf("    ✓ Restored to %.8s: %s", resetTo, chromiumPath))
					resetOnly++
				} else {
					logx.Error("    ✗ Failed to reset: " + chromiumPath)
					failed = append(failed, chromiumPath)
				}
				continue
			}
			target := filepath.Join(ctx.ChromiumSrc, filepath.FromSlash(chromiumPath))
			if _, err := os.Stat(target); err == nil {
				os.Remove(target)
				logx.Success(fmt.Sprintf("    ✓ Deleted (not in %.8s): %s", resetTo, chromiumPath))
			} else {
				logx.Info("    Already absent: " + chromiumPath)
			}
			resetOnly++
			continue
		}

		if _, err := os.Stat(patchPath); err != nil {
			logx.Error("  Patch file not found: " + patchPath)
			failed = append(failed, chromiumPath)
			continue
		}
		ok, _ := ApplySinglePatch(ctx, patchPath, chromiumPath, dryRun, resetTo)
		if ok {
			applied++
		} else {
			failed = append(failed, chromiumPath)
		}
	}
	return applied, resetOnly, failed
}
