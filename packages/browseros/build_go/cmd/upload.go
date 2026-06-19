package cmd

import (
	"fmt"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/vendorup"
	"github.com/spf13/cobra"
)

var uploadCmd = &cobra.Command{
	Use:         "upload",
	Short:       "Upload third-party resources to R2",
	Long:        "Upload third-party resources to Cloudflare R2 for build:server ingestion.",
	Annotations: map[string]string{"group": "Release & Distribution:"},
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

// requireR2Config mirrors the lima/bun commands, which demand R2 config even
// for dry-runs (codex/claude-code only need it for real uploads).
func requireR2Config() error {
	if !envx.HasR2Config() {
		return fmt.Errorf(
			"R2 configuration missing. Required: R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY")
	}
	return nil
}

func newVendorUploadCmd(name, short, versionHelp, defaultVersion string, strictR2 bool,
	run func(*vendorup.Deps, string) error) *cobra.Command {
	var version string
	var dryRun bool
	command := &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if version == "" {
				return fmt.Errorf("--version is required")
			}
			if strictR2 {
				if err := requireR2Config(); err != nil {
					return err
				}
			}
			deps, err := vendorup.Prepare(dryRun)
			if err != nil {
				return err
			}
			return run(deps, version)
		},
	}
	command.Flags().StringVarP(&version, "version", "v", defaultVersion, versionHelp)
	command.Flags().BoolVar(&dryRun, "dry-run", false, "Download + verify only; skip R2 uploads.")
	return command
}

func init() {
	uploadCmd.AddCommand(newVendorUploadCmd(
		"lima", "Download limactl from a Lima GitHub release and push to R2",
		"Lima release tag, e.g. v1.2.3", "", true, vendorup.UploadLima))
	uploadCmd.AddCommand(newVendorUploadCmd(
		"bun", "Download Bun from an upstream GitHub release and push target binaries to R2",
		"Bun release tag, e.g. bun-v1.2.15", "", true, vendorup.UploadBun))
	uploadCmd.AddCommand(newVendorUploadCmd(
		"codex", "Download Codex release packages and push normalized native binaries to R2",
		"Codex release tag, e.g. rust-v0.136.0", vendorup.CodexDefaultTag, false, vendorup.UploadCodex))
	uploadCmd.AddCommand(newVendorUploadCmd(
		"claude-code", "Download Claude Code release binaries and push normalized objects to R2",
		"Claude Code release version, e.g. 2.1.159", vendorup.ClaudeCodeDefaultVersion, false, vendorup.UploadClaudeCode))
	rootCmd.AddCommand(uploadCmd)
}
