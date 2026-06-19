package patchwork

import (
	"fmt"
	"os"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// ResolveBaseCommit returns an explicit base or the package BASE_COMMIT
// (extract/common.py resolve_base_commit).
func ResolveBaseCommit(ctx *buildctx.Context, base string) (string, error) {
	if base != "" {
		return base, nil
	}
	content, err := os.ReadFile(ctx.BaseCommitFile())
	if err != nil {
		return "", fmt.Errorf("BASE_COMMIT not found: %s", ctx.BaseCommitFile())
	}
	resolved := strings.TrimSpace(string(content))
	if resolved == "" {
		return "", fmt.Errorf("BASE_COMMIT is empty: %s", ctx.BaseCommitFile())
	}
	return resolved, nil
}

// CommitExists verifies a commit-ish resolves in chromium_src.
func CommitExists(ctx *buildctx.Context, ref string) bool {
	return git(ctx, "rev-parse", "--verify", ref+"^{commit}").Code == 0
}

// ChangedFilesWithStatus maps path → status (A/M/D/R/C) for one commit
// (extract/utils.py get_commit_changed_files_with_status).
func ChangedFilesWithStatus(ctx *buildctx.Context, commit string) (map[string]string, []string) {
	res := git(ctx, "diff-tree", "--no-commit-id", "--name-status", "-r", commit)
	if res.Code != 0 {
		return map[string]string{}, nil
	}
	return parseNameStatus(res.Stdout)
}

// RangeChangedFilesWithStatus maps path → status for base..head
// (extract_range.py get_range_changed_files_with_status).
func RangeChangedFilesWithStatus(ctx *buildctx.Context, base, head string) (map[string]string, []string) {
	res := git(ctx, "diff", "--name-status", base+".."+head)
	if res.Code != 0 {
		return map[string]string{}, nil
	}
	return parseNameStatus(res.Stdout)
}

func parseNameStatus(out string) (map[string]string, []string) {
	files := map[string]string{}
	var order []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		status := string(parts[0][0])
		path := parts[len(parts)-1]
		if _, seen := files[path]; !seen {
			order = append(order, path)
		}
		files[path] = status
	}
	return files, order
}

// ExtractOptions configures extraction runs.
type ExtractOptions struct {
	Verbose       bool
	Force         bool
	IncludeBinary bool
	Base          string // empty → BASE_COMMIT
	Confirm       Confirmer
}

func (o ExtractOptions) confirmer() Confirmer {
	if o.Confirm != nil {
		return o.Confirm
	}
	return StdinConfirmer{}
}

// checkOverwrite prompts when patches already exist (extract/common.py).
func checkOverwrite(ctx *buildctx.Context, patches []*FilePatch, opts ExtractOptions) bool {
	var existing []string
	for _, patch := range patches {
		if _, err := os.Stat(ctx.PatchPathForFile(patch.Path)); err == nil {
			existing = append(existing, patch.Path)
		}
	}
	if len(existing) == 0 {
		return true
	}
	logx.Warning(fmt.Sprintf("Found %d existing patches", len(existing)))
	if opts.Verbose {
		for i, path := range existing {
			if i >= 5 {
				logx.Warning(fmt.Sprintf("  ... and %d more", len(existing)-5))
				break
			}
			logx.Warning("  - " + path)
		}
	}
	if !opts.confirmer().Confirm("Overwrite existing patches?", false) {
		logx.Info("Extraction cancelled")
		return false
	}
	return true
}

// WritePatches persists extracted patches and markers (extract/common.py
// write_patches). Returns (successCount, extractedFiles).
func WritePatches(ctx *buildctx.Context, patches []*FilePatch, opts ExtractOptions) (int, []string) {
	success, fail, skip := 0, 0, 0
	var extracted []string

	for _, patch := range patches {
		if opts.Verbose {
			logx.Info(fmt.Sprintf("Processing (%s): %s", patch.Operation, patch.Path))
		}
		switch {
		case patch.Operation == OpDelete:
			created, skipped, err := CreateDeletionMarker(ctx, patch.Path, opts.confirmer())
			switch {
			case err != nil:
				fail++
			case skipped:
				skip++
			case created:
				success++
				extracted = append(extracted, patch.Path)
			}

		case patch.IsBinary:
			if opts.IncludeBinary {
				if err := CreateBinaryMarker(ctx, patch.Path, patch.Operation); err != nil {
					fail++
				} else {
					success++
					extracted = append(extracted, patch.Path)
				}
			} else {
				logx.Warning("  Skipping binary file: " + patch.Path)
				skip++
			}

		case patch.Operation == OpRename && patch.Content == "":
			if err := CreateRenameMarker(ctx, patch.Path, patch.OldPath, patch.Similarity); err != nil {
				fail++
			} else {
				success++
				extracted = append(extracted, patch.Path)
			}

		default:
			if patch.Content == "" {
				logx.Warning("  No patch content for: " + patch.Path)
				skip++
				continue
			}
			if err := WritePatchFile(ctx, patch.Path, patch.Content); err != nil {
				fail++
			} else {
				success++
				extracted = append(extracted, patch.Path)
			}
		}
	}

	logExtractionSummary(patches)
	if fail > 0 {
		logx.Warning(fmt.Sprintf("Failed to extract %d patches", fail))
	}
	if skip > 0 {
		logx.Info(fmt.Sprintf("Skipped %d files", skip))
	}
	return success, extracted
}

func logExtractionSummary(patches []*FilePatch) {
	counts := map[FileOperation]int{}
	binary := 0
	for _, patch := range patches {
		counts[patch.Operation]++
		if patch.IsBinary {
			binary++
		}
	}
	logx.Info("\nExtraction Summary")
	logx.Info(strings.Repeat("=", 60))
	logx.Info(fmt.Sprintf("Total files:     %d", len(patches)))
	logx.Info(strings.Repeat("-", 40))
	if counts[OpAdd] > 0 {
		logx.Info(fmt.Sprintf("New files:       %d", counts[OpAdd]))
	}
	if counts[OpModify] > 0 {
		logx.Info(fmt.Sprintf("Modified:        %d", counts[OpModify]))
	}
	if counts[OpDelete] > 0 {
		logx.Info(fmt.Sprintf("Deleted:         %d", counts[OpDelete]))
	}
	if counts[OpRename] > 0 {
		logx.Info(fmt.Sprintf("Renamed:         %d", counts[OpRename]))
	}
	if counts[OpCopy] > 0 {
		logx.Info(fmt.Sprintf("Copied:          %d", counts[OpCopy]))
	}
	if binary > 0 {
		logx.Info(fmt.Sprintf("Binary files:    %d", binary))
	}
	logx.Info(strings.Repeat("=", 60))
}

// extractWithBase extracts full diffs from base for every file touched by
// commit (extract/common.py extract_with_base).
func extractWithBase(ctx *buildctx.Context, commit, base string, opts ExtractOptions) (int, []string, error) {
	changed, order := ChangedFilesWithStatus(ctx, commit)
	if len(changed) == 0 {
		logx.Warning(fmt.Sprintf("No files changed in commit %s", commit))
		return 0, nil, nil
	}
	if opts.Verbose {
		logx.Info(fmt.Sprintf("Files changed in %s: %d", commit, len(changed)))
	}

	var patches []*FilePatch
	for _, filePath := range order {
		status := changed[filePath]
		if opts.Verbose {
			logx.Info(fmt.Sprintf("  Processing (%s): %s", status, filePath))
		}
		if status == "D" {
			patches = append(patches, &FilePatch{Path: filePath, Operation: OpDelete})
			continue
		}

		diffArgs := []string{"diff", base + ".." + commit, "--", filePath}
		if opts.IncludeBinary {
			diffArgs = append(diffArgs, "--binary")
		}
		res := git(ctx, diffArgs...)
		if res.Code != 0 {
			logx.Warning("Failed to get diff for " + filePath)
			continue
		}
		if strings.TrimSpace(res.Stdout) != "" {
			parsed, _ := ParseDiffOutput(res.Stdout)
			patches = append(patches, parsed...)
		} else if status == "A" {
			show := git(ctx, "show", commit+":"+filePath)
			if show.Code == 0 && show.Stdout != "" {
				patches = append(patches, &FilePatch{Path: filePath, Operation: OpAdd})
				logx.Warning("  Added file needs manual handling: " + filePath)
			}
		}
	}

	if len(patches) == 0 {
		logx.Warning("No patches to extract")
		return 0, nil, nil
	}
	logx.Info(fmt.Sprintf("Extracting %d patches with base %s", len(patches), base))

	if !opts.Force && !checkOverwrite(ctx, patches, opts) {
		return 0, nil, nil
	}
	count, extracted := WritePatches(ctx, patches, opts)
	return count, extracted, nil
}

// ExtractCommit extracts patches for one commit
// (extract/extract_commit.py extract_single_commit).
func ExtractCommit(ctx *buildctx.Context, commit string, opts ExtractOptions) (int, []string, error) {
	if !CommitExists(ctx, commit) {
		return 0, nil, fmt.Errorf("commit not found: %s", commit)
	}
	base, err := ResolveBaseCommit(ctx, opts.Base)
	if err != nil {
		return 0, nil, err
	}
	return extractWithBase(ctx, commit, base, opts)
}

// ExtractRange extracts the cumulative diff start..end
// (extract/extract_range.py extract_commit_range).
func ExtractRange(ctx *buildctx.Context, start, end string, opts ExtractOptions) (int, []string, error) {
	if !CommitExists(ctx, start) {
		return 0, nil, fmt.Errorf("base commit not found: %s", start)
	}
	if !CommitExists(ctx, end) {
		return 0, nil, fmt.Errorf("head commit not found: %s", end)
	}
	diffBase, err := ResolveBaseCommit(ctx, opts.Base)
	if err != nil {
		return 0, nil, err
	}
	if !CommitExists(ctx, diffBase) {
		label := "BASE_COMMIT"
		if opts.Base != "" {
			label = "Custom base"
		}
		return 0, nil, fmt.Errorf("%s commit not found: %s", label, diffBase)
	}

	countRes := git(ctx, "rev-list", "--count", start+".."+end)
	commitCount := strings.TrimSpace(countRes.Stdout)
	if countRes.Code != 0 || commitCount == "0" || commitCount == "" {
		logx.Warning(fmt.Sprintf("No commits between %s and %s", start, end))
		return 0, nil, nil
	}
	logx.Info(fmt.Sprintf("Processing %s commits", commitCount))

	changed, order := RangeChangedFilesWithStatus(ctx, start, end)
	if len(changed) == 0 {
		logx.Warning("No files changed in range")
		return 0, nil, nil
	}
	logx.Info(fmt.Sprintf("Found %d files changed in range", len(changed)))

	var patches []*FilePatch
	for _, filePath := range order {
		status := changed[filePath]
		if status == "D" {
			patches = append(patches, &FilePatch{Path: filePath, Operation: OpDelete})
			continue
		}
		diffArgs := []string{"diff", diffBase + ".." + end, "--", filePath}
		if opts.IncludeBinary {
			diffArgs = append(diffArgs, "--binary")
		}
		res := git(ctx, diffArgs...)
		if res.Code != 0 || strings.TrimSpace(res.Stdout) == "" {
			continue
		}
		parsed, _ := ParseDiffOutput(res.Stdout)
		patches = append(patches, parsed...)
	}

	if len(patches) == 0 {
		logx.Warning("No patches to extract")
		return 0, nil, nil
	}
	logx.Info(fmt.Sprintf("Extracting %d patches with base %s", len(patches), diffBase))
	if !opts.Force && !checkOverwrite(ctx, patches, opts) {
		return 0, nil, nil
	}
	count, extracted := WritePatches(ctx, patches, opts)
	return count, extracted, nil
}

// ExtractFilePatch extracts the working-tree diff for one chromium path
// (extract/extract_patch.py extract_single_file_patch).
func ExtractFilePatch(ctx *buildctx.Context, chromiumPath string, opts ExtractOptions) (bool, error) {
	base, err := ResolveBaseCommit(ctx, opts.Base)
	if err != nil {
		return false, err
	}
	if !CommitExists(ctx, base) {
		return false, fmt.Errorf("base commit not found: %s", base)
	}

	logx.Info("Extracting patch for: " + chromiumPath)
	logx.Info(fmt.Sprintf("  Base: %.12s", base))

	res := git(ctx, "diff", base, "--", chromiumPath)
	if res.Code != 0 {
		return false, fmt.Errorf("failed to get diff: %s", res.Stderr)
	}

	if strings.TrimSpace(res.Stdout) == "" {
		baseExists := git(ctx, "cat-file", "-e", base+":"+chromiumPath).Code == 0
		workingExists := false
		if _, err := os.Stat(ctx.ChromiumSrc + "/" + chromiumPath); err == nil {
			workingExists = true
		}
		switch {
		case !baseExists && !workingExists:
			return false, fmt.Errorf("file does not exist in base or working directory: %s", chromiumPath)
		case baseExists && workingExists:
			return false, fmt.Errorf("no changes found for: %s", chromiumPath)
		default: // new file → full content diff
			res = git(ctx, "diff", "--no-index", "/dev/null", chromiumPath)
			if strings.TrimSpace(res.Stdout) == "" {
				return false, fmt.Errorf("failed to generate diff for new file: %s", chromiumPath)
			}
		}
	}

	parsed, byPath := ParseDiffOutput(res.Stdout)
	if len(parsed) == 0 {
		return false, fmt.Errorf("failed to parse diff for: %s", chromiumPath)
	}
	patch, ok := byPath[chromiumPath]
	if !ok {
		if len(parsed) == 1 {
			patch = parsed[0]
		} else {
			return false, fmt.Errorf("unexpected diff output for: %s", chromiumPath)
		}
	}

	patchPath := ctx.PatchPathForFile(chromiumPath)
	if _, err := os.Stat(patchPath); err == nil && !opts.Force {
		if !opts.confirmer().Confirm(fmt.Sprintf("Patch already exists: %s. Overwrite?", chromiumPath), false) {
			logx.Info("Extraction cancelled")
			return false, fmt.Errorf("cancelled by user")
		}
	}

	switch {
	case patch.Operation == OpDelete:
		created, _, err := CreateDeletionMarker(ctx, chromiumPath, opts.confirmer())
		if err != nil || !created {
			return false, fmt.Errorf("failed to create deletion marker for: %s", chromiumPath)
		}
		return true, nil
	case patch.IsBinary:
		return false, fmt.Errorf("binary files not supported: %s", chromiumPath)
	case patch.Content == "":
		return false, fmt.Errorf("no patch content for: %s", chromiumPath)
	}

	if err := WritePatchFile(ctx, chromiumPath, patch.Content); err != nil {
		return false, fmt.Errorf("failed to write patch for: %s", chromiumPath)
	}
	return true, nil
}
