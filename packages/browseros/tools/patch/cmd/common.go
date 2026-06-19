package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/engine"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/repo"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/ui"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/workspace"
	"github.com/spf13/cobra"
)

const srcFlagUsage = "Chromium checkout path to operate on directly without registry lookup"

func repoInfo() (*repo.Info, error) {
	return appState.RepoInfo()
}

func resolveWorkspace(cmd *cobra.Command, positional []string, src string) (workspace.Entry, error) {
	name := ""
	if len(positional) > 0 {
		name = positional[0]
	}
	commandPath := ""
	if cmd != nil {
		commandPath = cmd.CommandPath()
	}
	return workspace.ResolveForCommand(appState.Registry, name, appState.CWD, src, commandPath)
}

func splitWorkspaceAndFilters(cmd *cobra.Command, args []string) ([]string, []string) {
	atDash := cmd.ArgsLenAtDash()
	if atDash == -1 {
		return args, nil
	}
	return args[:atDash], args[atDash:]
}

// llmTxtGuide returns a stable plain-text operating guide for coding agents.
func llmTxtGuide() string {
	return `browseros-patch quick reference for coding agents

Terms:
- patch repo: BrowserOS packages/browseros repo containing chromium_patches/ and BASE_COMMIT.
- BASE_COMMIT: the Chromium commit every patch is diffed against (file at the patch repo root).
- Chromium checkout: local Chromium src tree registered with a checkout name like ch1.
- checkout name: registry alias used by commands, for example ch1.
- --src: operate on a Chromium checkout path directly without registry lookup.

Rules:
- Checkout commands work from anywhere when passed a checkout name: browseros-patch diff ch1.
- browseros-patch list reads only registered Chromium checkouts; it does not inspect sync state.
- Use browseros-patch status ch1 or browseros-patch diff ch1 before mutating.
- Mutating commands: browseros-patch sync ch1 (alias: pull), browseros-patch apply ch1, browseros-patch extract ch1.
- Every command accepts --json for machine-readable output and never prompts.

Exit codes:
- 0 = success, nothing pending.
- 1 = error (bad input, git failure).
- 2 = paused on a conflict; read the JSON conflict list, fix, then continue.

Status fields (browseros-patch status ch1 --json):
- needs_apply: patches in the repo not present in the checkout -> run apply or sync.
- needs_update: checkout changes missing from the patch repo -> run extract.
- orphaned: checkout files not represented in the patch set (untracked junk is pre-filtered).
- pending_stash: sync parked local changes in a stash and has not restored them yet.

Daily flow (pull latest patches, keep local work):
1. browseros-patch sync ch1            # pull patch repo, apply, rebase local changes
2. exit 2 with conflicts -> fix the file, browseros-patch continue (or browseros-patch skip / browseros-patch abort)
3. exit 2 with stash_conflict -> resolve the conflict markers, then "git stash drop" in the checkout

Capture flow (turn checkout changes into patches):
1. browseros-patch extract ch1 --dry-run   # preview created/updated/deleted/unchanged
2. browseros-patch extract ch1             # write; unchanged patches are never rewritten
3. browseros-patch publish -m "chore: sync patches"   # commit + push chromium_patches

Chromium upgrade loop (move patches to a new Chromium base):
1. gclient sync the checkout to the new Chromium revision.
2. Update the BASE_COMMIT file in the patch repo to that revision.
3. browseros-patch apply ch1 --json    # exit 2 -> the JSON lists the conflicted file and reject path
4. Fix the conflicted file in the checkout, then browseros-patch continue.
   Repeat until exit 0. Use browseros-patch skip to defer one patch, browseros-patch abort to roll back.
5. Build/verify, then browseros-patch extract ch1 and browseros-patch publish.

Extraction hygiene:
- Untracked junk (.llm/, *.log, *.rej, *.orig, .DS_Store, .browseros-patch/) is ignored automatically.
- Add project-specific patterns to .browseros-patchignore at the patch repo root.
- One-off: browseros-patch extract ch1 --exclude "scratch/".
`
}

func ensureRepoConfigured(override string) error {
	if override == "" && appState.Config.PatchesRepo != "" {
		return nil
	}
	root := override
	if root == "" {
		discovered, err := repo.Discover(appState.CWD)
		if err != nil {
			return fmt.Errorf(`unable to discover patches repo; pass --patches-repo or run from packages/browseros`)
		}
		root = discovered
	}
	info, err := repo.Load(root)
	if err != nil {
		return err
	}
	appState.Config.PatchesRepo = info.Root
	return nil
}

// printStashOutcome reports what happened to a pending sync stash once the
// conflict loop completes.
func printStashOutcome(result *engine.ApplyResult) {
	switch {
	case result.StashRestored:
		fmt.Println(ui.Success("Local changes rebased on top of the new patches."))
	case result.StashConflict:
		fmt.Println(ui.Warning("Local changes conflict with the new patches"))
		for _, file := range result.StashConflictFiles {
			fmt.Printf("  %s\n", file)
		}
		fmt.Println(ui.Hint(`Resolve the conflicted files (markers, or restored content for delete conflicts), then "git stash drop" in the checkout.`))
	}
}

// cliProgress renders engine progress on stderr: a self-overwriting single
// line on a TTY, plain "... message" lines otherwise.
type cliProgress struct {
	w      io.Writer
	tty    bool
	active bool
}

// activeProgress lets renderResult clear a pending ephemeral line before
// printing results (package-global like jsonOut/appState).
var activeProgress *cliProgress

func (p *cliProgress) Step(message string) {
	if p == nil {
		return
	}
	if p.tty {
		fmt.Fprintf(p.w, "\r\x1b[2K%s %s", ui.Muted("..."), message)
		p.active = true
		return
	}
	fmt.Fprintf(p.w, "%s %s\n", ui.Muted("..."), message)
}

// Finish clears the in-place progress line so the next write starts clean.
func (p *cliProgress) Finish() {
	if p == nil || !p.active {
		return
	}
	fmt.Fprint(p.w, "\r\x1b[2K")
	p.active = false
}

func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// commandProgress routes long-running engine updates to stderr in human mode only.
func commandProgress(cmd *cobra.Command) engine.Progress {
	if jsonOut {
		return nil
	}
	stderr := cmd.ErrOrStderr()
	activeProgress = &cliProgress{w: stderr, tty: isTerminal(stderr)}
	return activeProgress
}
