package cmd

import (
	"fmt"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/release"
	"github.com/spf13/cobra"
)

type releaseFlags struct {
	version     string
	list        bool
	appcast     bool
	publish     bool
	download    bool
	osFilter    string
	output      string
	showModules bool
}

var releaseOpts releaseFlags

var releaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Release automation",
	Long: `Release automation for BrowserOS

Quick Operations (Flags):
  browseros release --list                        # List all available versions
  browseros release --list --version 0.31.0       # List artifacts for version
  browseros release --version 0.31.0 --appcast    # Generate appcast XML
  browseros release --version 0.31.0 --publish    # Publish to download/ paths
  browseros release --version 0.31.0 --download   # Download all artifacts

GitHub Release (Sub-command):
  browseros release github create --version 0.31.0`,
	Annotations: map[string]string{"group": "Release & Distribution:"},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRelease(releaseOpts)
	},
}

func runRelease(opts releaseFlags) error {
	if opts.showModules {
		logx.Info("\n📦 Available Release Modules:")
		for _, line := range []string{
			"  list: List release artifacts from R2",
			"  appcast: Generate Sparkle appcast XML snippets",
			"  publish: Publish versioned artifacts to latest download URLs",
			"  download: Download release artifacts from CDN",
			"  github: Create GitHub release from R2 artifacts",
		} {
			logx.Info(line)
		}
		return nil
	}

	hasFlags := opts.list || opts.appcast || opts.publish || opts.download
	if !hasFlags {
		return fmt.Errorf(
			"specify a flag (--list, --appcast, --publish, --download) or use a sub-command\n\n" +
				"Use --help for usage information\nUse --show-modules to see available modules")
	}
	if (opts.appcast || opts.publish || opts.download) && opts.version == "" {
		return fmt.Errorf("--version is required for this operation")
	}

	deps := &release.Deps{}
	if opts.list {
		if opts.version != "" {
			logx.Info(fmt.Sprintf("📋 Listing artifacts for v%s", opts.version))
		} else {
			logx.Info("📋 Listing all available releases")
		}
		if err := deps.List(opts.version); err != nil {
			return err
		}
	}
	if opts.appcast {
		logx.Info(fmt.Sprintf("📝 Generating appcast for v%s", opts.version))
		if err := deps.Appcast(opts.version); err != nil {
			return err
		}
	}
	if opts.publish {
		logx.Info(fmt.Sprintf("🚀 Publishing v%s to download/ paths", opts.version))
		if err := deps.Publish(opts.version); err != nil {
			return err
		}
	}
	if opts.download {
		logx.Info(fmt.Sprintf("📥 Downloading artifacts for v%s", opts.version))
		if err := deps.Download(opts.version, opts.osFilter, opts.output); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	f := releaseCmd.Flags()
	f.StringVarP(&releaseOpts.version, "version", "v", "", "Version to operate on (e.g., 0.31.0)")
	f.BoolVarP(&releaseOpts.list, "list", "l", false, "List artifacts for version from R2")
	f.BoolVarP(&releaseOpts.appcast, "appcast", "a", false, "Generate appcast XML snippets")
	f.BoolVarP(&releaseOpts.publish, "publish", "p", false, "Publish to download/ paths (make live)")
	f.BoolVarP(&releaseOpts.download, "download", "d", false, "Download artifacts to temp directory")
	f.StringVar(&releaseOpts.osFilter, "os", "", "Filter by OS: macos, windows, linux")
	f.StringVarP(&releaseOpts.output, "output", "o", "", "Output directory for downloads (default: temp dir)")
	f.BoolVar(&releaseOpts.showModules, "show-modules", false, "Show available modules and exit")

	githubCmd := &cobra.Command{Use: "github", Short: "GitHub release operations"}
	var ghVersion, ghRepo, ghTitle string
	var ghDraft, ghSkipUpload, ghPublish bool
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create GitHub release from R2 artifacts",
		RunE: func(cmd *cobra.Command, args []string) error {
			if ghVersion == "" {
				return fmt.Errorf("--version is required")
			}
			deps := &release.Deps{}
			logx.Info(fmt.Sprintf("🚀 Creating GitHub release for v%s", ghVersion))
			if err := deps.GithubCreate(release.GithubOptions{
				Version: ghVersion, Repo: ghRepo, Title: ghTitle,
				Draft: ghDraft, SkipUpload: ghSkipUpload,
			}); err != nil {
				return err
			}
			if ghPublish {
				logx.Info(fmt.Sprintf("\n🚀 Publishing v%s to download/ paths", ghVersion))
				return deps.Publish(ghVersion)
			}
			return nil
		},
	}
	createCmd.Flags().StringVarP(&ghVersion, "version", "v", "", "Version to release (e.g., 0.31.0)")
	createCmd.Flags().BoolVar(&ghDraft, "draft", true, "Create as draft (default: draft)")
	createCmd.Flags().StringVarP(&ghRepo, "repo", "r", "", "GitHub repo (owner/name)")
	createCmd.Flags().BoolVar(&ghSkipUpload, "skip-upload", false, "Skip uploading artifacts to GitHub")
	createCmd.Flags().StringVarP(&ghTitle, "title", "t", "", "Release title (default: v{version})")
	createCmd.Flags().BoolVarP(&ghPublish, "publish", "p", false, "Also publish to download/ paths after creating release")
	githubCmd.AddCommand(createCmd)
	releaseCmd.AddCommand(githubCmd)
	rootCmd.AddCommand(releaseCmd)
}
