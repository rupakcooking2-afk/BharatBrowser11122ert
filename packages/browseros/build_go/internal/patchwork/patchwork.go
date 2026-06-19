// Package patchwork ports the patch engine shared by the dev CLI and the
// build pipeline: build/modules/apply/* and build/modules/extract/*.
package patchwork

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// FileOperation classifies a file's change in a diff (apply/utils.py).
type FileOperation string

const (
	OpAdd    FileOperation = "add"
	OpModify FileOperation = "modify"
	OpDelete FileOperation = "delete"
	OpRename FileOperation = "rename"
	OpCopy   FileOperation = "copy"
	OpBinary FileOperation = "binary"
)

// FilePatch is a single file's patch information.
type FilePatch struct {
	Path       string
	Operation  FileOperation
	OldPath    string
	Content    string
	IsBinary   bool
	Similarity int
}

// Confirmer answers interactive prompts; tests and non-interactive flows
// inject canned answers.
type Confirmer interface {
	// Confirm asks a yes/no question.
	Confirm(prompt string, def bool) bool
	// Choose asks for one of the numbered options, returning the choice string.
	Choose(prompt string, options []string, def string) string
}

// StdinConfirmer prompts on the terminal (click.confirm/prompt equivalents).
type StdinConfirmer struct{}

func (StdinConfirmer) Confirm(prompt string, def bool) bool {
	defStr := "y/N"
	if def {
		defStr = "Y/n"
	}
	fmt.Fprintf(logx.Out, "%s [%s]: ", prompt, defStr)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return def
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "" {
		return def
	}
	return answer == "y" || answer == "yes"
}

func (StdinConfirmer) Choose(prompt string, options []string, def string) string {
	for _, opt := range options {
		fmt.Fprintln(logx.Out, opt)
	}
	fmt.Fprintf(logx.Out, "%s [%s]: ", prompt, def)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return def
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		return def
	}
	return answer
}

// AutoConfirmer answers every prompt with fixed values (non-interactive).
type AutoConfirmer struct {
	Answer bool
	Choice string
}

func (a AutoConfirmer) Confirm(string, bool) bool { return a.Answer }
func (a AutoConfirmer) Choose(_ string, _ []string, def string) string {
	if a.Choice != "" {
		return a.Choice
	}
	return def
}

var (
	diffGitRe    = regexp.MustCompile(`^diff --git a/(.*) b/(.*)$`)
	similarityRe = regexp.MustCompile(`^similarity index (\d+)%`)
)

// ParseDiffOutput parses `git diff` output into per-file patches, preserving
// encounter order (apply/utils.py parse_diff_output).
func ParseDiffOutput(diffOutput string) ([]*FilePatch, map[string]*FilePatch) {
	var order []*FilePatch
	byPath := map[string]*FilePatch{}

	var current *FilePatch
	var lines []string

	flush := func() {
		if current == nil || len(lines) == 0 {
			return
		}
		if !current.IsBinary {
			current.Content = strings.Join(lines, "\n")
		}
		if existing, ok := byPath[current.Path]; ok {
			*existing = *current
		} else {
			byPath[current.Path] = current
			order = append(order, current)
		}
	}

	for _, line := range strings.Split(diffOutput, "\n") {
		if match := diffGitRe.FindStringSubmatch(line); match != nil {
			flush()
			current = &FilePatch{Path: match[2], Operation: OpModify}
			lines = []string{line}
			continue
		}
		if current == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "deleted file"):
			current.Operation = OpDelete
		case strings.HasPrefix(line, "new file"):
			current.Operation = OpAdd
		case strings.HasPrefix(line, "similarity index"):
			if m := similarityRe.FindStringSubmatch(line); m != nil {
				current.Similarity, _ = strconv.Atoi(m[1])
			}
		case strings.HasPrefix(line, "rename from"):
			current.Operation = OpRename
			current.OldPath = strings.TrimSpace(strings.TrimPrefix(line, "rename from"))
		case strings.HasPrefix(line, "copy from"):
			current.Operation = OpCopy
			current.OldPath = strings.TrimSpace(strings.TrimPrefix(line, "copy from"))
		case strings.HasPrefix(line, "Binary files"):
			current.IsBinary = true
			if current.Operation == OpModify {
				current.Operation = OpBinary
			}
		}
		lines = append(lines, line)
	}
	flush()
	return order, byPath
}

// FindPatchFiles lists valid patch files under patchesDir, sorted
// (apply/common.py find_patch_files): markers and dotfiles excluded.
func FindPatchFiles(patchesDir string) []string {
	var patches []string
	filepath.WalkDir(patchesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".deleted") ||
			strings.HasSuffix(name, ".binary") ||
			strings.HasSuffix(name, ".rename") ||
			strings.HasPrefix(name, ".") {
			return nil
		}
		patches = append(patches, path)
		return nil
	})
	sort.Strings(patches)
	return patches
}

// WritePatchFile writes one patch under <root>/chromium_patches/<file_path>
// (extract/utils.py write_patch_file). Ensures trailing newline.
func WritePatchFile(ctx *buildctx.Context, filePath, content string) error {
	outputPath := ctx.PatchPathForFile(filePath)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		logx.Error(fmt.Sprintf("  Failed to write %s: %v", outputPath, err))
		return err
	}
	rel, _ := filepath.Rel(ctx.RootDir, outputPath)
	logx.Success("  Written: " + rel)
	return nil
}

// CreateDeletionMarker writes <path>.deleted. When existing patch files
// conflict, the confirmer decides (extract/utils.py create_deletion_marker):
// choice 1 = remove patch + marker, 2 = remove patch only, 3 = skip.
// Returns (created, skipped, err).
func CreateDeletionMarker(ctx *buildctx.Context, filePath string, confirm Confirmer) (bool, bool, error) {
	basePath := ctx.PatchPathForFile(filePath)

	var existing []string
	if _, err := os.Stat(basePath); err == nil {
		existing = append(existing, basePath)
	}
	for _, suffix := range []string{".patch", ".binary", ".rename"} {
		candidate := basePath + suffix
		if _, err := os.Stat(candidate); err == nil {
			existing = append(existing, candidate)
		}
	}

	if len(existing) > 0 {
		logx.Warning(fmt.Sprintf("File '%s' is being deleted, but existing patch(es) found:", filePath))
		for _, path := range existing {
			rel, _ := filepath.Rel(ctx.RootDir, path)
			logx.Warning("  - " + rel)
		}
		choice := confirm.Choose("Choice", []string{
			"  1) Remove patch and create .deleted marker (file exists in upstream)",
			"  2) Remove patch only (file was added by your patches, not in upstream)",
			"  3) Skip (keep existing patch, don't record deletion)",
		}, "1")
		if choice == "3" {
			logx.Warning("  Skipped: " + filePath)
			return false, true, nil
		}
		for _, path := range existing {
			if err := os.Remove(path); err != nil {
				return false, false, fmt.Errorf("failed to remove %s: %w", path, err)
			}
			rel, _ := filepath.Rel(ctx.RootDir, path)
			logx.Warning("  Removed: " + rel)
		}
		if choice == "2" {
			logx.Success(fmt.Sprintf("  Removed patch for: %s (no .deleted marker)", filePath))
			return true, false, nil
		}
	}

	markerPath := basePath + ".deleted"
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		return false, false, err
	}
	content := fmt.Sprintf("File deleted in patch\nOriginal path: %s\n", filePath)
	if err := os.WriteFile(markerPath, []byte(content), 0o644); err != nil {
		return false, false, err
	}
	rel, _ := filepath.Rel(ctx.RootDir, markerPath)
	logx.Warning("  Marked deleted: " + rel)
	return true, false, nil
}

// CreateBinaryMarker writes <path>.binary (extract/utils.py).
func CreateBinaryMarker(ctx *buildctx.Context, filePath string, op FileOperation) error {
	markerPath := ctx.PatchPathForFile(filePath) + ".binary"
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("Binary file\nOperation: %s\nOriginal path: %s\n", op, filePath)
	if err := os.WriteFile(markerPath, []byte(content), 0o644); err != nil {
		return err
	}
	rel, _ := filepath.Rel(ctx.RootDir, markerPath)
	logx.Warning("  Binary file marked: " + rel)
	return nil
}

// CreateRenameMarker writes <path>.rename for pure renames
// (extract/common.py write_patches rename branch).
func CreateRenameMarker(ctx *buildctx.Context, filePath, oldPath string, similarity int) error {
	markerPath := ctx.PatchPathForFile(filePath) + ".rename"
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("Renamed from: %s\nSimilarity: %d%%\n", oldPath, similarity)
	if err := os.WriteFile(markerPath, []byte(content), 0o644); err != nil {
		return err
	}
	logx.Info("  Rename marked: " + filePath)
	return nil
}
