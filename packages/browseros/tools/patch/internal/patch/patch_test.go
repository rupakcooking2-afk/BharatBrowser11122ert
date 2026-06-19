package patch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/git"
)

func TestParseDiffOutputDetectsRenameAndDeleteSignatures(t *testing.T) {
	renameDiff := `diff --git a/chrome/old.cc b/chrome/new.cc
similarity index 100%
rename from chrome/old.cc
rename to chrome/new.cc
`
	deleteDiff := `diff --git a/chrome/dead.cc b/chrome/dead.cc
deleted file mode 100644
index 123..000 100644
--- a/chrome/dead.cc
+++ /dev/null
@@ -1 +0,0 @@
-gone
`
	renameSet, err := ParseDiffOutput(renameDiff)
	if err != nil {
		t.Fatalf("ParseDiffOutput rename: %v", err)
	}
	deleteSet, err := ParseDiffOutput(deleteDiff)
	if err != nil {
		t.Fatalf("ParseDiffOutput delete: %v", err)
	}
	renamePatch := renameSet["chrome/new.cc"]
	if !renamePatch.IsPureRename() {
		t.Fatalf("expected pure rename patch")
	}
	if deletePatch := deleteSet["chrome/dead.cc"]; signature(deletePatch) != "delete:chrome/dead.cc" {
		t.Fatalf("unexpected delete signature: %s", signature(deletePatch))
	}
}

func TestWriteRepoPatchSetWritesMarkersAndReloads(t *testing.T) {
	patchesDir := t.TempDir()
	set := PatchSet{
		"chrome/dead.cc": {
			Path: "chrome/dead.cc",
			Op:   OpDelete,
		},
		"chrome/new.cc": {
			Path:       "chrome/new.cc",
			Op:         OpRename,
			OldPath:    "chrome/old.cc",
			Similarity: 100,
			Content: []byte(`diff --git a/chrome/old.cc b/chrome/new.cc
similarity index 100%
rename from chrome/old.cc
rename to chrome/new.cc
`),
		},
	}
	if _, err := WriteRepoPatchSet(patchesDir, set, nil); err != nil {
		t.Fatalf("WriteRepoPatchSet: %v", err)
	}
	if _, err := filepath.Abs(filepath.Join(patchesDir, "chrome", "dead.cc.deleted")); err != nil {
		t.Fatalf("abs: %v", err)
	}
	loaded, err := LoadRepoPatchSet(patchesDir, nil)
	if err != nil {
		t.Fatalf("LoadRepoPatchSet: %v", err)
	}
	if loaded["chrome/dead.cc"].Op != OpDelete {
		t.Fatalf("expected delete marker to round-trip")
	}
	if !loaded["chrome/new.cc"].IsPureRename() {
		t.Fatalf("expected rename marker to round-trip")
	}
}

func TestPathMatchesSkipsInternalState(t *testing.T) {
	if PathMatches(".browseros-patch/state.yaml", nil) {
		t.Fatalf("expected internal state path to be ignored")
	}
}

func TestBuildRangePatchSetUsesLatestBaseScopedPatch(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "config", "user.email", "test@example.com")

	writeRepoFile(t, filepath.Join(repoDir, "chrome", "foo.txt"), "one\n")
	runGit(t, repoDir, "add", "chrome/foo.txt")
	runGit(t, repoDir, "commit", "-m", "base")
	base := gitOutput(t, repoDir, "rev-parse", "HEAD")

	writeRepoFile(t, filepath.Join(repoDir, "chrome", "foo.txt"), "two\n")
	runGit(t, repoDir, "commit", "-am", "step one")
	writeRepoFile(t, filepath.Join(repoDir, "chrome", "foo.txt"), "three\n")
	runGit(t, repoDir, "commit", "-am", "step two")
	end := gitOutput(t, repoDir, "rev-parse", "HEAD")

	set, err := BuildRangePatchSet(ctx, repoDir, base, end, base, false, nil)
	if err != nil {
		t.Fatalf("BuildRangePatchSet: %v", err)
	}
	content := string(set["chrome/foo.txt"].Content)
	if !strings.Contains(content, "+three") {
		t.Fatalf("expected final patch content, got %q", content)
	}
	if strings.Contains(content, "+two") {
		t.Fatalf("expected latest base-scoped patch, got %q", content)
	}
}

func modifyPatch(rel string, indexLine string, addedLine string) FilePatch {
	content := "diff --git a/" + rel + " b/" + rel + "\n" +
		indexLine + "\n" +
		"--- a/" + rel + "\n" +
		"+++ b/" + rel + "\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+" + addedLine + "\n"
	return FilePatch{Path: rel, Op: OpModify, Content: []byte(content)}
}

const fullIndex = "index 0000000000000000000000000000000000000000..1111111111111111111111111111111111111111 100644"
const abbrevIndex = "index 000000000000a..111111111111b 100644"

func TestWriteRepoPatchSetSkipsUnchangedFiles(t *testing.T) {
	patchesDir := t.TempDir()
	rel := "chrome/foo.cc"
	set := PatchSet{rel: modifyPatch(rel, fullIndex, "new")}

	plan, err := WriteRepoPatchSet(patchesDir, set, nil)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if len(plan.Written()) != 1 {
		t.Fatalf("first write should create the file, wrote %v", plan.Written())
	}

	// Simulate a legacy on-disk patch: same hunks, abbreviated index line.
	legacy := string(modifyPatch(rel, abbrevIndex, "new").Content)
	target := filepath.Join(patchesDir, "chrome", "foo.cc")
	if err := os.WriteFile(target, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	plan, err = WriteRepoPatchSet(patchesDir, set, nil)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if len(plan.Written()) != 0 || len(plan.Deletes) != 0 {
		t.Fatalf("expected no-op write, got written=%v deleted=%v", plan.Written(), plan.Deletes)
	}
	if len(plan.Unchanged) != 1 || plan.Unchanged[0] != rel {
		t.Fatalf("expected %s unchanged, got %v", rel, plan.Unchanged)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(after) != legacy {
		t.Fatalf("legacy bytes must be preserved verbatim\n--- want ---\n%q\n--- got ---\n%q", legacy, string(after))
	}
}

func TestWriteRepoPatchSetRewritesChangedContent(t *testing.T) {
	patchesDir := t.TempDir()
	rel := "chrome/foo.cc"
	if _, err := WriteRepoPatchSet(patchesDir, PatchSet{rel: modifyPatch(rel, fullIndex, "new")}, nil); err != nil {
		t.Fatalf("first write: %v", err)
	}

	next := modifyPatch(rel, fullIndex, "different")
	plan, err := WriteRepoPatchSet(patchesDir, PatchSet{rel: next}, nil)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if len(plan.Written()) != 1 || plan.Written()[0] != rel {
		t.Fatalf("expected rewrite of %s, got written=%v unchanged=%v", rel, plan.Written(), plan.Unchanged)
	}
	after, err := os.ReadFile(filepath.Join(patchesDir, "chrome", "foo.cc"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(after) != string(next.Content) {
		t.Fatalf("expected canonical rewrite, got %q", string(after))
	}
}

func TestWriteRepoPatchSetSkipsUnchangedMarkers(t *testing.T) {
	patchesDir := t.TempDir()
	set := PatchSet{"chrome/dead.cc": {Path: "chrome/dead.cc", Op: OpDelete}}
	if _, err := WriteRepoPatchSet(patchesDir, set, nil); err != nil {
		t.Fatalf("first write: %v", err)
	}
	plan, err := WriteRepoPatchSet(patchesDir, set, nil)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if len(plan.Written()) != 0 || len(plan.Unchanged) != 1 {
		t.Fatalf("expected marker to be skip-stable, written=%v unchanged=%v", plan.Written(), plan.Unchanged)
	}
}

func TestWriteRepoPatchSetScopeMatchesDirectories(t *testing.T) {
	patchesDir := t.TempDir()
	inDir := modifyPatch("chrome/sub/in.cc", fullIndex, "new")
	outDir := modifyPatch("ui/out.cc", fullIndex, "new")
	plan, err := WriteRepoPatchSet(patchesDir, PatchSet{
		"chrome/sub/in.cc": inDir,
		"ui/out.cc":        outDir,
	}, []string{"chrome/sub"})
	if err != nil {
		t.Fatalf("WriteRepoPatchSet: %v", err)
	}
	assertStrings(t, "written", plan.Written(), []string{"chrome/sub/in.cc"})
	if _, err := os.Stat(filepath.Join(patchesDir, "ui", "out.cc")); !os.IsNotExist(err) {
		t.Fatalf("out-of-scope patch must not be written")
	}
}

func TestPlanRepoPatchSetClassifies(t *testing.T) {
	patchesDir := t.TempDir()
	existingSame := modifyPatch("chrome/same.cc", fullIndex, "new")
	existingDiff := modifyPatch("chrome/diff.cc", fullIndex, "new")
	existingGone := modifyPatch("chrome/gone.cc", fullIndex, "new")
	seed := PatchSet{
		"chrome/same.cc": existingSame,
		"chrome/diff.cc": existingDiff,
		"chrome/gone.cc": existingGone,
	}
	if _, err := WriteRepoPatchSet(patchesDir, seed, nil); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	incoming := PatchSet{
		"chrome/same.cc": existingSame,
		"chrome/diff.cc": modifyPatch("chrome/diff.cc", fullIndex, "changed"),
		"chrome/new.cc":  modifyPatch("chrome/new.cc", fullIndex, "new"),
	}
	plan, err := PlanRepoPatchSet(patchesDir, incoming, nil)
	if err != nil {
		t.Fatalf("PlanRepoPatchSet: %v", err)
	}
	assertStrings(t, "creates", plan.Creates, []string{"chrome/new.cc"})
	assertStrings(t, "updates", plan.Updates, []string{"chrome/diff.cc"})
	assertStrings(t, "unchanged", plan.Unchanged, []string{"chrome/same.cc"})
	assertStrings(t, "deletes", plan.Deletes, []string{"chrome/gone.cc"})
}

func assertStrings(t *testing.T, label string, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func TestSyntheticAddPatchMatchesGitByteForByte(t *testing.T) {
	cases := []struct {
		name string
		rel  string
		body string
		mode os.FileMode
	}{
		{name: "multi line", rel: "chrome/notes.txt", body: "alpha\nbeta\ngamma\n", mode: 0o644},
		{name: "single line", rel: "chrome/one.txt", body: "single\n", mode: 0o644},
		{name: "single line no newline", rel: "chrome/one_nonl.txt", body: "single", mode: 0o644},
		{name: "no trailing newline", rel: "chrome/nonl.txt", body: "alpha\nbeta", mode: 0o644},
		{name: "empty file", rel: "chrome/empty.txt", body: "", mode: 0o644},
		{name: "executable", rel: "chrome/tool.sh", body: "#!/bin/sh\necho hi\n", mode: 0o755},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			runGit(t, dir, "init")
			full := filepath.Join(dir, filepath.FromSlash(tc.rel))
			writeRepoFile(t, full, tc.body)
			if err := os.Chmod(full, tc.mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			want := gitNoIndexDiff(t, dir, tc.rel)

			gitMode := "100644"
			if tc.mode&0o100 != 0 {
				gitMode = "100755"
			}
			got := syntheticAddPatch(tc.rel, []byte(tc.body), gitMode)
			if got.Op != OpAdd {
				t.Fatalf("op = %s, want ADD", got.Op)
			}
			if string(got.Content) != want {
				t.Fatalf("synthetic patch differs from git output\n--- git ---\n%q\n--- synthetic ---\n%q", want, string(got.Content))
			}
		})
	}
}

func TestSyntheticAddPatchAppliesAndRoundTrips(t *testing.T) {
	ctx := context.Background()
	body := "alpha\nbeta"
	patchFile := syntheticAddPatch("chrome/nonl.txt", []byte(body), "100644")

	dir := t.TempDir()
	runGit(t, dir, "init")
	if _, err := git.ApplyPatch(ctx, dir, patchFile.Content); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "chrome", "nonl.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != body {
		t.Fatalf("applied content = %q, want %q (content must not be mutated)", string(data), body)
	}
}

func TestSyntheticAddPatchMarksBinaryContent(t *testing.T) {
	got := syntheticAddPatch("chrome/icon.png", []byte("\x89PNG\x00\x01binary"), "100644")
	if got.Op != OpBinary || !got.IsBinary {
		t.Fatalf("expected binary marker patch, got op=%s isBinary=%v", got.Op, got.IsBinary)
	}
	if len(got.Content) != 0 {
		t.Fatalf("binary marker should carry no diff content, got %q", string(got.Content))
	}
}

// gitNoIndexDiff returns git's own add-style diff for an untracked file,
// tolerating the exit status 1 git uses to signal "differences found".
func gitNoIndexDiff(t *testing.T, dir string, rel string) string {
	t.Helper()
	cmd := exec.Command("git",
		"-c", "diff.algorithm=myers",
		"-c", "diff.noprefix=false",
		"-c", "diff.mnemonicPrefix=false",
		"-c", "core.quotepath=false",
		"diff", "--binary", "--full-index", "-U3", "--no-index", "--", "/dev/null", rel)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("git diff --no-index: %v\n%s", err, string(output))
		}
	}
	return string(output)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
	}
	return strings.TrimSpace(string(output))
}

func writeRepoFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
