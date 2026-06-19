package cmd

import (
	"fmt"
	"strconv"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/engine"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/ui"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/workspace"
	"github.com/spf13/cobra"
)

// syncStateHints turns the terse sync_state enum into a one-line explanation
// for humans; JSON values stay untouched for agents.
var syncStateHints = map[string]string{
	"never-synced":  "registered, but no sync has been recorded by this tool yet — apply/extract may still have happened (see last apply/extract)",
	"needs-sync":    "the patch repo moved since the last sync of this checkout",
	"drifted":       "the patch repo has patches this checkout is missing",
	"local-changes": "this checkout has changes the patch repo does not capture yet",
	"conflicted":    `a patch application is paused on a conflict — fix it, then run "browseros-patch continue"`,
}

func init() {
	var src string
	var summary bool
	var all bool
	command := &cobra.Command{
		Use:         "status [checkout]",
		Annotations: map[string]string{"group": "Core:"},
		Short:       "Show checkout sync state",
		Example: `  browseros-patch status ch1
  browseros-patch status ch1 --summary
  browseros-patch status --all
  browseros-patch status --src /path/to/chromium/src`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) > 0 || src != "" {
					return fmt.Errorf("--all inspects every registered checkout; drop the checkout argument")
				}
				return runStatusAll(cmd)
			}
			ws, err := resolveWorkspace(cmd, args, src)
			if err != nil {
				return err
			}
			info, err := repoInfo()
			if err != nil {
				return err
			}
			status, err := engine.InspectWorkspace(cmd.Context(), engine.InspectWorkspaceOptions{
				Workspace: ws,
				Repo:      info,
				Progress:  commandProgress(cmd),
			})
			if err != nil {
				return err
			}
			return renderResult(status, func() {
				fmt.Println(ui.Title(fmt.Sprintf("%s (%s)", ws.Name, status.SyncState)))
				if hint, ok := syncStateHints[status.SyncState]; ok {
					fmt.Println(ui.Hint(hint))
				}
				fmt.Printf("%s  %s\n", ui.Muted("path:"), ws.Path)
				fmt.Printf("%s  %s\n", ui.Muted("repo head:"), status.RepoHead)
				fmt.Printf("%s  %s\n", ui.Muted("last sync:"), status.LastSyncRev)
				fmt.Printf("%s  %s\n", ui.Muted("last apply:"), status.LastApplyRev)
				fmt.Printf("%s  %s\n", ui.Muted("last extract:"), status.LastExtractRev)
				fmt.Printf("%s  %d\n", ui.Muted("needs apply:"), len(status.NeedsApply))
				fmt.Printf("%s  %d\n", ui.Muted("needs update:"), len(status.NeedsUpdate))
				fmt.Printf("%s  %d\n", ui.Muted("orphaned:"), len(status.Orphaned))
				if status.PendingStash != "" {
					fmt.Printf("%s  %s %s\n", ui.Muted("pending stash:"), status.PendingStash, ui.Hint("(local changes parked by sync)"))
				}
				if summary && len(status.Orphaned) > 0 {
					fmt.Println(ui.Header("Orphaned by directory:"))
					for _, group := range engine.OrphanSummary(status.Orphaned) {
						fmt.Printf("  %-24s %d\n", group.Dir, group.Count)
					}
				}
				if status.InSyncButUnreproducible() {
					fmt.Println(ui.Warning("Patch state is in sync, but orphaned files exist — a fresh checkout may not reproduce this workspace."))
				}
			})
		},
	}
	command.Flags().StringVar(&src, "src", "", srcFlagUsage)
	command.Flags().BoolVar(&summary, "summary", false, "Group orphaned files by top-level directory")
	command.Flags().BoolVar(&all, "all", false, "Show a one-line summary for every registered checkout")
	rootCmd.AddCommand(command)
}

// statusRow is the compact per-checkout shape used by status --all.
// pending_stash carries the same string ref as the single-checkout status.
type statusRow struct {
	Workspace    string `json:"workspace"`
	Path         string `json:"path"`
	SyncState    string `json:"sync_state,omitempty"`
	NeedsApply   int    `json:"needs_apply"`
	NeedsUpdate  int    `json:"needs_update"`
	Orphaned     int    `json:"orphaned"`
	PendingStash string `json:"pending_stash,omitempty"`
	Error        string `json:"error,omitempty"`
}

func runStatusAll(cmd *cobra.Command) error {
	info, err := repoInfo()
	if err != nil {
		return err
	}
	if len(appState.Registry.Workspaces) == 0 {
		return fmt.Errorf(`no Chromium checkouts registered; run "browseros-patch add <name> <path>" first`)
	}
	rows := make([]statusRow, 0, len(appState.Registry.Workspaces))
	for _, entry := range appState.Registry.Workspaces {
		row := statusRow{Workspace: entry.Name, Path: entry.Path}
		status, err := engine.InspectWorkspace(cmd.Context(), engine.InspectWorkspaceOptions{
			Workspace: workspace.Entry{Name: entry.Name, Path: entry.Path},
			Repo:      info,
			Progress:  commandProgress(cmd),
		})
		if err != nil {
			row.Error = err.Error()
		} else {
			row.SyncState = status.SyncState
			row.NeedsApply = len(status.NeedsApply)
			row.NeedsUpdate = len(status.NeedsUpdate)
			row.Orphaned = len(status.Orphaned)
			row.PendingStash = status.PendingStash
		}
		rows = append(rows, row)
	}
	return renderResult(rows, func() {
		headers := []string{"NAME", "STATE", "APPLY", "UPDATE", "ORPHANED", "STASH"}
		tableRows := make([][]string, 0, len(rows))
		for _, row := range rows {
			if row.Error != "" {
				tableRows = append(tableRows, []string{row.Workspace, "error: " + row.Error, "-", "-", "-", "-"})
				continue
			}
			stash := ""
			if row.PendingStash != "" {
				stash = "yes"
			}
			tableRows = append(tableRows, []string{
				row.Workspace,
				row.SyncState,
				strconv.Itoa(row.NeedsApply),
				strconv.Itoa(row.NeedsUpdate),
				strconv.Itoa(row.Orphaned),
				stash,
			})
		}
		fmt.Println(ui.RenderTable(headers, tableRows))
	})
}
