package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var fullIndexLine = regexp.MustCompile(`(?m)^index [0-9a-f]{40}\.\.[0-9a-f]{40}`)
var fullIndexAddLine = regexp.MustCompile(`(?m)^index 0{40}\.\.[0-9a-f]{40}`)

func TestDiffTextEmitsFullIndexRegardlessOfRepoConfig(t *testing.T) {
	ctx := context.Background()
	dir := initGitRepo(t)
	// Hostile per-checkout config that must not leak into tool output.
	runGit(t, dir, "config", "core.abbrev", "9")
	runGit(t, dir, "config", "diff.noprefix", "true")
	runGit(t, dir, "config", "diff.mnemonicPrefix", "true")
	runGit(t, dir, "config", "diff.algorithm", "histogram")
	runGit(t, dir, "config", "diff.context", "8")
	runGit(t, dir, "config", "diff.interHunkContext", "10")
	runGit(t, dir, "config", "diff.suppressBlankEmpty", "true")
	runGit(t, dir, "config", "diff.srcPrefix", "x/")
	runGit(t, dir, "config", "diff.dstPrefix", "y/")

	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("l%d", i))
	}
	lines[3] = "" // blank context line inside the first hunk
	base := strings.Join(lines, "\n") + "\n"
	writeFile(t, filepath.Join(dir, "f.txt"), base)
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-m", "base")
	lines[1] = "CHANGED2"
	lines[17] = "CHANGED18"
	writeFile(t, filepath.Join(dir, "f.txt"), strings.Join(lines, "\n")+"\n")

	diff, err := DiffText(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("DiffText: %v", err)
	}
	if !fullIndexLine.MatchString(diff) {
		t.Fatalf("expected full 40-hex index line, got:\n%s", diff)
	}
	if !strings.Contains(diff, "--- a/f.txt") || !strings.Contains(diff, "+++ b/f.txt") {
		t.Fatalf("expected a/ b/ prefixes despite prefix config, got:\n%s", diff)
	}
	if !strings.Contains(diff, "@@ -1,5 +1,5 @@") || !strings.Contains(diff, "@@ -15,6 +15,6 @@") {
		t.Fatalf("expected two default-context hunks despite diff.context/interHunkContext, got:\n%s", diff)
	}
	if !strings.Contains(diff, "\n \n") {
		t.Fatalf("expected blank context lines to keep their space despite suppressBlankEmpty, got:\n%q", diff)
	}
}

func TestDiffNoIndexEmitsFullIndexNewFile(t *testing.T) {
	ctx := context.Background()
	dir := initGitRepo(t)
	writeFile(t, filepath.Join(dir, "new.txt"), "hello\n")

	diff, err := DiffNoIndex(ctx, dir, "new.txt")
	if err != nil {
		t.Fatalf("DiffNoIndex: %v", err)
	}
	if !strings.Contains(diff, "new file mode 100644") {
		t.Fatalf("expected new file mode, got:\n%s", diff)
	}
	if !fullIndexAddLine.MatchString(diff) {
		t.Fatalf("expected full-index add line, got:\n%s", diff)
	}
}

func TestFileModeAtCommit(t *testing.T) {
	ctx := context.Background()
	dir := initGitRepo(t)
	writeFile(t, filepath.Join(dir, "plain.txt"), "x\n")
	writeFile(t, filepath.Join(dir, "tool.sh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "tool.sh"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	runGit(t, dir, "add", "plain.txt", "tool.sh")
	runGit(t, dir, "commit", "-m", "base")

	mode, err := FileModeAtCommit(ctx, dir, "HEAD", "plain.txt")
	if err != nil {
		t.Fatalf("FileModeAtCommit plain: %v", err)
	}
	if mode != "100644" {
		t.Fatalf("plain mode = %q, want 100644", mode)
	}
	mode, err = FileModeAtCommit(ctx, dir, "HEAD", "tool.sh")
	if err != nil {
		t.Fatalf("FileModeAtCommit exec: %v", err)
	}
	if mode != "100755" {
		t.Fatalf("exec mode = %q, want 100755", mode)
	}
	mode, err = FileModeAtCommit(ctx, dir, "HEAD", "missing.txt")
	if err != nil {
		t.Fatalf("missing path must not be a git failure: %v", err)
	}
	if mode != "" {
		t.Fatalf("missing path mode = %q, want empty", mode)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	return dir
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

func writeFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestStashRebaseFlagsModifyDeleteConflict(t *testing.T) {
	ctx := context.Background()
	dir := initGitRepo(t)
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-m", "base")

	// Local modification parked in a stash, then the file is deleted from
	// the tree (as a .deleted patch would do).
	writeFile(t, filepath.Join(dir, "f.txt"), "local edit\n")
	sha, err := StashPush(ctx, dir, "test", true, []string{"f.txt"})
	if err != nil {
		t.Fatalf("StashPush: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("StashPush should return a commit SHA, got %q", sha)
	}
	if err := os.Remove(filepath.Join(dir, "f.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	err = StashRebase(ctx, dir, sha)
	var conflict *StashConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected StashConflictError, got %v", err)
	}
	if len(conflict.Files) != 1 || conflict.Files[0] != "f.txt" {
		t.Fatalf("conflict files = %v, want [f.txt]", conflict.Files)
	}
	// Stashed content restored for visibility; stash entry kept.
	data, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(data) != "local edit\n" {
		t.Fatalf("restored content = %q", string(data))
	}
	if exists, err := StashEntryExists(ctx, dir, sha); err != nil || !exists {
		t.Fatalf("stash entry must survive a conflict (exists=%v err=%v)", exists, err)
	}
}

func TestStashRebaseRestoresUntrackedExecutableBySHA(t *testing.T) {
	ctx := context.Background()
	dir := initGitRepo(t)
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-m", "base")

	writeFile(t, filepath.Join(dir, "tool.sh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "tool.sh"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	sha, err := StashPush(ctx, dir, "test", true, []string{"tool.sh"})
	if err != nil {
		t.Fatalf("StashPush: %v", err)
	}

	// An unrelated stash shifts positional refs; the SHA must still resolve.
	writeFile(t, filepath.Join(dir, "f.txt"), "other\n")
	if _, err := StashPush(ctx, dir, "other", true, []string{"f.txt"}); err != nil {
		t.Fatalf("second StashPush: %v", err)
	}

	if err := StashRebase(ctx, dir, sha); err != nil {
		t.Fatalf("StashRebase: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "tool.sh"))
	if err != nil {
		t.Fatalf("stat restored file: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Fatalf("restored file lost the executable bit: %v", info.Mode())
	}
	if exists, err := StashEntryExists(ctx, dir, sha); err != nil || exists {
		t.Fatalf("rebased stash should be dropped (exists=%v err=%v)", exists, err)
	}
}

func TestStashRebaseReportsMissingEntry(t *testing.T) {
	ctx := context.Background()
	dir := initGitRepo(t)
	writeFile(t, filepath.Join(dir, "f.txt"), "base\n")
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-m", "base")

	err := StashRebase(ctx, dir, "0123456789abcdef0123456789abcdef01234567")
	if !errors.Is(err, ErrStashNotFound) {
		t.Fatalf("expected ErrStashNotFound, got %v", err)
	}
}

func TestRunReturnsContextError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := []byte("[alias]\n\thold = !sleep 5\n")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), config, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if _, err := Run(ctx, t.TempDir(), nil, "hold"); err == nil {
		t.Fatalf("expected timeout error")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("expected context deadline exceeded, got %v", ctx.Err())
	}
}
