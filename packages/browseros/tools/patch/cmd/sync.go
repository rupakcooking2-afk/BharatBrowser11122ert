package cmd

import (
	"fmt"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/engine"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/ui"
	"github.com/spf13/cobra"
)

func init() {
	var src string
	var rebase bool
	var noRebase bool
	var remote string
	command := &cobra.Command{
		Use:         "sync [checkout]",
		Aliases:     []string{"pull"},
		Annotations: map[string]string{"group": "Core:"},
		Short:       "Pull the latest patches and rebase local changes onto them",
		Example: `  browseros-patch sync ch1
  browseros-patch pull ch1
  browseros-patch sync ch1 --no-rebase
  browseros-patch sync --src /path/to/chromium/src`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := resolveWorkspace(cmd, args, src)
			if err != nil {
				return err
			}
			info, err := repoInfo()
			if err != nil {
				return err
			}
			result, err := engine.Sync(cmd.Context(), engine.SyncOptions{
				Workspace: ws,
				Repo:      info,
				Remote:    remote,
				Rebase:    rebase && !noRebase,
				Progress:  commandProgress(cmd),
			})
			if err != nil {
				return err
			}
			if err := renderResult(result, func() {
				if result.StashConflict || len(result.Conflicts) > 0 {
					fmt.Println(ui.Warning(fmt.Sprintf("Sync paused for %s", ws.Name)))
				} else {
					fmt.Println(ui.Title(fmt.Sprintf("Synced %s", ws.Name)))
				}
				fmt.Printf("%s  %s\n", ui.Muted("repo head:"), result.RepoHead)
				fmt.Printf("%s  %d\n", ui.Muted("applied:"), len(result.Applied))
				switch {
				case result.StashRestored:
					fmt.Println(ui.Success("Local changes rebased on top of the new patches."))
				case result.StashConflict:
					fmt.Println(ui.Warning("Local changes conflict with the new patches"))
					for _, file := range result.StashConflictFiles {
						fmt.Printf("  %s\n", file)
					}
					fmt.Println(ui.Hint(`Resolve the conflicted files (markers, or restored content for delete conflicts), then "git stash drop" in the checkout.`))
				case result.StashRef != "":
					fmt.Printf("%s  %s\n", ui.Muted("stash:"), result.StashRef)
					fmt.Println(ui.Hint("Local changes are parked in the stash (--no-rebase)."))
				}
				if len(result.Conflicts) > 0 {
					fmt.Println(ui.Warning("Conflicts detected"))
					for _, conflict := range result.Conflicts {
						fmt.Printf("  %s\n", conflict)
					}
					fmt.Println(ui.Hint(`Run "browseros-patch continue" after fixing the current conflict.`))
				}
			}); err != nil {
				return err
			}
			return conflictPauseError(len(result.Conflicts) > 0 || result.StashConflict)
		},
	}
	command.Flags().StringVar(&src, "src", "", srcFlagUsage)
	command.Flags().BoolVar(&rebase, "rebase", true, "Re-apply stashed local changes after syncing")
	command.Flags().BoolVar(&noRebase, "no-rebase", false, "Leave local changes parked in the stash instead of rebasing them")
	_ = command.Flags().MarkDeprecated("rebase", "rebasing is now the default; use --no-rebase to opt out")
	command.Flags().StringVar(&remote, "remote", "origin", "Remote to pull from")
	rootCmd.AddCommand(command)
}
