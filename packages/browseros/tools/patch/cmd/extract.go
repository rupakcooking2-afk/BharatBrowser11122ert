package cmd

import (
	"fmt"

	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/engine"
	"github.com/browseros-ai/BrowserOS/packages/browseros/tools/patch/internal/ui"
	"github.com/spf13/cobra"
)

func init() {
	var src string
	var commit string
	var rangeMode bool
	var squash bool
	var base string
	var dryRun bool
	var excludes []string
	command := &cobra.Command{
		Use:         "extract [checkout] [--range <start> <end>] [-- files...]",
		Annotations: map[string]string{"group": "Core:"},
		Short:       "Extract checkout changes back to chromium_patches",
		Example: `  browseros-patch extract ch1
  browseros-patch extract ch1 --range HEAD~2 HEAD
  browseros-patch extract --src /path/to/chromium/src`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			positional, filters := splitWorkspaceAndFilters(cmd, args)
			workspaceArgs := positional
			rangeStart := ""
			rangeEnd := ""
			if rangeMode {
				if len(positional) < 2 || len(positional) > 3 {
					return fmt.Errorf(`range mode expects "browseros-patch extract [checkout] --range <start> <end>"`)
				}
				rangeStart = positional[len(positional)-2]
				rangeEnd = positional[len(positional)-1]
				workspaceArgs = positional[:len(positional)-2]
			}
			if len(workspaceArgs) > 1 {
				return fmt.Errorf("expected at most one checkout name")
			}
			ws, err := resolveWorkspace(cmd, workspaceArgs, src)
			if err != nil {
				return err
			}
			info, err := repoInfo()
			if err != nil {
				return err
			}
			result, err := engine.Extract(cmd.Context(), engine.ExtractOptions{
				Workspace:  ws,
				Repo:       info,
				Commit:     commit,
				RangeStart: rangeStart,
				RangeEnd:   rangeEnd,
				Squash:     squash,
				Base:       base,
				Filters:    filters,
				Excludes:   excludes,
				DryRun:     dryRun,
				Progress:   commandProgress(cmd),
			})
			if err != nil {
				return err
			}
			return renderResult(result, func() {
				title := fmt.Sprintf("Extracted patches from %s", ws.Name)
				if result.DryRun {
					title = fmt.Sprintf("Extract preview for %s (dry run)", ws.Name)
				}
				fmt.Println(ui.Title(title))
				fmt.Printf("%s  %s\n", ui.Muted("mode:"), result.Mode)
				fmt.Printf("%s  %d (%d new, %d updated)\n", ui.Muted("written:"), len(result.Written), len(result.Created), len(result.Updated))
				fmt.Printf("%s  %d\n", ui.Muted("unchanged:"), len(result.Unchanged))
				fmt.Printf("%s  %d\n", ui.Muted("deleted:"), len(result.Deleted))
				if result.DryRun {
					printGroup("Would create", result.Created)
					printGroup("Would update", result.Updated)
					printGroup("Would delete", result.Deleted)
				}
				if len(result.Written) == 0 && len(result.Deleted) == 0 && !result.DryRun {
					fmt.Println(ui.Hint("Patch repo already matches this checkout — nothing rewritten."))
				}
			})
		},
	}
	command.Flags().StringVar(&src, "src", "", srcFlagUsage)
	command.Flags().StringVar(&commit, "commit", "", "Extract from a single commit")
	command.Flags().BoolVar(&rangeMode, "range", false, "Extract from a commit range")
	command.Flags().BoolVar(&squash, "squash", false, "Squash a range into a cumulative diff")
	command.Flags().StringVar(&base, "base", "", "Override BASE_COMMIT for extraction")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "Preview what would be written without touching the patch repo")
	command.Flags().StringArrayVar(&excludes, "exclude", nil, "Extra ignore pattern for untracked files; also removes previously extracted patches matching it (repeatable)")
	rootCmd.AddCommand(command)
}
