package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/git"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/repo"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/workspace"
)

type SyncOptions struct {
	Workspace workspace.Entry
	Repo      *repo.Info
	Remote    string
	Rebase    bool
	Progress  Progress
}

type SyncResult struct {
	Workspace          string   `json:"workspace"`
	Remote             string   `json:"remote"`
	RepoHead           string   `json:"repo_head"`
	StashRef           string   `json:"stash_ref,omitempty"`
	Rebased            bool     `json:"rebased"`
	Fallback           bool     `json:"fallback"`
	StashRestored      bool     `json:"stash_restored,omitempty"`
	StashConflict      bool     `json:"stash_conflict,omitempty"`
	StashConflictFiles []string `json:"stash_conflict_files,omitempty"`
	Applied            []string `json:"applied,omitempty"`
	Conflicts          []string `json:"conflicts,omitempty"`
}

func Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error) {
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	reportProgress(opts.Progress, "Checking patch repo status")
	dirty, err := git.IsDirty(ctx, opts.Repo.Root)
	if err != nil {
		return nil, err
	}
	if dirty {
		return nil, fmt.Errorf("patches repo has uncommitted changes; commit or stash them before syncing")
	}
	branch, err := git.CurrentBranch(ctx, opts.Repo.Root)
	if err != nil {
		return nil, err
	}
	reportProgress(opts.Progress, "Pulling patch repo from %s/%s", opts.Remote, branch)
	if err := git.PullRebase(ctx, opts.Repo.Root, opts.Remote, branch); err != nil {
		return nil, err
	}
	head, err := git.HeadRev(ctx, opts.Repo.Root)
	if err != nil {
		return nil, err
	}
	state, err := workspace.LoadState(opts.Workspace.Path)
	if err != nil {
		return nil, err
	}
	result := &SyncResult{
		Workspace: opts.Workspace.Name,
		Remote:    opts.Remote,
		RepoHead:  head,
		Rebased:   opts.Rebase,
	}
	// A previous run may have parked local changes (--no-rebase or a crash).
	// Bring them back before measuring divergence so they ride this sync like
	// any other local change; stale records are cleared.
	if state.PendingStash != "" {
		if opts.Rebase {
			reportProgress(opts.Progress, "Restoring previously parked local changes")
			switch err := git.StashRebase(ctx, opts.Workspace.Path, state.PendingStash); {
			case err == nil, errors.Is(err, git.ErrStashNotFound):
				state.PendingStash = ""
				if err := workspace.SaveState(opts.Workspace.Path, state); err != nil {
					return nil, err
				}
			default:
				var conflict *git.StashConflictError
				if !errors.As(err, &conflict) {
					return nil, err
				}
				result.StashConflict = true
				result.StashConflictFiles = conflict.Files
				return result, nil
			}
		} else {
			live, err := git.StashEntryExists(ctx, opts.Workspace.Path, state.PendingStash)
			if err != nil {
				return nil, err
			}
			if !live {
				state.PendingStash = ""
				if err := workspace.SaveState(opts.Workspace.Path, state); err != nil {
					return nil, err
				}
			}
		}
	}
	status, err := InspectWorkspace(ctx, InspectWorkspaceOptions{
		Workspace: opts.Workspace,
		Repo:      opts.Repo,
		Progress:  opts.Progress,
	})
	if err != nil {
		return nil, err
	}
	divergent := append([]string{}, status.NeedsUpdate...)
	divergent = append(divergent, status.Orphaned...)
	if !opts.Rebase && state.PendingStash != "" && len(divergent) > 0 {
		return nil, fmt.Errorf(
			"a previous sync already parked local changes (stash %s); run \"browseros-patch sync %s\" without --no-rebase to restore them first, or pop the stash manually",
			state.PendingStash, opts.Workspace.Name)
	}
	if len(divergent) > 0 {
		reportProgress(opts.Progress, "Stashing %d divergent %s", len(divergent), plural(len(divergent), "file", "files"))
		stashRef, err := git.StashPush(ctx, opts.Workspace.Path, "browseros-patch sync stash", true, divergent)
		if err != nil {
			return nil, err
		}
		result.StashRef = stashRef
		state.PendingStash = stashRef
		if err := workspace.SaveState(opts.Workspace.Path, state); err != nil {
			return nil, err
		}
	}
	if state.LastSyncRev == "" || state.BaseCommit != "" && state.BaseCommit != opts.Repo.BaseCommit {
		result.Fallback = true
		applyResult, err := Apply(ctx, ApplyOptions{
			Workspace:           opts.Workspace,
			Repo:                opts.Repo,
			Reset:               true,
			Mode:                "sync-reset",
			RestorePendingStash: opts.Rebase,
			Progress:            opts.Progress,
		})
		if err != nil {
			return nil, err
		}
		result.Applied = applyResult.Applied
		if len(applyResult.Conflicts) > 0 {
			for _, conflict := range applyResult.Conflicts {
				result.Conflicts = append(result.Conflicts, conflict.ChromiumPath)
			}
			return result, nil
		}
	} else {
		applyResult, err := Apply(ctx, ApplyOptions{
			Workspace:           opts.Workspace,
			Repo:                opts.Repo,
			ChangedRef:          state.LastSyncRev,
			RangeEnd:            head,
			Mode:                "sync",
			RestorePendingStash: opts.Rebase,
			Progress:            opts.Progress,
		})
		if err != nil {
			return nil, err
		}
		result.Applied = applyResult.Applied
		if len(applyResult.Conflicts) > 0 {
			for _, conflict := range applyResult.Conflicts {
				result.Conflicts = append(result.Conflicts, conflict.ChromiumPath)
			}
			return result, nil
		}
	}
	if opts.Rebase && result.StashRef != "" {
		reportProgress(opts.Progress, "Rebasing stashed local changes")
		if err := git.StashRebase(ctx, opts.Workspace.Path, result.StashRef); err != nil {
			var conflict *git.StashConflictError
			if !errors.As(err, &conflict) {
				return nil, err
			}
			result.StashConflict = true
			result.StashConflictFiles = conflict.Files
		} else {
			result.StashRestored = true
		}
	}
	switch {
	case result.StashConflict:
		// Keep PendingStash: git preserved the stash entry and the user
		// still has to resolve the rebase conflict.
	case !opts.Rebase && result.StashRef != "":
		// Local changes stay parked; remember where they are instead of
		// silently forgetting the stash.
		state.PendingStash = result.StashRef
	case !opts.Rebase:
		// Keep any live pre-existing record (validated above).
	default:
		state.PendingStash = ""
	}
	state.BaseCommit = opts.Repo.BaseCommit
	state.LastSyncRev = head
	state.LastSyncAt = time.Now().UTC()
	if err := workspace.SaveState(opts.Workspace.Path, state); err != nil {
		return nil, err
	}
	return result, nil
}
