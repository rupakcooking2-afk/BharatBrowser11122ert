package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/annotate"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/feature"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/patchwork"
	"github.com/spf13/cobra"
)

var (
	devChromiumSrc string
	devVerbose     bool
	devQuiet       bool
)

var devCmd = &cobra.Command{
	Use:         "dev",
	Short:       "Dev patch management",
	Long:        "Development workflows for extracting, applying, and organizing Chromium patches.",
	Annotations: map[string]string{"group": "Development:"},
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

// devContext builds the Context shared by every dev leaf; --chromium-src is
// required (cli/dev.py callback).
func devContext() (*buildctx.Context, error) {
	if devChromiumSrc == "" {
		return nil, fmt.Errorf("--chromium-src is required (e.g. browseros dev -S ~/chromium/src extract commit HEAD)")
	}
	if _, err := os.Stat(devChromiumSrc); err != nil {
		return nil, fmt.Errorf("chromium source not found: %s", devChromiumSrc)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git is not available in PATH")
	}
	return buildctx.New(buildctx.Options{ChromiumSrc: devChromiumSrc})
}

// interactivePair registers typer-style --interactive/--no-interactive flags
// (default true) and returns the resolver.
func interactivePair(cmd *cobra.Command) func() bool {
	interactive := cmd.Flags().BoolP("interactive", "i", true, "Interactive mode")
	noInteractive := cmd.Flags().Bool("no-interactive", false, "Disable interactive mode")
	return func() bool {
		if *noInteractive {
			return false
		}
		return *interactive
	}
}

func extractOptions(force, includeBinary bool, base string) patchwork.ExtractOptions {
	return patchwork.ExtractOptions{
		Verbose:       devVerbose,
		Force:         force,
		IncludeBinary: includeBinary,
		Base:          base,
	}
}

func init() {
	devCmd.PersistentFlags().StringVarP(&devChromiumSrc, "chromium-src", "S", "", "Path to Chromium source directory")
	devCmd.PersistentFlags().BoolVarP(&devVerbose, "verbose", "v", false, "Enable verbose output")
	devCmd.PersistentFlags().BoolVarP(&devQuiet, "quiet", "q", false, "Suppress non-essential output")

	devCmd.AddCommand(buildExtractCmd())
	devCmd.AddCommand(buildApplyCmd())
	devCmd.AddCommand(buildFeatureCmd())
	devCmd.AddCommand(buildAnnotateCmd())
	rootCmd.AddCommand(devCmd)
}

func buildExtractCmd() *cobra.Command {
	extractCmd := &cobra.Command{Use: "extract", Short: "Extract patches from commits or files"}

	// extract commit <commit>
	var commitOutput, commitBase string
	var commitForce, commitBinary, commitFeature bool
	commitCmd := &cobra.Command{
		Use:   "commit <commit>",
		Short: "Extract patches from a single commit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := devContext()
			if err != nil {
				return err
			}
			count, extracted, err := patchwork.ExtractCommit(ctx, args[0], extractOptions(commitForce, commitBinary, commitBase))
			if err != nil {
				return fmt.Errorf("git error: %w", err)
			}
			if count == 0 {
				logx.Warning("No patches extracted from " + args[0])
				return nil
			}
			logx.Success(fmt.Sprintf("Successfully extracted %d patches from %s", count, args[0]))
			if commitFeature {
				name, description, ok := feature.PromptSelection(ctx, args[0], "", feature.StdinPrompter{})
				if !ok {
					logx.Warning("Skipped adding files to feature")
					return nil
				}
				return feature.AddFiles(ctx, name, description, extracted)
			}
			return nil
		},
	}
	commitCmd.Flags().StringVarP(&commitOutput, "output", "o", "", "Output directory (unused, kept for compatibility)")
	interactivePair(commitCmd)
	commitCmd.Flags().BoolVarP(&commitForce, "force", "f", false, "Overwrite existing patches")
	commitCmd.Flags().BoolVar(&commitBinary, "include-binary", false, "Include binary files")
	commitCmd.Flags().StringVar(&commitBase, "base", "", "Base commit to diff from (defaults to BASE_COMMIT)")
	commitCmd.Flags().BoolVar(&commitFeature, "feature", false, "Add extracted files to a feature in features.yaml")
	extractCmd.AddCommand(commitCmd)

	// extract patch <chromium_path>
	var patchBase string
	var patchForce, patchFeature bool
	patchCmd := &cobra.Command{
		Use:   "patch <chromium_path>",
		Short: "Extract patch for a single chromium file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := devContext()
			if err != nil {
				return err
			}
			ok, err := patchwork.ExtractFilePatch(ctx, args[0], extractOptions(patchForce, false, patchBase))
			if err != nil {
				return err
			}
			if ok && patchFeature {
				name, description, promptOK := feature.PromptSelection(ctx, "", "", feature.StdinPrompter{})
				if promptOK {
					return feature.AddFiles(ctx, name, description, []string{args[0]})
				}
			}
			return nil
		},
	}
	patchCmd.Flags().StringVarP(&patchBase, "base", "b", "", "Base commit to diff against (defaults to BASE_COMMIT)")
	patchCmd.Flags().BoolVarP(&patchForce, "force", "f", false, "Overwrite existing patch without prompting")
	patchCmd.Flags().BoolVar(&patchFeature, "feature", false, "Add extracted file to a feature in features.yaml")
	extractCmd.AddCommand(patchCmd)

	// extract range <start> <end>
	var rangeOutput, rangeBase string
	var rangeForce, rangeBinary, rangeSquash, rangeFeature bool
	rangeCmd := &cobra.Command{
		Use:   "range <start> <end>",
		Short: "Extract patches from a range of commits",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := devContext()
			if err != nil {
				return err
			}
			_ = rangeSquash // the cumulative-diff extraction already squashes
			count, extracted, err := patchwork.ExtractRange(ctx, args[0], args[1], extractOptions(rangeForce, rangeBinary, rangeBase))
			if err != nil {
				return fmt.Errorf("git error: %w", err)
			}
			if count == 0 {
				logx.Warning("No patches extracted from range")
				return nil
			}
			logx.Success(fmt.Sprintf("Successfully extracted %d patches from %s..%s", count, args[0], args[1]))
			if rangeFeature {
				name, description, ok := feature.PromptSelection(ctx, args[1], "", feature.StdinPrompter{})
				if ok {
					return feature.AddFiles(ctx, name, description, extracted)
				}
			}
			return nil
		},
	}
	rangeCmd.Flags().StringVarP(&rangeOutput, "output", "o", "", "Output directory (unused, kept for compatibility)")
	interactivePair(rangeCmd)
	rangeCmd.Flags().BoolVarP(&rangeForce, "force", "f", false, "Overwrite existing patches")
	rangeCmd.Flags().BoolVar(&rangeBinary, "include-binary", false, "Include binary files")
	rangeCmd.Flags().BoolVar(&rangeSquash, "squash", false, "Squash all commits into single patches")
	rangeCmd.Flags().StringVar(&rangeBase, "base", "", "Base commit to diff from (defaults to BASE_COMMIT)")
	rangeCmd.Flags().BoolVar(&rangeFeature, "feature", false, "Add extracted files to a feature in features.yaml")
	extractCmd.AddCommand(rangeCmd)

	return extractCmd
}

func buildApplyCmd() *cobra.Command {
	applyCmd := &cobra.Command{Use: "apply", Short: "Apply patches to the Chromium tree"}

	// apply all
	var allResetTo string
	var allAnnotate bool
	allCmd := &cobra.Command{
		Use:   "all",
		Short: "Apply all patches from chromium_patches/",
	}
	allInteractive := interactivePair(allCmd)
	allCmd.Flags().StringVarP(&allResetTo, "reset-to", "r", "", "Reset files to this commit before applying")
	allCmd.Flags().BoolVarP(&allAnnotate, "annotate", "a", false, "Create git commits per feature after applying")
	allCmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx, err := devContext()
		if err != nil {
			return err
		}
		_, failed, err := patchwork.ApplyAllPatches(ctx, false, allInteractive(), patchwork.StdinConfirmer{}, allResetTo)
		if err != nil {
			return err
		}
		if len(failed) > 0 {
			return fmt.Errorf("failed to apply %d patches", len(failed))
		}
		if allAnnotate {
			logx.Info("\n🏗️  Creating feature-based commits...")
			commits, _, err := annotate.Features(ctx, "")
			if err != nil {
				return err
			}
			logx.Success(fmt.Sprintf("✓ Created %d commit(s)", commits))
		}
		return nil
	}
	applyCmd.AddCommand(allCmd)

	// apply feature <name>
	var featResetTo string
	var featAnnotate bool
	featCmd := &cobra.Command{
		Use:   "feature <feature_name>",
		Short: "Apply patches for a specific feature",
		Args:  cobra.ExactArgs(1),
	}
	interactivePair(featCmd)
	featCmd.Flags().StringVarP(&featResetTo, "reset-to", "r", "", "Reset files to this commit before applying")
	featCmd.Flags().BoolVarP(&featAnnotate, "annotate", "a", false, "Create git commit for this feature after applying")
	featCmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx, err := devContext()
		if err != nil {
			return err
		}
		_, failed, err := patchwork.ApplyFeaturePatches(ctx, args[0], false, featResetTo)
		if err != nil {
			return err
		}
		if len(failed) > 0 {
			return fmt.Errorf("failed to apply %d patches for feature '%s'", len(failed), args[0])
		}
		if featAnnotate {
			logx.Info(fmt.Sprintf("\n🏗️  Creating commit for feature '%s'...", args[0]))
			if _, _, err := annotate.Features(ctx, args[0]); err != nil {
				return err
			}
		}
		return nil
	}
	applyCmd.AddCommand(featCmd)

	// apply patch <chromium_path>
	var patchResetTo string
	var patchDryRun bool
	patchCmd := &cobra.Command{
		Use:   "patch <chromium_path>",
		Short: "Apply patch for a single chromium file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := devContext()
			if err != nil {
				return err
			}
			ok, message := patchwork.ApplySingleFilePatch(ctx, args[0], patchResetTo, patchDryRun)
			if !ok {
				return fmt.Errorf("%s", message)
			}
			return nil
		},
	}
	patchCmd.Flags().StringVarP(&patchResetTo, "reset-to", "r", "", "Reset file to this commit before applying")
	patchCmd.Flags().BoolVar(&patchDryRun, "dry-run", false, "Test without applying")
	applyCmd.AddCommand(patchCmd)

	// apply force
	var forceResetTo string
	forceCmd := &cobra.Command{
		Use:   "force",
		Short: "Apply all patches non-interactively, writing .rej files for conflicts",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := devContext()
			if err != nil {
				return err
			}
			_, rejected, _ := patchwork.ApplyAllForce(ctx, forceResetTo)
			if rejected > 0 {
				logx.Warning(fmt.Sprintf("%d patch(es) had conflicts. Review .rej files in chromium source.", rejected))
			}
			return nil
		},
	}
	forceCmd.Flags().StringVarP(&forceResetTo, "reset-to", "r", "", "Reset files to this commit before applying")
	applyCmd.AddCommand(forceCmd)

	// apply changed
	var changedCommit, changedRangeStart, changedRangeEnd, changedResetTo string
	var changedDryRun bool
	changedCmd := &cobra.Command{
		Use:   "changed",
		Short: "Apply patches that changed in browseros repo commits",
		RunE: func(cmd *cobra.Command, args []string) error {
			if changedResetTo == "" {
				return fmt.Errorf("--reset-to is required")
			}
			ctx, err := devContext()
			if err != nil {
				return err
			}
			var changes [][2]string
			switch {
			case changedCommit != "":
				changes, err = patchwork.ChangedFilesInCommit(ctx, changedCommit)
			case changedRangeStart != "" && changedRangeEnd != "":
				changes, err = patchwork.ChangedFilesInRange(ctx, changedRangeStart, changedRangeEnd)
			default:
				return fmt.Errorf("specify --commit or both --range-start and --range-end")
			}
			if err != nil {
				return err
			}
			patchChanges := patchwork.FilterPatchChanges(changes)
			if len(patchChanges) == 0 {
				logx.Info("No changed patches found")
				return nil
			}
			logx.Info(fmt.Sprintf("Found %d changed patch(es)", len(patchChanges)))
			applied, resetOnly, failed := patchwork.ApplyChangedPatches(ctx, patchChanges, changedResetTo, changedDryRun)
			logx.Info(fmt.Sprintf("\nSummary: %d applied, %d reset-only, %d failed", applied, resetOnly, len(failed)))
			if len(failed) > 0 {
				return fmt.Errorf("failed to apply %d patches", len(failed))
			}
			return nil
		},
	}
	changedCmd.Flags().StringVarP(&changedCommit, "commit", "c", "", "Single commit hash in the browseros repo")
	changedCmd.Flags().StringVar(&changedRangeStart, "range-start", "", "Range start commit (exclusive)")
	changedCmd.Flags().StringVar(&changedRangeEnd, "range-end", "", "Range end commit (inclusive)")
	changedCmd.Flags().StringVarP(&changedResetTo, "reset-to", "r", "", "Reset chromium files to this commit (required)")
	changedCmd.Flags().BoolVar(&changedDryRun, "dry-run", false, "Test without applying")
	applyCmd.AddCommand(changedCmd)

	return applyCmd
}

func buildFeatureCmd() *cobra.Command {
	featureCmd := &cobra.Command{Use: "feature", Short: "Manage feature-to-file mappings"}

	featureCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all defined features",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := devContext()
			if err != nil {
				return err
			}
			return feature.List(ctx)
		},
	})
	featureCmd.AddCommand(&cobra.Command{
		Use:   "show <feature_name>",
		Short: "Show details of a specific feature",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := devContext()
			if err != nil {
				return err
			}
			return feature.Show(ctx, args[0])
		},
	})

	var addName, addCommit, addDescription string
	addCmd := &cobra.Command{
		Use:   "add-update",
		Short: "Add or update a feature with files from a commit",
		RunE: func(cmd *cobra.Command, args []string) error {
			if addName == "" || addCommit == "" || addDescription == "" {
				return fmt.Errorf("--name, --commit, and --description are required")
			}
			ctx, err := devContext()
			if err != nil {
				return err
			}
			return feature.AddOrUpdate(ctx, addName, addCommit, addDescription)
		},
	}
	addCmd.Flags().StringVarP(&addName, "name", "n", "", "Feature key name (e.g., llm-chat)")
	addCmd.Flags().StringVarP(&addCommit, "commit", "c", "", "Git commit reference")
	addCmd.Flags().StringVarP(&addDescription, "description", "d", "", "Feature description with prefix (e.g., 'feat: LLM chat')")
	featureCmd.AddCommand(addCmd)

	featureCmd.AddCommand(&cobra.Command{
		Use:   "classify",
		Short: "Classify unclassified patch files into features",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := devContext()
			if err != nil {
				return err
			}
			_, _, err = feature.Classify(ctx, feature.StdinPrompter{})
			return err
		},
	})
	return featureCmd
}

func buildAnnotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "annotate [feature_name]",
		Short: "Create git commits organized by features",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := devContext()
			if err != nil {
				return err
			}
			featureFilter := ""
			if len(args) > 0 {
				featureFilter = args[0]
			}
			logx.Info("🏗️  Annotate Features")
			logx.Info("📁 Chromium source: " + ctx.ChromiumSrc)
			logx.Info("📄 Features file: " + ctx.FeaturesYAMLPath())
			commits, skipped, err := annotate.Features(ctx, featureFilter)
			if err != nil {
				return err
			}
			if commits > 0 {
				logx.Success(fmt.Sprintf("✓ Created %d commit(s)", commits))
			} else {
				logx.Info("No commits created (no modified files found)")
			}
			if skipped > 0 {
				logx.Info(fmt.Sprintf("  Skipped %d feature(s) with no changes", skipped))
			}
			return nil
		},
	}
}
