package patch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoreSetDefaultPatterns(t *testing.T) {
	set, err := LoadIgnoreSet(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("LoadIgnoreSet: %v", err)
	}
	for _, rel := range []string{
		".llm/task.md",
		"chrome/.llm/notes.md",
		".browseros-patch/state.yaml",
		"debug.log",
		"chrome/browser/debug.log",
		"chrome/browser/foo.cc.rej",
		"chrome/browser/foo.cc.orig",
		".DS_Store",
		"chrome/app/.DS_Store",
	} {
		if !set.Match(rel) {
			t.Fatalf("expected default ignore to match %q", rel)
		}
	}
	for _, rel := range []string{
		"chrome/browser/foo.cc",
		"chrome/login.cc",
		"chrome/llm/feature.cc",
		"tools/logger.cc",
	} {
		if set.Match(rel) {
			t.Fatalf("expected default ignore not to match %q", rel)
		}
	}
}

func TestIgnoreSetNilNeverMatches(t *testing.T) {
	var set *IgnoreSet
	if set.Match(".llm/task.md") {
		t.Fatalf("nil ignore set must not match")
	}
}

func TestLoadIgnoreSetReadsRepoFileAndExtras(t *testing.T) {
	root := t.TempDir()
	body := "# local junk\n\nscratch/\n*.tmp\nchrome/generated/*.json\n"
	if err := os.WriteFile(filepath.Join(root, ".browseros-patchignore"), []byte(body), 0o644); err != nil {
		t.Fatalf("write ignore file: %v", err)
	}
	set, err := LoadIgnoreSet(root, []string{"third_party/sparkle/"})
	if err != nil {
		t.Fatalf("LoadIgnoreSet: %v", err)
	}
	cases := map[string]bool{
		"scratch/notes.md":              true,
		"chrome/scratch/notes.md":       true,
		"build.tmp":                     true,
		"chrome/generated/strings.json": true,
		"chrome/generated/sub/x.json":   false,
		"third_party/sparkle/bin":       true,
		"chrome/real.cc":                false,
	}
	for rel, want := range cases {
		if got := set.Match(rel); got != want {
			t.Fatalf("Match(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestBuildWorkingTreePatchSetAppliesIgnoreToUntrackedOnly(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")

	// Tracked file whose name matches an ignore pattern: must stay visible.
	writeRepoFile(t, filepath.Join(dir, "chrome", "trace.log"), "tracked\n")
	runGit(t, dir, "add", "chrome/trace.log")
	runGit(t, dir, "commit", "-m", "base")
	base := gitOutput(t, dir, "rev-parse", "HEAD")
	writeRepoFile(t, filepath.Join(dir, "chrome", "trace.log"), "tracked modified\n")

	// Untracked junk and untracked real content.
	writeRepoFile(t, filepath.Join(dir, ".llm", "scratch.md"), "junk\n")
	writeRepoFile(t, filepath.Join(dir, "debug.log"), "junk\n")
	writeRepoFile(t, filepath.Join(dir, "chrome", "feature.cc"), "real\n")

	ign, err := LoadIgnoreSet(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("LoadIgnoreSet: %v", err)
	}
	set, err := BuildWorkingTreePatchSet(ctx, dir, WorkingTreeOptions{Base: base, Ignore: ign})
	if err != nil {
		t.Fatalf("BuildWorkingTreePatchSet: %v", err)
	}
	if _, ok := set["chrome/trace.log"]; !ok {
		t.Fatalf("tracked modification must never be ignored, got %v", set)
	}
	if _, ok := set["chrome/feature.cc"]; !ok {
		t.Fatalf("untracked real content should be present, got %v", set)
	}
	for _, junk := range []string{".llm/scratch.md", "debug.log"} {
		if _, ok := set[junk]; ok {
			t.Fatalf("untracked junk %q should be ignored", junk)
		}
	}
}
