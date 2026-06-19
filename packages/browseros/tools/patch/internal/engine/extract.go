package engine

import (
	"context"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/git"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/patch"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/repo"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/workspace"
)

type ExtractOptions struct {
	Workspace  workspace.Entry
	Repo       *repo.Info
	Commit     string
	RangeStart string
	RangeEnd   string
	Squash     bool
	Base       string
	Filters    []string
	Excludes   []string
	DryRun     bool
	Progress   Progress
}

type ExtractResult struct {
	Workspace  string   `json:"workspace"`
	Mode       string   `json:"mode"`
	BaseCommit string   `json:"base_commit"`
	DryRun     bool     `json:"dry_run,omitempty"`
	Written    []string `json:"written"`
	Created    []string `json:"created"`
	Updated    []string `json:"updated"`
	Unchanged  []string `json:"unchanged"`
	Deleted    []string `json:"deleted"`
}

func Extract(ctx context.Context, opts ExtractOptions) (*ExtractResult, error) {
	base := opts.Base
	if base == "" {
		base = opts.Repo.BaseCommit
	}
	var (
		set   patch.PatchSet
		scope []string
		err   error
		mode  string
	)
	switch {
	case opts.Commit != "":
		mode = "commit"
		reportProgress(opts.Progress, "Extracting patches from commit %s", opts.Commit)
		set, err = patch.BuildCommitPatchSet(ctx, opts.Workspace.Path, opts.Commit, opts.Base, opts.Filters)
		if err == nil {
			if opts.Base != "" {
				changes, err := git.DiffTreeNameStatus(ctx, opts.Workspace.Path, opts.Commit, opts.Filters)
				if err != nil {
					return nil, err
				}
				scope = changedScope(changes)
			} else {
				scope = patch.ScopeFromSet(set)
			}
		}
	case opts.RangeStart != "" && opts.RangeEnd != "":
		mode = "range"
		reportProgress(opts.Progress, "Extracting patches from range %s..%s", opts.RangeStart, opts.RangeEnd)
		set, err = patch.BuildRangePatchSet(ctx, opts.Workspace.Path, opts.RangeStart, opts.RangeEnd, opts.Base, opts.Squash, opts.Filters)
		if err == nil {
			if opts.Base != "" || opts.Squash {
				changes, err := git.DiffNameStatusBetween(ctx, opts.Workspace.Path, opts.RangeStart, opts.RangeEnd, opts.Filters)
				if err != nil {
					return nil, err
				}
				scope = changedScope(changes)
			} else {
				scope = patch.ScopeFromSet(set)
			}
		}
	default:
		mode = "working-tree"
		reportProgress(opts.Progress, "Extracting workspace changes")
		ignore, ignoreErr := patch.LoadIgnoreSet(opts.Repo.Root, opts.Excludes)
		if ignoreErr != nil {
			return nil, ignoreErr
		}
		set, err = patch.BuildWorkingTreePatchSet(ctx, opts.Workspace.Path, patch.WorkingTreeOptions{
			Base:    base,
			Filters: opts.Filters,
			Ignore:  ignore,
			Report:  func(message string) { reportProgress(opts.Progress, "%s", message) },
		})
		if err == nil && len(opts.Filters) > 0 {
			scope = opts.Filters
		}
	}
	if err != nil {
		return nil, err
	}
	if opts.DryRun {
		reportProgress(opts.Progress, "Planning %d patch %s (dry run)", len(set), plural(len(set), "file", "files"))
		plan, err := patch.PlanRepoPatchSet(opts.Repo.PatchesDir, set, scope)
		if err != nil {
			return nil, err
		}
		return extractResult(opts.Workspace.Name, mode, base, plan, true), nil
	}
	reportProgress(opts.Progress, "Writing %d patch %s", len(set), plural(len(set), "file", "files"))
	plan, err := patch.WriteRepoPatchSet(opts.Repo.PatchesDir, set, scope)
	if err != nil {
		return nil, err
	}
	state, err := workspace.LoadState(opts.Workspace.Path)
	if err != nil {
		return nil, err
	}
	head, err := git.HeadRev(ctx, opts.Workspace.Path)
	if err != nil {
		return nil, err
	}
	state.BaseCommit = opts.Repo.BaseCommit
	state.LastExtractRev = head
	state.LastExtractAt = time.Now().UTC()
	if err := workspace.SaveState(opts.Workspace.Path, state); err != nil {
		return nil, err
	}
	return extractResult(opts.Workspace.Name, mode, base, plan, false), nil
}

func extractResult(workspaceName string, mode string, base string, plan *patch.WritePlan, dryRun bool) *ExtractResult {
	return &ExtractResult{
		Workspace:  workspaceName,
		Mode:       mode,
		BaseCommit: base,
		DryRun:     dryRun,
		Written:    orEmpty(plan.Written()),
		Created:    orEmpty(plan.Creates),
		Updated:    orEmpty(plan.Updates),
		Unchanged:  orEmpty(plan.Unchanged),
		Deleted:    orEmpty(plan.Deletes),
	}
}

// orEmpty keeps agent-facing JSON arrays as [] instead of null.
func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func changedScope(changes []git.FileChange) []string {
	scope := make([]string, 0, len(changes))
	for _, change := range changes {
		rel := patch.NormalizeChromiumPath(change.Path)
		if patch.IsInternalPath(rel) {
			continue
		}
		scope = append(scope, rel)
	}
	return scope
}
