package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/git"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/patch"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/repo"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/resolve"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/workspace"
)

func TestAbortRevertsAppliedOpsAndRestoresPendingStash(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "a.txt"), "a\n")
	writeFile(t, filepath.Join(workspacePath, "b.txt"), "b\n")
	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local\n")
	runGit(t, workspacePath, "add", "a.txt", "b.txt", "local.txt")
	runGit(t, workspacePath, "commit", "-m", "base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local changed\n")
	runGit(t, workspacePath, "stash", "push", "-m", "test stash", "-u", "--", "local.txt")
	stashRef := gitOutput(t, workspacePath, "stash", "list", "-1", "--format=%gd")
	if stashRef == "" {
		t.Fatalf("expected stash ref")
	}

	if err := workspace.SaveState(workspacePath, &workspace.State{
		Version:      1,
		Workspace:    workspacePath,
		BaseCommit:   baseCommit,
		PendingStash: stashRef,
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	writeFile(t, filepath.Join(workspacePath, "a.txt"), "applied\n")
	writeFile(t, filepath.Join(workspacePath, "b.txt"), "conflict\n")
	if err := resolve.Save(workspacePath, &resolve.State{
		Workspace:  workspacePath,
		RepoRoot:   workspacePath,
		BaseCommit: baseCommit,
		Current:    1,
		Operations: []resolve.Operation{
			{ChromiumPath: "a.txt", PatchRel: "a.txt", Op: patch.OpModify},
			{ChromiumPath: "b.txt", PatchRel: "b.txt", Op: patch.OpModify},
		},
	}); err != nil {
		t.Fatalf("resolve.Save: %v", err)
	}

	if err := Abort(ctx, workspace.Entry{Name: "ws", Path: workspacePath}); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	assertFile(t, filepath.Join(workspacePath, "a.txt"), "a\n")
	assertFile(t, filepath.Join(workspacePath, "b.txt"), "b\n")
	assertFile(t, filepath.Join(workspacePath, "local.txt"), "local changed\n")
	if resolve.Exists(workspacePath) {
		t.Fatalf("expected resolve state to be removed")
	}
	state, err := workspace.LoadState(workspacePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash != "" {
		t.Fatalf("expected pending stash cleared, got %q", state.PendingStash)
	}
}

func TestPublishReturnsHelpfulErrorWhenNothingChanged(t *testing.T) {
	ctx := context.Background()
	repoRoot := initGitRepo(t)
	writeFile(t, filepath.Join(repoRoot, "BASE_COMMIT"), "base123\n")
	writeFile(t, filepath.Join(repoRoot, "chromium_patches", ".gitkeep"), "")
	runGit(t, repoRoot, "add", "BASE_COMMIT", "chromium_patches/.gitkeep")
	runGit(t, repoRoot, "commit", "-m", "repo init")

	repoInfo, err := repo.Load(repoRoot)
	if err != nil {
		t.Fatalf("repo.Load: %v", err)
	}
	if _, err := Publish(ctx, PublishOptions{Repo: repoInfo}); err == nil || !strings.Contains(err.Error(), "nothing to publish") {
		t.Fatalf("expected helpful no-op error, got %v", err)
	}
}

func TestOperationsFromChangesNormalizesOldPath(t *testing.T) {
	ops := operationsFromChanges(nil, []git.FileChange{{
		Status:  "R",
		Path:    "chromium_patches/chrome/new.cc",
		OldPath: "chromium_patches/chrome/old.cc",
	}}, nil)

	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(ops))
	}
	if ops[0].ChromiumPath != "chrome/new.cc" {
		t.Fatalf("unexpected chromium path: %q", ops[0].ChromiumPath)
	}
	if ops[0].OldPath != "chrome/old.cc" {
		t.Fatalf("unexpected old path: %q", ops[0].OldPath)
	}
}

func TestApplyReportsPatchProgress(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "base\n")
	runGit(t, workspacePath, "add", "chrome/browser.cc")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "patched\n")
	diff, err := git.DiffText(ctx, workspacePath, baseCommit, "--", "chrome/browser.cc")
	if err != nil {
		t.Fatalf("DiffText: %v", err)
	}
	runGit(t, workspacePath, "checkout", "--", "chrome/browser.cc")

	repoRoot := initGitRepo(t)
	writeFile(t, filepath.Join(repoRoot, "BASE_COMMIT"), baseCommit+"\n")
	writeFile(t, filepath.Join(repoRoot, "chromium_patches", "chrome", "browser.cc"), diff)
	runGit(t, repoRoot, "add", "BASE_COMMIT", "chromium_patches/chrome/browser.cc")
	runGit(t, repoRoot, "commit", "-m", "patch repo init")
	repoInfo, err := repo.Load(repoRoot)
	if err != nil {
		t.Fatalf("repo.Load: %v", err)
	}

	progress := &progressRecorder{}
	_, err = Apply(ctx, ApplyOptions{
		Workspace: workspace.Entry{Name: "ws", Path: workspacePath},
		Repo:      repoInfo,
		Progress:  progress,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	progress.requireContains(t, "Inspecting workspace changes")
	progress.requireContains(t, "Applying 1 patch operation")
	progress.requireContains(t, "Applying 1/1 chrome/browser.cc")
	assertFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "patched\n")
}

func TestInspectWorkspaceSkipsIgnoredUntrackedFiles(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "base\n")
	runGit(t, workspacePath, "add", "chrome/browser.cc")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(workspacePath, ".llm", "scratch.md"), "junk\n")
	writeFile(t, filepath.Join(workspacePath, "debug.log"), "junk\n")
	writeFile(t, filepath.Join(workspacePath, "chrome", "feature.cc"), "real\n")

	repoRoot := initGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoRoot, "chromium_patches"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(repoRoot, "BASE_COMMIT"), baseCommit+"\n")
	runGit(t, repoRoot, "add", "BASE_COMMIT")
	runGit(t, repoRoot, "commit", "-m", "patch repo init")
	repoInfo, err := repo.Load(repoRoot)
	if err != nil {
		t.Fatalf("repo.Load: %v", err)
	}

	status, err := InspectWorkspace(ctx, InspectWorkspaceOptions{
		Workspace: workspace.Entry{Name: "ws", Path: workspacePath},
		Repo:      repoInfo,
	})
	if err != nil {
		t.Fatalf("InspectWorkspace: %v", err)
	}
	if !slices.Contains(status.Orphaned, "chrome/feature.cc") {
		t.Fatalf("expected real untracked file as orphan, got %v", status.Orphaned)
	}
	for _, junk := range []string{".llm/scratch.md", "debug.log"} {
		if slices.Contains(status.Orphaned, junk) {
			t.Fatalf("expected %q to be ignored, got orphans %v", junk, status.Orphaned)
		}
	}
}

// newPatchRepo builds a minimal committed patch repo pointing at baseCommit.
func newPatchRepo(t *testing.T, baseCommit string) *repo.Info {
	t.Helper()
	repoRoot := initGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoRoot, "chromium_patches"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(repoRoot, "BASE_COMMIT"), baseCommit+"\n")
	runGit(t, repoRoot, "add", "BASE_COMMIT")
	runGit(t, repoRoot, "commit", "-m", "patch repo init")
	repoInfo, err := repo.Load(repoRoot)
	if err != nil {
		t.Fatalf("repo.Load: %v", err)
	}
	return repoInfo
}

func TestExtractRoundTripIsChurnFree(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "base\nline\n")
	runGit(t, workspacePath, "add", "chrome/browser.cc")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "patched\nline\n")
	writeFile(t, filepath.Join(workspacePath, "chrome", "feature.cc"), "new feature\n")

	repoInfo := newPatchRepo(t, baseCommit)
	ws := workspace.Entry{Name: "ws", Path: workspacePath}

	first, err := Extract(ctx, ExtractOptions{Workspace: ws, Repo: repoInfo})
	if err != nil {
		t.Fatalf("first Extract: %v", err)
	}
	if len(first.Written) != 2 {
		t.Fatalf("expected 2 files written, got %v", first.Written)
	}

	// After extract, status must agree the workspace is fully captured.
	status, err := InspectWorkspace(ctx, InspectWorkspaceOptions{Workspace: ws, Repo: repoInfo})
	if err != nil {
		t.Fatalf("InspectWorkspace: %v", err)
	}
	if len(status.NeedsUpdate) != 0 || len(status.NeedsApply) != 0 || len(status.Orphaned) != 0 {
		t.Fatalf("expected clean status after extract, got needs_update=%v needs_apply=%v orphaned=%v",
			status.NeedsUpdate, status.NeedsApply, status.Orphaned)
	}

	beforeBytes := map[string]string{}
	for _, rel := range first.Written {
		data, err := os.ReadFile(filepath.Join(repoInfo.PatchesDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read patch %s: %v", rel, err)
		}
		beforeBytes[rel] = string(data)
	}

	second, err := Extract(ctx, ExtractOptions{Workspace: ws, Repo: repoInfo})
	if err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	if len(second.Written) != 0 || len(second.Deleted) != 0 {
		t.Fatalf("second extract must be a no-op, wrote %v deleted %v", second.Written, second.Deleted)
	}
	if len(second.Unchanged) != 2 {
		t.Fatalf("expected both files unchanged, got %v", second.Unchanged)
	}
	for rel, before := range beforeBytes {
		data, err := os.ReadFile(filepath.Join(repoInfo.PatchesDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read patch %s: %v", rel, err)
		}
		if string(data) != before {
			t.Fatalf("patch %s churned between identical extracts", rel)
		}
	}
}

func TestExtractFromTwoCheckoutsIsByteIdentical(t *testing.T) {
	ctx := context.Background()
	checkout1 := initGitRepo(t)
	writeFile(t, filepath.Join(checkout1, "chrome", "browser.cc"), "base\nline\n")
	runGit(t, checkout1, "add", "chrome/browser.cc")
	runGit(t, checkout1, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, checkout1, "rev-parse", "HEAD")

	checkout2Parent := t.TempDir()
	runGit(t, checkout2Parent, "clone", checkout1, "clone")
	checkout2 := filepath.Join(checkout2Parent, "clone")
	// Hostile per-checkout config must not leak into extracted patches.
	runGit(t, checkout2, "config", "core.abbrev", "9")
	runGit(t, checkout2, "config", "diff.algorithm", "histogram")
	runGit(t, checkout2, "config", "diff.mnemonicPrefix", "true")

	edit := "patched\nline\n"
	addition := "new feature\n"
	for _, checkout := range []string{checkout1, checkout2} {
		writeFile(t, filepath.Join(checkout, "chrome", "browser.cc"), edit)
		writeFile(t, filepath.Join(checkout, "chrome", "feature.cc"), addition)
	}

	repo1 := newPatchRepo(t, baseCommit)
	repo2 := newPatchRepo(t, baseCommit)
	if _, err := Extract(ctx, ExtractOptions{Workspace: workspace.Entry{Name: "c1", Path: checkout1}, Repo: repo1}); err != nil {
		t.Fatalf("extract checkout1: %v", err)
	}
	if _, err := Extract(ctx, ExtractOptions{Workspace: workspace.Entry{Name: "c2", Path: checkout2}, Repo: repo2}); err != nil {
		t.Fatalf("extract checkout2: %v", err)
	}

	for _, rel := range []string{"chrome/browser.cc", "chrome/feature.cc"} {
		data1, err := os.ReadFile(filepath.Join(repo1.PatchesDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read repo1 %s: %v", rel, err)
		}
		data2, err := os.ReadFile(filepath.Join(repo2.PatchesDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read repo2 %s: %v", rel, err)
		}
		if string(data1) != string(data2) {
			t.Fatalf("patch %s differs across checkouts\n--- c1 ---\n%s\n--- c2 ---\n%s", rel, data1, data2)
		}
	}
}

func TestExtractReportsUntrackedScanProgress(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "base\n")
	runGit(t, workspacePath, "add", "chrome/browser.cc")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(workspacePath, "chrome", "one.cc"), "one\n")
	writeFile(t, filepath.Join(workspacePath, "chrome", "two.cc"), "two\n")

	repoInfo := newPatchRepo(t, baseCommit)
	progress := &progressRecorder{}
	if _, err := Extract(ctx, ExtractOptions{
		Workspace: workspace.Entry{Name: "ws", Path: workspacePath},
		Repo:      repoInfo,
		Progress:  progress,
	}); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	progress.requireContains(t, "Scanning untracked 1/2")
	progress.requireContains(t, "Scanning untracked 2/2")
	progress.requireContains(t, "Writing 2 patch files")
}

func TestExtractDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "base\n")
	runGit(t, workspacePath, "add", "chrome/browser.cc")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "patched\n")

	repoInfo := newPatchRepo(t, baseCommit)
	ws := workspace.Entry{Name: "ws", Path: workspacePath}

	result, err := Extract(ctx, ExtractOptions{Workspace: ws, Repo: repoInfo, DryRun: true})
	if err != nil {
		t.Fatalf("Extract dry-run: %v", err)
	}
	if !result.DryRun {
		t.Fatalf("expected dry_run result flag")
	}
	if len(result.Created) != 1 || result.Created[0] != "chrome/browser.cc" {
		t.Fatalf("expected planned create, got %+v", result)
	}
	if _, err := os.Stat(filepath.Join(repoInfo.PatchesDir, "chrome", "browser.cc")); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not write patch files")
	}
	state, err := workspace.LoadState(workspacePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.LastExtractRev != "" {
		t.Fatalf("dry-run must not record extract state, got %q", state.LastExtractRev)
	}
}

func TestExtractExcludesFilterUntracked(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "base\n")
	runGit(t, workspacePath, "add", "chrome/browser.cc")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(workspacePath, "scratch", "notes.md"), "junk\n")
	writeFile(t, filepath.Join(workspacePath, "chrome", "feature.cc"), "real\n")

	repoInfo := newPatchRepo(t, baseCommit)
	result, err := Extract(ctx, ExtractOptions{
		Workspace: workspace.Entry{Name: "ws", Path: workspacePath},
		Repo:      repoInfo,
		Excludes:  []string{"scratch/"},
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if slices.Contains(result.Written, "scratch/notes.md") {
		t.Fatalf("excluded path extracted anyway: %v", result.Written)
	}
	if !slices.Contains(result.Written, "chrome/feature.cc") {
		t.Fatalf("expected real file extracted, got %v", result.Written)
	}
}

// syncFixture builds a workspace with one patched file and one local-only
// change, plus a patch repo (with bare remote) whose patch rewrites a.txt.
func syncFixture(t *testing.T, patchedLine string, localLine string) (workspace.Entry, *repo.Info) {
	t.Helper()
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "a.txt"), "line1\nline2\nline3\n")
	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local base\n")
	runGit(t, workspacePath, "add", "a.txt", "local.txt")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	// Build the repo patch for a.txt from a temporary edit.
	writeFile(t, filepath.Join(workspacePath, "a.txt"), "line1\n"+patchedLine+"\nline3\n")
	diff, err := git.DiffText(ctx, workspacePath, baseCommit, "--", "a.txt")
	if err != nil {
		t.Fatalf("DiffText: %v", err)
	}
	runGit(t, workspacePath, "checkout", "--", "a.txt")

	repoInfo := newPatchRepo(t, baseCommit)
	writeFile(t, filepath.Join(repoInfo.PatchesDir, "a.txt"), diff)
	runGit(t, repoInfo.Root, "add", "chromium_patches/a.txt")
	runGit(t, repoInfo.Root, "commit", "-m", "add a.txt patch")
	remoteRepo := t.TempDir()
	runGit(t, remoteRepo, "init", "--bare")
	runGit(t, repoInfo.Root, "remote", "add", "origin", remoteRepo)
	runGit(t, repoInfo.Root, "push", "-u", "origin", "HEAD")

	// Local divergence in the workspace.
	writeFile(t, filepath.Join(workspacePath, "local.txt"), localLine+"\n")
	return workspace.Entry{Name: "ws", Path: workspacePath}, repoInfo
}

func TestSyncRebaseRestoresLocalChanges(t *testing.T) {
	ctx := context.Background()
	ws, repoInfo := syncFixture(t, "PATCHED", "local change")

	result, err := Sync(ctx, SyncOptions{Workspace: ws, Repo: repoInfo, Rebase: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", result.Conflicts)
	}
	if !result.StashRestored {
		t.Fatalf("expected stash restored, got %+v", result)
	}
	assertFile(t, filepath.Join(ws.Path, "a.txt"), "line1\nPATCHED\nline3\n")
	assertFile(t, filepath.Join(ws.Path, "local.txt"), "local change\n")
	state, err := workspace.LoadState(ws.Path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash != "" {
		t.Fatalf("expected no pending stash, got %q", state.PendingStash)
	}
}

func TestSyncNoRebaseKeepsStashRecorded(t *testing.T) {
	ctx := context.Background()
	ws, repoInfo := syncFixture(t, "PATCHED", "local change")

	result, err := Sync(ctx, SyncOptions{Workspace: ws, Repo: repoInfo, Rebase: false})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.StashRef == "" {
		t.Fatalf("expected stash to be created")
	}
	if result.StashRestored {
		t.Fatalf("no-rebase must not pop the stash")
	}
	state, err := workspace.LoadState(ws.Path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash != result.StashRef {
		t.Fatalf("pending stash = %q, want %q (must stay recorded)", state.PendingStash, result.StashRef)
	}
	if stashList := gitOutput(t, ws.Path, "stash", "list"); stashList == "" {
		t.Fatalf("stash entry should still exist")
	}
}

func TestAbortRestoresSyncParkedStashBySHA(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "a.txt"), "a\n")
	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local\n")
	runGit(t, workspacePath, "add", "a.txt", "local.txt")
	runGit(t, workspacePath, "commit", "-m", "base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	// Park a local change exactly the way sync does: recorded by commit SHA.
	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local changed\n")
	sha, err := git.StashPush(ctx, workspacePath, "sync stash", true, []string{"local.txt"})
	if err != nil {
		t.Fatalf("StashPush: %v", err)
	}
	if err := workspace.SaveState(workspacePath, &workspace.State{
		Version:      1,
		Workspace:    workspacePath,
		BaseCommit:   baseCommit,
		PendingStash: sha,
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	writeFile(t, filepath.Join(workspacePath, "a.txt"), "applied\n")
	if err := resolve.Save(workspacePath, &resolve.State{
		Workspace:  workspacePath,
		RepoRoot:   workspacePath,
		BaseCommit: baseCommit,
		Current:    0,
		Operations: []resolve.Operation{
			{ChromiumPath: "a.txt", PatchRel: "a.txt", Op: patch.OpModify},
		},
	}); err != nil {
		t.Fatalf("resolve.Save: %v", err)
	}

	if err := Abort(ctx, workspace.Entry{Name: "ws", Path: workspacePath}); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	assertFile(t, filepath.Join(workspacePath, "a.txt"), "a\n")
	assertFile(t, filepath.Join(workspacePath, "local.txt"), "local changed\n")
	state, err := workspace.LoadState(workspacePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash != "" {
		t.Fatalf("expected pending stash cleared, got %q", state.PendingStash)
	}
}

func TestSyncRefusesToDoubleParkLocalChanges(t *testing.T) {
	ctx := context.Background()
	ws, repoInfo := syncFixture(t, "PATCHED", "local change")

	first, err := Sync(ctx, SyncOptions{Workspace: ws, Repo: repoInfo, Rebase: false})
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if first.StashRef == "" {
		t.Fatalf("expected a parked stash")
	}

	// New divergence while changes are still parked.
	writeFile(t, filepath.Join(ws.Path, "local.txt"), "second change\n")
	_, err = Sync(ctx, SyncOptions{Workspace: ws, Repo: repoInfo, Rebase: false})
	if err == nil || !strings.Contains(err.Error(), "already parked") {
		t.Fatalf("expected double-park refusal, got %v", err)
	}
	// The original stash record must be intact.
	state, err := workspace.LoadState(ws.Path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash != first.StashRef {
		t.Fatalf("pending stash = %q, want %q", state.PendingStash, first.StashRef)
	}
}

func TestSyncRestoresPreviouslyParkedStash(t *testing.T) {
	ctx := context.Background()
	ws, repoInfo := syncFixture(t, "PATCHED", "local change")

	// First sync parks the local change (--no-rebase).
	first, err := Sync(ctx, SyncOptions{Workspace: ws, Repo: repoInfo, Rebase: false})
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if first.StashRef == "" {
		t.Fatalf("expected a parked stash")
	}
	assertFile(t, filepath.Join(ws.Path, "local.txt"), "local base\n")

	// Second sync (rebase default) must bring the parked change back.
	second, err := Sync(ctx, SyncOptions{Workspace: ws, Repo: repoInfo, Rebase: true})
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if len(second.Conflicts) != 0 || second.StashConflict {
		t.Fatalf("unexpected conflicts: %+v", second)
	}
	assertFile(t, filepath.Join(ws.Path, "local.txt"), "local change\n")
	state, err := workspace.LoadState(ws.Path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash != "" {
		t.Fatalf("pending stash should be cleared after restore, got %q", state.PendingStash)
	}
	if stashList := gitOutput(t, ws.Path, "stash", "list"); stashList != "" {
		t.Fatalf("stash should be dropped after restore, got %q", stashList)
	}
}

func TestSyncReportsStashPopConflict(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "a.txt"), "line1\nline2\nline3\n")
	runGit(t, workspacePath, "add", "a.txt")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(workspacePath, "a.txt"), "line1\nPATCHED\nline3\n")
	diff, err := git.DiffText(ctx, workspacePath, baseCommit, "--", "a.txt")
	if err != nil {
		t.Fatalf("DiffText: %v", err)
	}
	runGit(t, workspacePath, "checkout", "--", "a.txt")

	repoInfo := newPatchRepo(t, baseCommit)
	writeFile(t, filepath.Join(repoInfo.PatchesDir, "a.txt"), diff)
	runGit(t, repoInfo.Root, "add", "chromium_patches/a.txt")
	runGit(t, repoInfo.Root, "commit", "-m", "add a.txt patch")
	remoteRepo := t.TempDir()
	runGit(t, remoteRepo, "init", "--bare")
	runGit(t, repoInfo.Root, "remote", "add", "origin", remoteRepo)
	runGit(t, repoInfo.Root, "push", "-u", "origin", "HEAD")

	// Local edit to the same line the patch rewrites -> stash pop conflict.
	writeFile(t, filepath.Join(workspacePath, "a.txt"), "line1\nLOCAL\nline3\n")

	ws := workspace.Entry{Name: "ws", Path: workspacePath}
	result, err := Sync(ctx, SyncOptions{Workspace: ws, Repo: repoInfo, Rebase: true})
	if err != nil {
		t.Fatalf("Sync should report stash conflicts, not fail: %v", err)
	}
	if !result.StashConflict {
		t.Fatalf("expected stash conflict, got %+v", result)
	}
	if !slices.Contains(result.StashConflictFiles, "a.txt") {
		t.Fatalf("expected a.txt in conflict files, got %v", result.StashConflictFiles)
	}
	state, err := workspace.LoadState(ws.Path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash == "" {
		t.Fatalf("pending stash must stay recorded after pop conflict")
	}
	if stashList := gitOutput(t, ws.Path, "stash", "list"); stashList == "" {
		t.Fatalf("stash entry must survive a pop conflict")
	}
	merged, err := os.ReadFile(filepath.Join(ws.Path, "a.txt"))
	if err != nil {
		t.Fatalf("read merged file: %v", err)
	}
	for _, marker := range []string{"<<<<<<<", "PATCHED", "LOCAL", ">>>>>>>"} {
		if !strings.Contains(string(merged), marker) {
			t.Fatalf("expected 3-way conflict markers with both sides, got:\n%s", merged)
		}
	}
}

func TestContinueRestoresPendingStashAfterLastConflict(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "b.txt"), "base\n")
	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local base\n")
	runGit(t, workspacePath, "add", "b.txt", "local.txt")
	runGit(t, workspacePath, "commit", "-m", "base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(workspacePath, "b.txt"), "patched\n")
	diff, err := git.DiffText(ctx, workspacePath, baseCommit, "--", "b.txt")
	if err != nil {
		t.Fatalf("DiffText: %v", err)
	}

	repoInfo := newPatchRepo(t, baseCommit)
	writeFile(t, filepath.Join(repoInfo.PatchesDir, "b.txt"), diff)
	runGit(t, repoInfo.Root, "add", "chromium_patches/b.txt")
	runGit(t, repoInfo.Root, "commit", "-m", "add b.txt patch")

	// Park a local change in a stash, recorded as pending (as sync does).
	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local changed\n")
	runGit(t, workspacePath, "stash", "push", "-m", "sync stash", "-u", "--", "local.txt")
	stashRef := gitOutput(t, workspacePath, "stash", "list", "-1", "--format=%gd")
	if err := workspace.SaveState(workspacePath, &workspace.State{
		Version:      1,
		Workspace:    workspacePath,
		BaseCommit:   baseCommit,
		PendingStash: stashRef,
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// The conflicted operation is already resolved in the working tree; the
	// pause came from a rebase-mode sync, so restore intent is recorded.
	if err := resolve.Save(workspacePath, &resolve.State{
		Workspace:           workspacePath,
		RepoRoot:            repoInfo.Root,
		BaseCommit:          baseCommit,
		Current:             0,
		Operations:          []resolve.Operation{{ChromiumPath: "b.txt", PatchRel: "b.txt", Op: patch.OpModify}},
		RestorePendingStash: true,
	}); err != nil {
		t.Fatalf("resolve.Save: %v", err)
	}

	result, err := Continue(ctx, ContinueOptions{Workspace: workspace.Entry{Name: "ws", Path: workspacePath}})
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if !result.StashRestored {
		t.Fatalf("expected pending stash restored on completion, got %+v", result)
	}
	assertFile(t, filepath.Join(workspacePath, "local.txt"), "local changed\n")
	state, err := workspace.LoadState(workspacePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash != "" {
		t.Fatalf("pending stash should be cleared, got %q", state.PendingStash)
	}
}

func TestContinueLeavesExplicitlyParkedStashAlone(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "b.txt"), "base\n")
	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local base\n")
	runGit(t, workspacePath, "add", "b.txt", "local.txt")
	runGit(t, workspacePath, "commit", "-m", "base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(workspacePath, "b.txt"), "patched\n")
	diff, err := git.DiffText(ctx, workspacePath, baseCommit, "--", "b.txt")
	if err != nil {
		t.Fatalf("DiffText: %v", err)
	}
	repoInfo := newPatchRepo(t, baseCommit)
	writeFile(t, filepath.Join(repoInfo.PatchesDir, "b.txt"), diff)
	runGit(t, repoInfo.Root, "add", "chromium_patches/b.txt")
	runGit(t, repoInfo.Root, "commit", "-m", "add b.txt patch")

	// Stash parked by an explicit --no-rebase sync: no restore intent.
	writeFile(t, filepath.Join(workspacePath, "local.txt"), "local changed\n")
	sha, err := git.StashPush(ctx, workspacePath, "sync stash", true, []string{"local.txt"})
	if err != nil {
		t.Fatalf("StashPush: %v", err)
	}
	if err := workspace.SaveState(workspacePath, &workspace.State{
		Version:      1,
		Workspace:    workspacePath,
		BaseCommit:   baseCommit,
		PendingStash: sha,
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := resolve.Save(workspacePath, &resolve.State{
		Workspace:  workspacePath,
		RepoRoot:   repoInfo.Root,
		BaseCommit: baseCommit,
		Current:    0,
		Operations: []resolve.Operation{{ChromiumPath: "b.txt", PatchRel: "b.txt", Op: patch.OpModify}},
	}); err != nil {
		t.Fatalf("resolve.Save: %v", err)
	}

	result, err := Continue(ctx, ContinueOptions{Workspace: workspace.Entry{Name: "ws", Path: workspacePath}})
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if result.StashRestored {
		t.Fatalf("--no-rebase parked stash must stay parked, got %+v", result)
	}
	assertFile(t, filepath.Join(workspacePath, "local.txt"), "local base\n")
	state, err := workspace.LoadState(workspacePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash != sha {
		t.Fatalf("pending stash record must survive, got %q want %q", state.PendingStash, sha)
	}
}

func TestInspectWorkspaceReportsPendingStash(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "base\n")
	runGit(t, workspacePath, "add", "chrome/browser.cc")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")
	repoInfo := newPatchRepo(t, baseCommit)

	if err := workspace.SaveState(workspacePath, &workspace.State{
		Version:      1,
		Workspace:    workspacePath,
		BaseCommit:   baseCommit,
		PendingStash: "stash@{0}",
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	status, err := InspectWorkspace(ctx, InspectWorkspaceOptions{
		Workspace: workspace.Entry{Name: "ws", Path: workspacePath},
		Repo:      repoInfo,
	})
	if err != nil {
		t.Fatalf("InspectWorkspace: %v", err)
	}
	if status.PendingStash != "stash@{0}" {
		t.Fatalf("pending stash = %q, want stash@{0}", status.PendingStash)
	}
}

func TestOrphanSummaryGroupsByTopLevelDir(t *testing.T) {
	groups := OrphanSummary([]string{
		"chrome/app/one.cc",
		"chrome/browser/two.cc",
		"third_party/sparkle/bin",
		"BUILD.gn",
	})
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %v", groups)
	}
	if groups[0].Dir != "chrome" || groups[0].Count != 2 {
		t.Fatalf("expected chrome first with count 2, got %v", groups)
	}
	rest := map[string]int{groups[1].Dir: groups[1].Count, groups[2].Dir: groups[2].Count}
	if rest["third_party"] != 1 || rest["(root)"] != 1 {
		t.Fatalf("unexpected groups: %v", groups)
	}
}

func TestInSyncButUnreproducible(t *testing.T) {
	status := &WorkspaceStatus{Orphaned: []string{"chrome/x"}}
	if !status.InSyncButUnreproducible() {
		t.Fatalf("expected hint condition with only orphans present")
	}
	status.NeedsApply = []string{"chrome/y"}
	if status.InSyncButUnreproducible() {
		t.Fatalf("hint must not fire when applies are pending")
	}
}

func TestSyncClearsPendingStashAfterSuccessfulNonRebaseRun(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "base\n")
	runGit(t, workspacePath, "add", "chrome/browser.cc")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	remoteRepo := t.TempDir()
	runGit(t, remoteRepo, "init", "--bare")

	repoRoot := initGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoRoot, "chromium_patches"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(repoRoot, "BASE_COMMIT"), baseCommit+"\n")
	runGit(t, repoRoot, "add", "BASE_COMMIT")
	runGit(t, repoRoot, "commit", "-m", "patch repo init")
	runGit(t, repoRoot, "remote", "add", "origin", remoteRepo)
	runGit(t, repoRoot, "push", "-u", "origin", "HEAD")
	repoHead := gitOutput(t, repoRoot, "rev-parse", "HEAD")

	repoInfo, err := repo.Load(repoRoot)
	if err != nil {
		t.Fatalf("repo.Load: %v", err)
	}
	if err := workspace.SaveState(workspacePath, &workspace.State{
		Version:      1,
		Workspace:    workspacePath,
		BaseCommit:   baseCommit,
		LastSyncRev:  repoHead,
		PendingStash: "stash@{42}",
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	result, err := Sync(ctx, SyncOptions{
		Workspace: workspace.Entry{Name: "ws", Path: workspacePath},
		Repo:      repoInfo,
		Remote:    "origin",
		Rebase:    false,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.StashRef != "" {
		t.Fatalf("expected no new stash ref, got %q", result.StashRef)
	}

	state, err := workspace.LoadState(workspacePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.PendingStash != "" {
		t.Fatalf("expected pending stash to be cleared, got %q", state.PendingStash)
	}
}

func TestSyncReportsPatchRepoProgress(t *testing.T) {
	ctx := context.Background()
	workspacePath := initGitRepo(t)
	writeFile(t, filepath.Join(workspacePath, "chrome", "browser.cc"), "base\n")
	runGit(t, workspacePath, "add", "chrome/browser.cc")
	runGit(t, workspacePath, "commit", "-m", "workspace base")
	baseCommit := gitOutput(t, workspacePath, "rev-parse", "HEAD")

	remoteRepo := t.TempDir()
	runGit(t, remoteRepo, "init", "--bare")

	repoRoot := initGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoRoot, "chromium_patches"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(repoRoot, "BASE_COMMIT"), baseCommit+"\n")
	runGit(t, repoRoot, "add", "BASE_COMMIT")
	runGit(t, repoRoot, "commit", "-m", "patch repo init")
	runGit(t, repoRoot, "remote", "add", "origin", remoteRepo)
	runGit(t, repoRoot, "push", "-u", "origin", "HEAD")

	repoInfo, err := repo.Load(repoRoot)
	if err != nil {
		t.Fatalf("repo.Load: %v", err)
	}
	progress := &progressRecorder{}
	_, err = Sync(ctx, SyncOptions{
		Workspace: workspace.Entry{Name: "ws", Path: workspacePath},
		Repo:      repoInfo,
		Remote:    "origin",
		Progress:  progress,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	progress.requireContains(t, "Checking patch repo status")
	progress.requireContains(t, "Pulling patch repo from origin/")
	progress.requireContains(t, "Inspecting workspace drift")
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	return dir
}

type progressRecorder struct {
	messages []string
}

func (p *progressRecorder) Step(message string) {
	p.messages = append(p.messages, message)
}

func (p *progressRecorder) requireContains(t *testing.T, want string) {
	t.Helper()
	if slices.ContainsFunc(p.messages, func(message string) bool {
		return strings.Contains(message, want)
	}) {
		return
	}
	t.Fatalf("progress missing %q in %#v", want, p.messages)
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

func writeFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func assertFile(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("unexpected file contents for %s: got %q want %q", path, string(data), want)
	}
}
