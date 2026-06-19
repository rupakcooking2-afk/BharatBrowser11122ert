package cmd

import (
	"fmt"
	"os"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/otasrv"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/sparkle"
	"github.com/spf13/cobra"
)

var otaCmd = &cobra.Command{
	Use:         "ota",
	Short:       "OTA update automation",
	Annotations: map[string]string{"group": "Release & Distribution:"},
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	serverCmd := &cobra.Command{Use: "server", Short: "BrowserOS Server OTA operations"}

	var relVersion, relChannel, relPlatform string
	releaseCmd := &cobra.Command{
		Use:   "release",
		Short: "Create and upload BrowserOS Server OTA update",
		RunE: func(cmd *cobra.Command, args []string) error {
			if relVersion == "" {
				return fmt.Errorf("--version is required")
			}
			ctx, err := buildctx.New(buildctx.Options{ChromiumSrc: "."})
			if err != nil {
				return err
			}
			return otasrv.ServerRelease(ctx, &otasrv.Deps{}, relVersion, relChannel, relPlatform)
		},
	}
	releaseCmd.Flags().StringVarP(&relVersion, "version", "v", "", "Server version, e.g. 0.0.69")
	releaseCmd.Flags().StringVarP(&relChannel, "channel", "c", "alpha", "Release channel: alpha or prod")
	releaseCmd.Flags().StringVarP(&relPlatform, "platform", "p", "",
		"Platforms (comma-separated): darwin_arm64, darwin_x64, linux_arm64, linux_x64, windows_x64")
	serverCmd.AddCommand(releaseCmd)

	var appcastChannel, appcastFile string
	appcastCmd := &cobra.Command{
		Use:   "release-appcast",
		Short: "Publish appcast XML to make the release live",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := buildctx.New(buildctx.Options{ChromiumSrc: "."})
			if err != nil {
				return err
			}
			return otasrv.ReleaseAppcast(ctx.RootDir, &otasrv.Deps{}, appcastChannel, appcastFile)
		},
	}
	appcastCmd.Flags().StringVarP(&appcastChannel, "channel", "c", "alpha", "Release channel: alpha or prod")
	appcastCmd.Flags().StringVarP(&appcastFile, "file", "f", "", "Custom appcast file to upload")
	serverCmd.AddCommand(appcastCmd)

	serverCmd.AddCommand(&cobra.Command{
		Use:   "list-platforms",
		Short: "List available server platforms",
		RunE: func(cmd *cobra.Command, args []string) error {
			logx.Info("\n📦 Available Server Platforms:")
			for _, platform := range otasrv.ServerPlatforms {
				logx.Info(fmt.Sprintf("  %-15s %-10s %s", platform.Name, platform.OS, platform.Arch))
			}
			return nil
		},
	})
	otaCmd.AddCommand(serverCmd)

	otaCmd.AddCommand(&cobra.Command{
		Use:   "test-signing <file>",
		Short: "Test Sparkle Ed25519 signing on a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]
			if _, err := os.Stat(filePath); err != nil {
				return fmt.Errorf("file not found: %s", filePath)
			}
			if !envx.HasSparkleKey() {
				return fmt.Errorf("SPARKLE_PRIVATE_KEY not set")
			}
			logx.Info("\n🔐 Testing Sparkle Ed25519 signing")
			logx.Info("File: " + filePath)
			sig, length, err := sparkle.SignFileWithEnv(filePath)
			if err != nil {
				return fmt.Errorf("signing failed: %w", err)
			}
			logx.Success("✅ Signed successfully")
			logx.Info(fmt.Sprintf("   Signature: %.50s...", sig))
			logx.Info(fmt.Sprintf("   Length: %d", length))
			return nil
		},
	})

	rootCmd.AddCommand(otaCmd)
}
