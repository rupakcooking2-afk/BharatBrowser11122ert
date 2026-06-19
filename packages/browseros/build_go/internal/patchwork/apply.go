package patchwork

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/config"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

func git(ctx *buildctx.Context, args ...string) execx.Result {
	res, _ := ctx.Runner.Run(execx.Cmd{Args: append([]string{"git"}, args...), Dir: ctx.ChromiumSrc})
	return res
}

// FileExistsInCommit checks `git cat-file -e commit:path` (apply/utils.py).
func FileExistsInCommit(ctx *buildctx.Context, filePath, commit string) bool {
	return git(ctx, "cat-file", "-e", commit+":"+filePath).Code == 0
}

// ResetFileToCommit checks a single file out of a commit.
func ResetFileToCommit(ctx *buildctx.Context, filePath, commit string) bool {
	return git(ctx, "checkout", commit, "--", filePath).Code == 0
}

func resetBeforeApply(ctx *buildctx.Context, displayPath, resetTo string) {
	if FileExistsInCommit(ctx, displayPath, resetTo) {
		logx.Info(fmt.Sprintf("  Resetting to %.8s: %s", resetTo, displayPath))
		ResetFileToCommit(ctx, displayPath, resetTo)
		return
	}
	target := filepath.Join(ctx.ChromiumSrc, filepath.FromSlash(displayPath))
	if _, err := os.Stat(target); err == nil {
		logx.Info(fmt.Sprintf("  Deleting (not in %.8s): %s", resetTo, displayPath))
		os.Remove(target)
	}
}

// ApplySinglePatch applies one patch (apply/common.py apply_single_patch):
// standard apply, then --3way fallback. displayPath is the patch path
// relative to the patches dir (== the chromium path).
func ApplySinglePatch(ctx *buildctx.Context, patchPath, displayPath string, dryRun bool, resetTo string) (bool, string) {
	if resetTo != "" && !dryRun {
		resetBeforeApply(ctx, displayPath, resetTo)
	}

	if dryRun {
		res := git(ctx, "apply", "--check", "-p1", patchPath)
		if res.Code == 0 {
			logx.Success("  ✓ Would apply: " + displayPath)
			return true, ""
		}
		logx.Error("  ✗ Would fail: " + displayPath)
		return false, res.Stderr
	}

	res := git(ctx, "apply", "--ignore-whitespace", "--whitespace=nowarn", "-p1", patchPath)
	if res.Code != 0 {
		res = git(ctx, "apply", "--ignore-whitespace", "--whitespace=nowarn", "-p1", "--3way", patchPath)
	}
	if res.Code == 0 {
		logx.Success("  ✓ Applied: " + displayPath)
		return true, ""
	}
	logx.Error("  ✗ Failed: " + displayPath)
	if res.Stderr != "" {
		logx.Error("    " + res.Stderr)
	}
	return false, res.Stderr
}

// PatchListEntry pairs a patch file with its display name.
type PatchListEntry struct {
	Path        string
	DisplayName string
}

// ProcessPatchList applies patches in order (apply/common.py
// process_patch_list). Interactive prompting goes through the confirmer.
func ProcessPatchList(ctx *buildctx.Context, list []PatchListEntry, dryRun bool, interactive bool, confirm Confirmer, resetTo string) (int, []string, error) {
	applied := 0
	var failed []string
	skipped := 0
	total := len(list)

	for i, entry := range list {
		if interactive && !dryRun {
			logx.Info("\n" + strings.Repeat("=", 60))
			logx.Info(fmt.Sprintf("Patch %d/%d: %s", i+1, total, entry.DisplayName))
			logx.Info(strings.Repeat("=", 60))
			choice := confirm.Choose("Choice (1-3)", []string{
				"  1) Apply this patch",
				"  2) Skip this patch",
				"  3) Stop patching",
			}, "1")
			switch choice {
			case "2":
				logx.Warning("⏭️  Skipping patch: " + entry.DisplayName)
				skipped++
				continue
			case "3":
				logx.Info(fmt.Sprintf("Stopped. Applied: %d, Failed: %d, Skipped: %d", applied, len(failed), skipped))
				return applied, failed, nil
			}
		}

		if _, err := os.Stat(entry.Path); err != nil {
			logx.Warning("  Patch not found: " + entry.DisplayName)
			failed = append(failed, entry.DisplayName)
			continue
		}

		ok, _ := ApplySinglePatch(ctx, entry.Path, entry.DisplayName, dryRun, resetTo)
		if ok {
			applied++
			continue
		}
		failed = append(failed, entry.DisplayName)

		if interactive && !dryRun {
			logx.Error("\n" + strings.Repeat("=", 60))
			logx.Error(fmt.Sprintf("Patch %s failed to apply", entry.DisplayName))
			choice := confirm.Choose("Choice (1-3)", []string{
				"  1) Continue with next patch",
				"  2) Abort",
				"  3) Fix manually and continue",
			}, "1")
			switch choice {
			case "2":
				return applied, failed, fmt.Errorf("aborted at patch: %s", entry.DisplayName)
			case "3":
				confirm.Confirm("Fix the issue manually, then press Enter to continue", true)
				applied++
				failed = failed[:len(failed)-1]
			}
		}
	}
	return applied, failed, nil
}

func patchEntries(patchesDir string) []PatchListEntry {
	var list []PatchListEntry
	for _, path := range FindPatchFiles(patchesDir) {
		rel, err := filepath.Rel(patchesDir, path)
		if err != nil {
			rel = path
		}
		list = append(list, PatchListEntry{Path: path, DisplayName: filepath.ToSlash(rel)})
	}
	return list
}

// ApplyAllPatches applies everything under chromium_patches/
// (apply/apply_all.py apply_all_patches).
func ApplyAllPatches(ctx *buildctx.Context, dryRun, interactive bool, confirm Confirmer, resetTo string) (int, []string, error) {
	patchesDir := ctx.PatchesDir()
	if _, err := os.Stat(patchesDir); err != nil {
		logx.Warning("Patches directory does not exist: " + patchesDir)
		return 0, nil, nil
	}
	list := patchEntries(patchesDir)
	if len(list) == 0 {
		logx.Warning("No patch files found")
		return 0, nil, nil
	}
	logx.Info(fmt.Sprintf("Found %d patches", len(list)))
	if dryRun {
		logx.Info("DRY RUN - No changes will be made")
	}

	applied, failed, err := ProcessPatchList(ctx, list, dryRun, interactive, confirm, resetTo)
	if err != nil {
		return applied, failed, err
	}

	logx.Info(fmt.Sprintf("\nSummary: %d applied, %d failed", applied, len(failed)))
	if len(failed) > 0 {
		logx.Error("Failed patches:")
		for _, name := range failed {
			logx.Error("  - " + name)
		}
	}
	return applied, failed, nil
}

// ApplyPatchWithReject applies one patch falling back to --reject
// (apply/apply_force.py apply_patch_with_reject). Status: applied|rejected.
func ApplyPatchWithReject(ctx *buildctx.Context, patchPath, displayPath, resetTo string) (string, []string) {
	if resetTo != "" {
		if FileExistsInCommit(ctx, displayPath, resetTo) {
			ResetFileToCommit(ctx, displayPath, resetTo)
		} else {
			target := filepath.Join(ctx.ChromiumSrc, filepath.FromSlash(displayPath))
			if _, err := os.Stat(target); err == nil {
				os.Remove(target)
			}
		}
	}

	res := git(ctx, "apply", "--ignore-whitespace", "--whitespace=nowarn", "-p1", patchPath)
	if res.Code == 0 {
		logx.Success("  Applied: " + displayPath)
		return "applied", nil
	}
	res = git(ctx, "apply", "--ignore-whitespace", "--whitespace=nowarn", "-p1", "--3way", patchPath)
	if res.Code == 0 {
		logx.Success("  Applied (3way): " + displayPath)
		return "applied", nil
	}
	res = git(ctx, "apply", "--ignore-whitespace", "--whitespace=nowarn", "-p1", "--reject", patchPath)
	if res.Code == 0 {
		logx.Success("  Applied (reject): " + displayPath)
		return "applied", nil
	}

	logx.Warning("  Conflict: " + displayPath)
	var rejFiles []string
	rejPath := filepath.Join(ctx.ChromiumSrc, filepath.FromSlash(displayPath)+".rej")
	if _, err := os.Stat(rejPath); err == nil {
		rejFiles = append(rejFiles, rejPath)
		logx.Warning("    .rej: " + rejPath)
	}
	return "rejected", rejFiles
}

// ApplyAllForce applies all patches non-interactively with --reject
// (apply/apply_force.py apply_all_force).
func ApplyAllForce(ctx *buildctx.Context, resetTo string) (int, int, []string) {
	patchesDir := ctx.PatchesDir()
	if _, err := os.Stat(patchesDir); err != nil {
		logx.Warning("Patches directory does not exist: " + patchesDir)
		return 0, 0, nil
	}
	list := patchEntries(patchesDir)
	if len(list) == 0 {
		logx.Warning("No patch files found")
		return 0, 0, nil
	}
	logx.Info(fmt.Sprintf("Found %d patches (non-interactive, --reject on conflict)", len(list)))

	applied, rejected := 0, 0
	var failed, allRej []string
	for _, entry := range list {
		status, rejFiles := ApplyPatchWithReject(ctx, entry.Path, entry.DisplayName, resetTo)
		switch status {
		case "applied":
			applied++
		case "rejected":
			rejected++
			failed = append(failed, entry.DisplayName)
			allRej = append(allRej, rejFiles...)
		default:
			failed = append(failed, entry.DisplayName)
		}
	}

	logx.Info(fmt.Sprintf("\nSummary: %d applied, %d rejected (.rej), %d total failed", applied, rejected, len(failed)))
	if len(allRej) > 0 {
		logx.Warning("Reject files:")
		for _, rej := range allRej {
			logx.Warning("  " + rej)
		}
	}
	return applied, rejected, failed
}

// FeatureFiles loads the file list for a feature from features.yaml.
func FeatureFiles(ctx *buildctx.Context, featureName string) ([]string, error) {
	var doc struct {
		Features map[string]struct {
			Files []string `yaml:"files"`
		} `yaml:"features"`
	}
	if err := config.LoadInto(ctx.FeaturesYAMLPath(), &doc); err != nil {
		return nil, fmt.Errorf("no features.yaml found: %w", err)
	}
	feature, ok := doc.Features[featureName]
	if !ok {
		var names []string
		for name := range doc.Features {
			names = append(names, name)
		}
		return nil, fmt.Errorf("feature '%s' not found (available: %s)", featureName, strings.Join(names, ", "))
	}
	return feature.Files, nil
}

// ApplyFeaturePatches applies a feature's patches
// (apply/apply_feature.py apply_feature_patches).
func ApplyFeaturePatches(ctx *buildctx.Context, featureName string, dryRun bool, resetTo string) (int, []string, error) {
	files, err := FeatureFiles(ctx, featureName)
	if err != nil {
		return 0, nil, err
	}
	if len(files) == 0 {
		logx.Warning(fmt.Sprintf("Feature '%s' has no files", featureName))
		return 0, nil, nil
	}
	logx.Info(fmt.Sprintf("Applying patches for feature '%s' (%d files)", featureName, len(files)))
	if dryRun {
		logx.Info("DRY RUN - No changes will be made")
	}

	var list []PatchListEntry
	for _, filePath := range files {
		list = append(list, PatchListEntry{Path: ctx.PatchPathForFile(filePath), DisplayName: filePath})
	}
	applied, failed, err := ProcessPatchList(ctx, list, dryRun, false, AutoConfirmer{}, resetTo)
	if err != nil {
		return applied, failed, err
	}
	logx.Info(fmt.Sprintf("\nSummary: %d applied, %d failed", applied, len(failed)))
	if len(failed) > 0 {
		logx.Error("Failed patches:")
		for _, name := range failed {
			logx.Error("  - " + name)
		}
	}
	return applied, failed, nil
}

// ApplySingleFilePatch applies the patch for one chromium path
// (apply/apply_patch.py apply_single_file_patch).
func ApplySingleFilePatch(ctx *buildctx.Context, chromiumPath string, resetTo string, dryRun bool) (bool, string) {
	patchPath := ctx.PatchPathForFile(chromiumPath)
	if _, err := os.Stat(patchPath); err != nil {
		return false, "No patch found for: " + chromiumPath
	}
	logx.Info("Applying patch for: " + chromiumPath)
	if dryRun {
		logx.Info("DRY RUN - No changes will be made")
	}
	return ApplySinglePatch(ctx, patchPath, chromiumPath, dryRun, resetTo)
}
