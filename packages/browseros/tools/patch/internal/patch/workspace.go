package patch

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/git"
)

// WorkingTreeOptions bound a working-tree patch-set build: the base commit to
// diff against, include filters, an ignore set for untracked junk, and an
// optional progress callback.
type WorkingTreeOptions struct {
	Base    string
	Filters []string
	Ignore  *IgnoreSet
	Report  func(message string)
}

func BuildWorkingTreePatchSet(ctx context.Context, workspacePath string, opts WorkingTreeOptions) (PatchSet, error) {
	diff, err := git.DiffText(ctx, workspacePath, opts.Base)
	if err != nil {
		return nil, err
	}
	set, err := ParseDiffOutput(diff)
	if err != nil {
		return nil, err
	}
	untracked, err := git.ListUntracked(ctx, workspacePath, opts.Filters)
	if err != nil {
		return nil, err
	}
	kept := make([]string, 0, len(untracked))
	for _, rel := range untracked {
		if opts.Ignore.Match(rel) {
			continue
		}
		kept = append(kept, rel)
	}
	for idx, rel := range kept {
		if opts.Report != nil {
			opts.Report(fmt.Sprintf("Scanning untracked %d/%d %s", idx+1, len(kept), rel))
		}
		diffText, err := git.DiffNoIndex(ctx, workspacePath, rel)
		if err != nil {
			return nil, err
		}
		untrackedSet, err := ParseDiffOutput(diffText)
		if err != nil {
			return nil, err
		}
		for patchPath, patchFile := range untrackedSet {
			set[patchPath] = patchFile
		}
	}
	return filterSet(set, opts.Filters), nil
}

func BuildCommitPatchSet(ctx context.Context, workspacePath string, ref string, base string, filters []string) (PatchSet, error) {
	if base == "" {
		diff, err := git.DiffText(ctx, workspacePath, ref+"^.."+ref)
		if err != nil {
			return nil, err
		}
		set, err := ParseDiffOutput(diff)
		if err != nil {
			return nil, err
		}
		return filterSet(set, filters), nil
	}
	changes, err := git.DiffTreeNameStatus(ctx, workspacePath, ref, filters)
	if err != nil {
		return nil, err
	}
	return buildBaseScopedSet(ctx, workspacePath, ref, base, changes)
}

func BuildRangePatchSet(ctx context.Context, workspacePath string, start string, end string, base string, squash bool, filters []string) (PatchSet, error) {
	if squash {
		if base == "" {
			diff, err := git.DiffText(ctx, workspacePath, start+".."+end)
			if err != nil {
				return nil, err
			}
			set, err := ParseDiffOutput(diff)
			if err != nil {
				return nil, err
			}
			return filterSet(set, filters), nil
		}
		changes, err := git.DiffNameStatusBetween(ctx, workspacePath, start, end, filters)
		if err != nil {
			return nil, err
		}
		return buildBaseScopedSet(ctx, workspacePath, end, base, changes)
	}

	commits, err := git.RevListRange(ctx, workspacePath, start, end)
	if err != nil {
		return nil, err
	}
	set := PatchSet{}
	seen := map[string]bool{}
	for _, commit := range commits {
		var current PatchSet
		if base == "" {
			diff, err := git.DiffText(ctx, workspacePath, commit+"^.."+commit)
			if err != nil {
				return nil, err
			}
			current, err = ParseDiffOutput(diff)
			if err != nil {
				return nil, err
			}
		} else {
			changes, err := git.DiffTreeNameStatus(ctx, workspacePath, commit, filters)
			if err != nil {
				return nil, err
			}
			current, err = buildBaseScopedSet(ctx, workspacePath, commit, base, changes)
			if err != nil {
				return nil, err
			}
		}
		for rel, patchFile := range filterSet(current, filters) {
			if base != "" {
				set[rel] = patchFile
				continue
			}
			if seen[rel] {
				continue
			}
			set[rel] = patchFile
			seen[rel] = true
		}
	}
	return set, nil
}

func buildBaseScopedSet(ctx context.Context, workspacePath string, ref string, base string, changes []git.FileChange) (PatchSet, error) {
	set := PatchSet{}
	for _, change := range changes {
		rel := NormalizeChromiumPath(change.Path)
		diff, err := git.DiffText(ctx, workspacePath, base, ref, "--", rel)
		if err != nil {
			return nil, err
		}
		switch {
		case strings.TrimSpace(diff) != "":
			patches, err := ParseDiffOutput(diff)
			if err != nil {
				return nil, err
			}
			for patchPath, patchFile := range patches {
				set[patchPath] = patchFile
			}
		case change.Status == "D":
			exists, err := git.FileExistsAtCommit(ctx, workspacePath, base, rel)
			if err != nil {
				return nil, err
			}
			if exists {
				set[rel] = FilePatch{Path: rel, Op: OpDelete}
			}
		case change.Status == "A":
			content, err := git.ShowFile(ctx, workspacePath, ref, rel)
			if err != nil {
				return nil, err
			}
			mode, err := git.FileModeAtCommit(ctx, workspacePath, ref, rel)
			if err != nil {
				return nil, err
			}
			set[rel] = syntheticAddPatch(rel, content, mode)
		}
	}
	return set, nil
}

func filterSet(set PatchSet, filters []string) PatchSet {
	filtered := PatchSet{}
	for rel, patchFile := range set {
		if !PathMatches(rel, filters) {
			continue
		}
		filtered[rel] = patchFile
	}
	return filtered
}

func ScopeFromSet(set PatchSet) []string {
	paths := make([]string, 0, len(set))
	for rel := range set {
		paths = append(paths, rel)
	}
	slices.Sort(paths)
	return paths
}

func RejectPath(workspacePath string, rel string) string {
	return filepath.Join(workspacePath, filepath.FromSlash(rel+".rej"))
}

// syntheticAddPatch builds the add-style diff for content git never saw as a
// working-tree file. The bytes must match `git diff --no-index --full-index`
// exactly so the same logical patch is identical no matter which extraction
// path produced it.
func syntheticAddPatch(rel string, content []byte, mode string) FilePatch {
	if git.LooksBinary(content) {
		return FilePatch{Path: rel, Op: OpBinary, IsBinary: true}
	}
	if mode == "" {
		mode = "100644"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", rel, rel)
	fmt.Fprintf(&b, "new file mode %s\n", mode)
	fmt.Fprintf(&b, "index %s..%s\n", strings.Repeat("0", 40), blobSHA1(content))
	if len(content) > 0 {
		body := string(content)
		missingEOFNewline := !strings.HasSuffix(body, "\n")
		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n", rel)
		if len(lines) == 1 {
			b.WriteString("@@ -0,0 +1 @@\n")
		} else {
			fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
		}
		for _, line := range lines {
			b.WriteString("+")
			b.WriteString(line)
			b.WriteString("\n")
		}
		if missingEOFNewline {
			b.WriteString("\\ No newline at end of file\n")
		}
	}
	return FilePatch{Path: rel, Op: OpAdd, Content: []byte(b.String())}
}

// blobSHA1 hashes content the way git names blobs (SHA-1 object format;
// SHA-256 repos are out of scope — Chromium uses SHA-1).
func blobSHA1(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
