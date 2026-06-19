package cmd

import (
	"fmt"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/engine"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/resolve"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/ui"
	"github.com/spf13/cobra"
)

func init() {
	command := &cobra.Command{
		Use:         "skip",
		Annotations: map[string]string{"group": "Conflict:"},
		Short:       "Skip the current conflict and move to the next one",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := resolve.FindActive(appState.Registry, appState.CWD)
			if err != nil {
				return err
			}
			result, err := engine.Skip(cmd.Context(), engine.SkipOptions{
				Workspace: ws,
				Progress:  commandProgress(cmd),
			})
			if err != nil {
				return err
			}
			if err := renderResult(result, func() {
				fmt.Println(ui.Warning(fmt.Sprintf("Skipped current conflict in %s", ws.Name)))
				if len(result.Conflicts) > 0 {
					fmt.Println(ui.Warning("Next conflict"))
					for _, conflict := range result.Conflicts {
						fmt.Printf("  %s\n", conflict.ChromiumPath)
					}
				}
				printStashOutcome(result)
			}); err != nil {
				return err
			}
			return conflictPauseError(len(result.Conflicts) > 0 || result.StashConflict)
		},
	}
	rootCmd.AddCommand(command)
}
