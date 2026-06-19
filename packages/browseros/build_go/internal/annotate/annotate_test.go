package annotate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t.t",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t.t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
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

func fixtureCtx(t *testing.T) *buildctx.Context {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pyproject.toml"), "name = \"browseros\"\n")

	src := filepath.Join(t.TempDir(), "src")
	os.MkdirAll(src, 0o755)
	runGit(t, src, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(src, "chrome", "a.cc"), "a\n")
	writeFile(t, filepath.Join(src, "chrome", "b.cc"), "b\n")
	writeFile(t, filepath.Join(src, "chrome", "c.cc"), "c\n")
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "base")

	writeFile(t, filepath.Join(root, "build", "features.yaml"), `version: "1.0"
features:
  feature-a:
    description: "feat: feature a"
    files:
      - chrome/a.cc
  feature-b:
    description: "feat: feature b"
    files:
      - chrome/b.cc
  feature-untouched:
    description: "feat: untouched"
    files:
      - chrome/c.cc
`)

	plat := platform.Platform{OS: "macos", Arch: "arm64"}
	ctx, err := buildctx.New(buildctx.Options{ChromiumSrc: src, Platform: &plat, RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestFeaturesCommitsModifiedFilesPerFeature(t *testing.T) {
	ctx := fixtureCtx(t)
	src := ctx.ChromiumSrc

	writeFile(t, filepath.Join(src, "chrome", "a.cc"), "a modified\n")
	writeFile(t, filepath.Join(src, "chrome", "b.cc"), "b modified\n")

	commits, skipped, err := Features(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if commits != 2 || skipped != 1 {
		t.Errorf("commits = %d, skipped = %d, want 2/1", commits, skipped)
	}

	log := runGit(t, src, "log", "--format=%s", "-n", "3")
	if !strings.Contains(log, "feat: feature a") || !strings.Contains(log, "feat: feature b") {
		t.Errorf("commit subjects:\n%s", log)
	}
	if strings.Contains(log, "untouched") {
		t.Error("untouched feature must not be committed")
	}

	// Working tree clean afterwards.
	status := runGit(t, src, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("tree not clean:\n%s", status)
	}
}

func TestFeaturesFilterSingleFeature(t *testing.T) {
	ctx := fixtureCtx(t)
	src := ctx.ChromiumSrc
	writeFile(t, filepath.Join(src, "chrome", "a.cc"), "a modified\n")
	writeFile(t, filepath.Join(src, "chrome", "b.cc"), "b modified\n")

	commits, _, err := Features(ctx, "feature-a")
	if err != nil || commits != 1 {
		t.Fatalf("commits = %d, err = %v", commits, err)
	}
	// b.cc stays dirty (not committed).
	status := runGit(t, src, "status", "--porcelain")
	if !strings.Contains(status, "b.cc") {
		t.Errorf("feature-b file should remain dirty:\n%s", status)
	}

	if _, _, err := Features(ctx, "no-such-feature"); err == nil {
		t.Error("unknown feature should error")
	}
}
