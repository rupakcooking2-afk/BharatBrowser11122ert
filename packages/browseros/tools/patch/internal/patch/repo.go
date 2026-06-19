package patch

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func LoadRepoPatchSet(patchesDir string, filters []string) (PatchSet, error) {
	set := PatchSet{}
	err := filepath.WalkDir(patchesDir, func(fullPath string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(patchesDir, fullPath)
		if err != nil {
			return err
		}
		relPath = NormalizeChromiumPath(relPath)
		patchFile, err := loadPatchFile(fullPath, relPath)
		if err != nil {
			return err
		}
		if !PathMatches(patchFile.Path, filters) {
			return nil
		}
		set[patchFile.Path] = patchFile
		return nil
	})
	return set, err
}

// WritePlan classifies what a WriteRepoPatchSet call would do. Unchanged
// patches (same signature, same marker kind) are never rewritten so volatile
// diff metadata can't churn the patch repo.
type WritePlan struct {
	Creates   []string `json:"creates"`
	Updates   []string `json:"updates"`
	Unchanged []string `json:"unchanged"`
	Deletes   []string `json:"deletes"`
}

func PlanRepoPatchSet(patchesDir string, set PatchSet, scope []string) (*WritePlan, error) {
	existing, err := LoadRepoPatchSet(patchesDir, nil)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return planRepoPatchSet(patchesDir, existing, set, scope), nil
}

func planRepoPatchSet(patchesDir string, existing PatchSet, set PatchSet, scope []string) *WritePlan {
	// Scope entries follow PathMatches semantics (exact file or directory
	// prefix) so "extract ch1 -- chrome/dir" scopes the whole directory.
	inScope := map[string]bool{}
	for rel := range existing {
		if PathMatches(rel, scope) {
			inScope[rel] = true
		}
	}
	for rel := range set {
		if PathMatches(rel, scope) {
			inScope[rel] = true
		}
	}

	plan := &WritePlan{}
	for rel := range existing {
		if !inScope[rel] || set[rel].Path != "" {
			continue
		}
		plan.Deletes = append(plan.Deletes, rel)
	}
	for rel, patchFile := range set {
		if !inScope[rel] {
			continue
		}
		current, ok := existing[rel]
		switch {
		case !ok:
			plan.Creates = append(plan.Creates, rel)
		case sameWriteTarget(patchesDir, current, patchFile) && signature(current) == signature(patchFile):
			plan.Unchanged = append(plan.Unchanged, rel)
		default:
			plan.Updates = append(plan.Updates, rel)
		}
	}
	slices.Sort(plan.Creates)
	slices.Sort(plan.Updates)
	slices.Sort(plan.Unchanged)
	slices.Sort(plan.Deletes)
	return plan
}

func sameWriteTarget(patchesDir string, a FilePatch, b FilePatch) bool {
	targetA, _ := patchWriteTarget(patchesDir, a)
	targetB, _ := patchWriteTarget(patchesDir, b)
	return targetA == targetB
}

// Written returns every path the plan actually rewrites.
func (p *WritePlan) Written() []string {
	written := append(append([]string{}, p.Creates...), p.Updates...)
	slices.Sort(written)
	return written
}

func WriteRepoPatchSet(patchesDir string, set PatchSet, scope []string) (*WritePlan, error) {
	existing, err := LoadRepoPatchSet(patchesDir, nil)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	plan := planRepoPatchSet(patchesDir, existing, set, scope)
	for _, rel := range plan.Deletes {
		if err := removePatchVariants(patchesDir, rel); err != nil {
			return nil, err
		}
	}
	for _, rel := range plan.Written() {
		if err := removePatchVariants(patchesDir, rel); err != nil {
			return nil, err
		}
		target, body := patchWriteTarget(patchesDir, set[rel])
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if len(body) == 0 || body[len(body)-1] != '\n' {
			body = append(body, '\n')
		}
		if err := os.WriteFile(target, body, 0o644); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func loadPatchFile(fullPath string, relPath string) (FilePatch, error) {
	switch {
	case strings.HasSuffix(relPath, ".deleted"):
		return FilePatch{Path: strings.TrimSuffix(relPath, ".deleted"), Op: OpDelete}, nil
	case strings.HasSuffix(relPath, ".binary"):
		return FilePatch{Path: strings.TrimSuffix(relPath, ".binary"), Op: OpBinary, IsBinary: true}, nil
	case strings.HasSuffix(relPath, ".rename"):
		body, err := os.ReadFile(fullPath)
		if err != nil {
			return FilePatch{}, err
		}
		return parseRenameMarker(strings.TrimSuffix(relPath, ".rename"), string(body))
	default:
		body, err := os.ReadFile(fullPath)
		if err != nil {
			return FilePatch{}, err
		}
		set, err := ParseDiffOutput(string(body))
		if err != nil {
			return FilePatch{}, fmt.Errorf("parse %s: %w", relPath, err)
		}
		for _, patchFile := range set {
			return patchFile, nil
		}
		return FilePatch{}, fmt.Errorf("empty patch file: %s", relPath)
	}
}

func patchWriteTarget(patchesDir string, patchFile FilePatch) (string, []byte) {
	switch {
	case patchFile.Op == OpDelete:
		return filepath.Join(patchesDir, filepath.FromSlash(patchFile.Path+".deleted")), []byte("File deleted in patch")
	case patchFile.IsPureRename():
		body := []byte(fmt.Sprintf("Renamed from: %s\nSimilarity: %d%%", patchFile.OldPath, patchFile.Similarity))
		return filepath.Join(patchesDir, filepath.FromSlash(patchFile.Path+".rename")), body
	case patchFile.Op == OpBinary && len(patchFile.Content) == 0:
		return filepath.Join(patchesDir, filepath.FromSlash(patchFile.Path+".binary")), []byte("Binary file")
	default:
		return filepath.Join(patchesDir, filepath.FromSlash(patchFile.Path)), patchFile.Content
	}
}

func removePatchVariants(patchesDir string, rel string) error {
	variants := []string{
		filepath.Join(patchesDir, filepath.FromSlash(rel)),
		filepath.Join(patchesDir, filepath.FromSlash(rel+".deleted")),
		filepath.Join(patchesDir, filepath.FromSlash(rel+".binary")),
		filepath.Join(patchesDir, filepath.FromSlash(rel+".rename")),
	}
	for _, variant := range variants {
		if err := os.Remove(variant); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func parseRenameMarker(rel string, body string) (FilePatch, error) {
	patchFile := FilePatch{Path: rel, Op: OpRename}
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "Renamed from: "):
			patchFile.OldPath = NormalizeChromiumPath(strings.TrimPrefix(line, "Renamed from: "))
		case strings.HasPrefix(line, "Similarity: "):
			value := strings.TrimSuffix(strings.TrimPrefix(line, "Similarity: "), "%")
			if similarity, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				patchFile.Similarity = similarity
			}
		}
	}
	if patchFile.OldPath == "" {
		return FilePatch{}, fmt.Errorf("rename marker missing source path for %s", rel)
	}
	return patchFile, nil
}
