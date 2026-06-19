package patchwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

// --- test helpers: real git repos in temp dirs (patch-tool test style) ---

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t.t",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t.t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src")
	os.MkdirAll(dir, 0o755)
	runGit(t, dir, "init", "-q", "-b", "main")
	return dir
}

// fixtureCtx builds a Context whose chromium_src is a real git repo with one
// base commit (chrome/foo.cc, chrome/bar.cc), recording BASE_COMMIT.
func fixtureCtx(t *testing.T) (*buildctx.Context, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pyproject.toml"), "name = \"browseros\"\n")

	src := initRepo(t)
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2\nline3\n")
	writeFile(t, filepath.Join(src, "chrome", "bar.cc"), "alpha\nbeta\ngamma\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(runGit(t, src, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(root, "BASE_COMMIT"), base+"\n")

	plat := platform.Platform{OS: "macos", Arch: "arm64"}
	ctx, err := buildctx.New(buildctx.Options{ChromiumSrc: src, Platform: &plat, RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, base
}

// --- diff parsing ---

func TestParseDiffOutputDetectsOperations(t *testing.T) {
	diff := `diff --git a/chrome/new.cc b/chrome/new.cc
new file mode 100644
index 0000000..e69de29
--- /dev/null
+++ b/chrome/new.cc
@@ -0,0 +1 @@
+hello
diff --git a/chrome/gone.cc b/chrome/gone.cc
deleted file mode 100644
index e69de29..0000000
--- a/chrome/gone.cc
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/chrome/old_name.cc b/chrome/new_name.cc
similarity index 97%
rename from chrome/old_name.cc
rename to chrome/new_name.cc
index 123..456 100644
--- a/chrome/old_name.cc
+++ b/chrome/new_name.cc
@@ -1 +1 @@
-x
+y
diff --git a/chrome/logo.png b/chrome/logo.png
index 111..222 100644
Binary files a/chrome/logo.png and b/chrome/logo.png differ
`
	order, byPath := ParseDiffOutput(diff)
	if len(order) != 4 {
		t.Fatalf("parsed %d patches, want 4", len(order))
	}
	if byPath["chrome/new.cc"].Operation != OpAdd {
		t.Errorf("new.cc op = %s", byPath["chrome/new.cc"].Operation)
	}
	if byPath["chrome/gone.cc"].Operation != OpDelete {
		t.Errorf("gone.cc op = %s", byPath["chrome/gone.cc"].Operation)
	}
	rename := byPath["chrome/new_name.cc"]
	if rename.Operation != OpRename || rename.OldPath != "chrome/old_name.cc" || rename.Similarity != 97 {
		t.Errorf("rename = %+v", rename)
	}
	logo := byPath["chrome/logo.png"]
	if !logo.IsBinary || logo.Operation != OpBinary || logo.Content != "" {
		t.Errorf("binary = %+v", logo)
	}
	if !strings.Contains(byPath["chrome/new.cc"].Content, "+hello") {
		t.Errorf("content missing diff body: %q", byPath["chrome/new.cc"].Content)
	}
}

// --- find/apply ---

func TestFindPatchFilesSortsAndExcludesMarkers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "chrome", "b.cc"), "patch")
	writeFile(t, filepath.Join(dir, "chrome", "a.cc"), "patch")
	writeFile(t, filepath.Join(dir, "chrome", "x.cc.deleted"), "marker")
	writeFile(t, filepath.Join(dir, "chrome", "y.png.binary"), "marker")
	writeFile(t, filepath.Join(dir, "chrome", "z.cc.rename"), "marker")
	writeFile(t, filepath.Join(dir, ".hidden"), "dot")

	files := FindPatchFiles(dir)
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	if !strings.HasSuffix(files[0], "a.cc") || !strings.HasSuffix(files[1], "b.cc") {
		t.Errorf("sort order wrong: %v", files)
	}
}

func TestApplyAllPatchesAppliesRealPatch(t *testing.T) {
	ctx, _ := fixtureCtx(t)

	// Build a patch by editing foo.cc and capturing the diff.
	src := ctx.ChromiumSrc
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 EDITED\nline3\n")
	diff := runGit(t, src, "diff")
	runGit(t, src, "checkout", "--", ".") // restore

	writeFile(t, filepath.Join(ctx.PatchesDir(), "chrome", "foo.cc"), diff)

	applied, failed, err := ApplyAllPatches(ctx, false, false, AutoConfirmer{}, "")
	if err != nil || applied != 1 || len(failed) != 0 {
		t.Fatalf("apply = (%d, %v, %v)", applied, failed, err)
	}
	content, _ := os.ReadFile(filepath.Join(src, "chrome", "foo.cc"))
	if !strings.Contains(string(content), "line2 EDITED") {
		t.Errorf("patch not applied:\n%s", content)
	}
}

func TestApplyAllPatchesDryRunTouchesNothing(t *testing.T) {
	ctx, _ := fixtureCtx(t)
	src := ctx.ChromiumSrc
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 EDITED\nline3\n")
	diff := runGit(t, src, "diff")
	runGit(t, src, "checkout", "--", ".")
	writeFile(t, filepath.Join(ctx.PatchesDir(), "chrome", "foo.cc"), diff)

	applied, failed, err := ApplyAllPatches(ctx, true, false, AutoConfirmer{}, "")
	if err != nil || applied != 1 || len(failed) != 0 {
		t.Fatalf("dry run = (%d, %v, %v)", applied, failed, err)
	}
	content, _ := os.ReadFile(filepath.Join(src, "chrome", "foo.cc"))
	if strings.Contains(string(content), "EDITED") {
		t.Error("dry run must not modify files")
	}
}

func TestApplyAllForceWritesRejFilesOnConflict(t *testing.T) {
	ctx, _ := fixtureCtx(t)
	src := ctx.ChromiumSrc

	// A patch built against content that no longer matches → conflict.
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 EDITED\nline3\n")
	diff := runGit(t, src, "diff")
	runGit(t, src, "checkout", "--", ".")
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "completely\ndifferent\ncontent\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "diverge")

	writeFile(t, filepath.Join(ctx.PatchesDir(), "chrome", "foo.cc"), diff)

	applied, rejected, failed := ApplyAllForce(ctx, "")
	if applied != 0 || rejected != 1 || len(failed) != 1 {
		t.Fatalf("force = (%d, %d, %v)", applied, rejected, failed)
	}
	if _, err := os.Stat(filepath.Join(src, "chrome", "foo.cc.rej")); err != nil {
		t.Errorf(".rej file not written: %v", err)
	}
}

func TestApplyWithResetToRestoresBaseFirst(t *testing.T) {
	ctx, base := fixtureCtx(t)
	src := ctx.ChromiumSrc

	// Patch against base content.
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 EDITED\nline3\n")
	diff := runGit(t, src, "diff")
	runGit(t, src, "checkout", "--", ".")
	writeFile(t, filepath.Join(ctx.PatchesDir(), "chrome", "foo.cc"), diff)

	// Diverge the working tree so the patch only applies after reset.
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "diverged\n")

	ok, msg := ApplySingleFilePatch(ctx, "chrome/foo.cc", base, false)
	if !ok {
		t.Fatalf("apply with reset failed: %s", msg)
	}
	content, _ := os.ReadFile(filepath.Join(src, "chrome", "foo.cc"))
	if !strings.Contains(string(content), "line2 EDITED") {
		t.Errorf("content after reset+apply:\n%s", content)
	}
}

// --- extract ---

func TestExtractCommitWritesPatchesAgainstBase(t *testing.T) {
	ctx, _ := fixtureCtx(t)
	src := ctx.ChromiumSrc

	// Commit 1: edit foo. Commit 2: edit foo again + delete bar.
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 v2\nline3\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "edit foo")
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 v3\nline3\n")
	os.Remove(filepath.Join(src, "chrome", "bar.cc"))
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "edit foo again, delete bar")

	count, extracted, err := ExtractCommit(ctx, "HEAD", ExtractOptions{Force: true, Confirm: AutoConfirmer{Choice: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(extracted) != 2 {
		t.Fatalf("count=%d extracted=%v", count, extracted)
	}

	// foo patch is the FULL diff from BASE_COMMIT (contains v3, not v2).
	patch, err := os.ReadFile(filepath.Join(ctx.PatchesDir(), "chrome", "foo.cc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "+line2 v3") || strings.Contains(string(patch), "v2") {
		t.Errorf("patch should be cumulative from base:\n%s", patch)
	}

	// bar deletion → .deleted marker.
	marker := filepath.Join(ctx.PatchesDir(), "chrome", "bar.cc.deleted")
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("deletion marker missing: %v", err)
	}
	if !strings.Contains(string(content), "Original path: chrome/bar.cc") {
		t.Errorf("marker content:\n%s", content)
	}
}

func TestExtractCommitDefaultsBaseToBaseCommitFile(t *testing.T) {
	ctx, base := fixtureCtx(t)
	resolved, err := ResolveBaseCommit(ctx, "")
	if err != nil || resolved != base {
		t.Errorf("ResolveBaseCommit = (%q, %v), want %q", resolved, err, base)
	}
	if got, _ := ResolveBaseCommit(ctx, "explicit"); got != "explicit" {
		t.Errorf("explicit base should win, got %q", got)
	}

	os.Remove(ctx.BaseCommitFile())
	if _, err := ResolveBaseCommit(ctx, ""); err == nil {
		t.Error("missing BASE_COMMIT should error")
	}
}

func TestExtractRangeCumulativeDiff(t *testing.T) {
	ctx, base := fixtureCtx(t)
	src := ctx.ChromiumSrc

	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 r1\nline3\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "r1")
	writeFile(t, filepath.Join(src, "chrome", "new.cc"), "brand new\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "r2")
	head := strings.TrimSpace(runGit(t, src, "rev-parse", "HEAD"))

	count, _, err := ExtractRange(ctx, base, head, ExtractOptions{Force: true})
	if err != nil || count != 2 {
		t.Fatalf("range = (%d, %v)", count, err)
	}
	if _, err := os.Stat(filepath.Join(ctx.PatchesDir(), "chrome", "foo.cc")); err != nil {
		t.Error("foo.cc patch missing")
	}
	newPatch, err := os.ReadFile(filepath.Join(ctx.PatchesDir(), "chrome", "new.cc"))
	if err != nil || !strings.Contains(string(newPatch), "+brand new") {
		t.Errorf("new.cc patch = (%s, %v)", newPatch, err)
	}
}

func TestExtractFilePatchFromWorkingTree(t *testing.T) {
	ctx, _ := fixtureCtx(t)
	src := ctx.ChromiumSrc

	// Uncommitted working-tree edit.
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 WT\nline3\n")

	ok, err := ExtractFilePatch(ctx, "chrome/foo.cc", ExtractOptions{Force: true})
	if !ok || err != nil {
		t.Fatalf("extract = (%v, %v)", ok, err)
	}
	patch, _ := os.ReadFile(filepath.Join(ctx.PatchesDir(), "chrome", "foo.cc"))
	if !strings.Contains(string(patch), "+line2 WT") {
		t.Errorf("patch content:\n%s", patch)
	}

	// No changes → error.
	runGit(t, src, "checkout", "--", ".")
	_, err = ExtractFilePatch(ctx, "chrome/bar.cc", ExtractOptions{Force: true})
	if err == nil || !strings.Contains(err.Error(), "no changes found") {
		t.Errorf("err = %v", err)
	}
}

func TestExtractOverwritePromptCancels(t *testing.T) {
	ctx, _ := fixtureCtx(t)
	src := ctx.ChromiumSrc
	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 X\nline3\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "x")

	// Pre-existing patch + confirmer that answers "no".
	writeFile(t, filepath.Join(ctx.PatchesDir(), "chrome", "foo.cc"), "old patch")
	count, _, err := ExtractCommit(ctx, "HEAD", ExtractOptions{Confirm: AutoConfirmer{Answer: false}})
	if err != nil || count != 0 {
		t.Fatalf("cancelled extract = (%d, %v)", count, err)
	}
	content, _ := os.ReadFile(filepath.Join(ctx.PatchesDir(), "chrome", "foo.cc"))
	if string(content) != "old patch" {
		t.Error("existing patch must be untouched after cancel")
	}
}

// --- series ---

func TestParseSeriesSkipsCommentsAndInline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "series")
	writeFile(t, path, "# header comment\n\npatches/one.patch\npatches/two.patch # inline note\n   \n#disabled.patch\n")
	got, err := ParseSeries(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"patches/one.patch", "patches/two.patch"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ParseSeries = %v, want %v", got, want)
	}
}

func TestSeriesFilesCommonThenPlatform(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "series"), "a.patch\n")
	writeFile(t, filepath.Join(dir, "series.macos"), "b.patch\n")
	writeFile(t, filepath.Join(dir, "series.windows"), "c.patch\n")

	files := SeriesFiles(dir, "macos")
	if len(files) != 2 || !strings.HasSuffix(files[0], "series") || !strings.HasSuffix(files[1], "series.macos") {
		t.Errorf("files = %v", files)
	}
	if files := SeriesFiles(dir, "linux"); len(files) != 1 {
		t.Errorf("linux files = %v", files)
	}
}

func TestApplySeriesPatchesAppliesInOrder(t *testing.T) {
	ctx, _ := fixtureCtx(t)
	src := ctx.ChromiumSrc

	writeFile(t, filepath.Join(src, "chrome", "foo.cc"), "line1\nline2 S1\nline3\n")
	diff1 := runGit(t, src, "diff")
	runGit(t, src, "checkout", "--", ".")

	seriesDir := ctx.SeriesPatchesDir()
	writeFile(t, filepath.Join(seriesDir, "patches", "edit-foo.patch"), diff1)
	writeFile(t, filepath.Join(seriesDir, "series"), "patches/edit-foo.patch\n")

	applied, failed := ApplySeriesPatches(ctx, false)
	if len(applied) != 1 || len(failed) != 0 {
		t.Fatalf("series = (%v, %v)", applied, failed)
	}
	content, _ := os.ReadFile(filepath.Join(src, "chrome", "foo.cc"))
	if !strings.Contains(string(content), "line2 S1") {
		t.Errorf("series patch not applied:\n%s", content)
	}

	// Missing listed patch → failed.
	writeFile(t, filepath.Join(seriesDir, "series"), "patches/edit-foo.patch\npatches/missing.patch\n")
	runGit(t, src, "checkout", "--", ".")
	_, failed = ApplySeriesPatches(ctx, false)
	if len(failed) != 1 {
		t.Errorf("missing patch should fail: %v", failed)
	}
}
