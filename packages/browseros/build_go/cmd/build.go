package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/config"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/notify"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/pipeline"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/resolver"
	"github.com/spf13/cobra"
)

type buildFlags struct {
	config      string
	modules     string
	listModules bool
	setup       bool
	prep        bool
	build       bool
	sign        bool
	pkg         bool
	upload      bool
	arch        string
	buildType   string
	chromiumSrc string
}

var buildOpts buildFlags

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build BrowserOS browser",
	Long: `BrowserOS Build System - Modular pipeline executor

Build BrowserOS using phase flags (auto-ordered), explicit modules, or configs.

Phase Flags (Recommended - Auto-Ordered):
  browseros build --setup --build --sign --package
  browseros build --build --sign           # Skip setup
  browseros build --package --sign         # Flags work in any order!

Explicit Modules (Power Users):
  browseros build --modules clean,compile,sign_macos

Config Files (CI/CD):
  browseros build --config release.yaml

List Available:
  browseros build --list                   # Show all modules and phases

Note: Phase flags always execute in correct order regardless of how you write them.
      --sign and --package auto-select platform (macos/windows/linux)`,
	Annotations: map[string]string{"group": "Build:"},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBuild(buildOpts)
	},
}

func init() {
	f := buildCmd.Flags()
	f.StringVarP(&buildOpts.config, "config", "c", "", "Load configuration from YAML file")
	f.StringVarP(&buildOpts.modules, "modules", "m", "", "Comma-separated list of modules to run")
	f.BoolVarP(&buildOpts.listModules, "list", "l", false, "List all available modules and exit")
	f.BoolVar(&buildOpts.setup, "setup", false, "Run setup phase (clean, git_setup, sparkle_setup)")
	f.BoolVar(&buildOpts.prep, "prep", false, "Run prep phase (resources, chromium_replace, string_replaces, patches, configure)")
	f.BoolVar(&buildOpts.build, "build", false, "Run build phase (compile)")
	f.BoolVar(&buildOpts.sign, "sign", false, "Run sign phase (platform-specific: sign_macos/windows/linux)")
	f.BoolVar(&buildOpts.pkg, "package", false, "Run package phase (platform-specific: package_macos/windows/linux)")
	f.BoolVar(&buildOpts.upload, "upload", false, "Run upload phase (upload artifacts)")
	f.StringVarP(&buildOpts.arch, "arch", "a", "", "Architecture (arm64, x64, universal)")
	f.StringVarP(&buildOpts.buildType, "build-type", "t", "", "Build type (debug or release)")
	f.StringVarP(&buildOpts.chromiumSrc, "chromium-src", "S", "", "Path to Chromium source directory")
	rootCmd.AddCommand(buildCmd)
}

func runBuild(opts buildFlags) error {
	available := modules.Available()

	if opts.listModules {
		pipeline.ShowAvailableModules(available)
		return nil
	}

	plat := platform.Current()

	// Mutually exclusive modes (cli/build.py main()).
	hasConfig := opts.config != ""
	hasModules := opts.modules != ""
	hasFlags := opts.setup || opts.prep || opts.build || opts.sign || opts.pkg || opts.upload
	optionsProvided := 0
	for _, on := range []bool{hasConfig, hasModules, hasFlags} {
		if on {
			optionsProvided++
		}
	}
	if optionsProvided == 0 {
		return fmt.Errorf(
			"specify --config, --modules, or phase flags (--setup, --build, etc.)\n\n" +
				"Use --help for usage information\n" +
				"Use --list to see available modules")
	}
	if optionsProvided > 1 {
		return fmt.Errorf(
			"specify only ONE of: --config, --modules, or phase flags\n" +
				"Examples:\n" +
				"  browseros build --setup --build --sign\n" +
				"  browseros build --modules clean,compile\n" +
				"  browseros build --config release.yaml")
	}

	// CONFIG mode: YAML controls everything; CLI build params not allowed.
	if hasConfig {
		var conflicting []string
		if opts.arch != "" {
			conflicting = append(conflicting, "--arch")
		}
		if opts.buildType != "" {
			conflicting = append(conflicting, "--build-type")
		}
		if len(conflicting) > 0 {
			return fmt.Errorf(
				"CONFIG MODE: Cannot use %s with --config\n"+
					"When using --config, ALL build parameters come from YAML\n"+
					"Remove the conflicting flags or don't use --config", strings.Join(conflicting, ", "))
		}
	}

	logx.Info("🚀 BrowserOS Build System")
	logx.Info(strings.Repeat("=", 70))

	var yamlCfg *config.BuildFile
	if hasConfig {
		var err error
		yamlCfg, err = config.Load(opts.config)
		if err != nil {
			return err
		}
	}

	cli := resolver.CLIArgs{
		ChromiumSrc: opts.chromiumSrc,
		Arch:        opts.arch,
		BuildType:   opts.buildType,
		Modules:     opts.modules,
		Setup:       opts.setup,
		Prep:        opts.prep,
		Build:       opts.build,
		Sign:        opts.sign,
		Package:     opts.pkg,
		Upload:      opts.upload,
	}

	archCtxs, err := resolver.ResolveContexts(cli, yamlCfg, resolver.Options{Platform: plat})
	if err != nil {
		return err
	}
	moduleList, err := resolver.ResolvePipeline(cli, yamlCfg, plat)
	if err != nil {
		return err
	}

	// Execution plan for flag-based mode.
	if hasFlags {
		logx.Info("\n📋 Execution Plan (auto-ordered):")
		logx.Info(strings.Repeat("-", 70))
		var phaseNames []string
		if opts.setup {
			phaseNames = append(phaseNames, "setup")
		}
		if opts.prep {
			phaseNames = append(phaseNames, "prep")
			logx.Warning("⚠️  --prep does NOT apply series_patches. Run 'browseros build -m series_patches' separately if needed.")
		}
		if opts.build {
			phaseNames = append(phaseNames, "build")
		}
		if opts.sign {
			signModule, _ := resolver.SignModule(plat)
			phaseNames = append(phaseNames, fmt.Sprintf("sign (→ %s)", signModule))
		}
		if opts.pkg {
			packageModule, _ := resolver.PackageModule(plat)
			phaseNames = append(phaseNames, fmt.Sprintf("package (→ %s)", packageModule))
		}
		if opts.upload {
			phaseNames = append(phaseNames, "upload")
		}
		for _, name := range phaseNames {
			logx.Info(fmt.Sprintf("  ✓ %s", name))
		}
		logx.Info(fmt.Sprintf("\n  Pipeline: %s", strings.Join(moduleList, " → ")))
		logx.Info(strings.Repeat("-", 70))
	}

	// YAML-declared required env vars.
	if yamlCfg != nil && len(yamlCfg.RequiredEnvs) > 0 {
		if err := config.ValidateRequiredEnvs(yamlCfg.RequiredEnvs); err != nil {
			return err
		}
	}

	if err := pipeline.Validate(moduleList, available); err != nil {
		return err
	}

	if plat.IsWindows() {
		os.Setenv("DEPOT_TOOLS_WIN_TOOLCHAIN", "0")
		logx.Info("Set DEPOT_TOOLS_WIN_TOOLCHAIN=0 for Windows build")
	}

	summary := archCtxs[0]
	logx.Info(fmt.Sprintf("📍 Root: %s", summary.RootDir))
	logx.Info(fmt.Sprintf("📍 Chromium: %s", summary.ChromiumSrc))
	if len(archCtxs) > 1 {
		var archs []string
		for _, c := range archCtxs {
			archs = append(archs, c.Architecture)
		}
		logx.Info(fmt.Sprintf("📍 Architectures: [%s] (multi-arch loop)", strings.Join(archs, ", ")))
	} else {
		logx.Info(fmt.Sprintf("📍 Architecture: %s", summary.Architecture))
	}
	logx.Info(fmt.Sprintf("📍 Build type: %s", summary.BuildType))
	logx.Info(fmt.Sprintf("📍 Semantic version: %s", summary.SemanticVersion))
	logx.Info(fmt.Sprintf("📍 Chromium version: %s", summary.ChromiumVersion))
	logx.Info(fmt.Sprintf("📍 Build offset: %s", summary.BrowserOSBuildOffset))
	logx.Info(fmt.Sprintf("📍 Pipeline: %s", strings.Join(moduleList, " → ")))
	logx.Info(strings.Repeat("=", 70))

	osName := map[string]string{"macos": "macOS", "windows": "Windows", "linux": "Linux"}[plat.OS]

	for i, archCtx := range archCtxs {
		if len(archCtxs) > 1 {
			logx.Info("\n" + strings.Repeat("#", 70))
			logx.Info(fmt.Sprintf("# Architecture %d/%d: %s", i+1, len(archCtxs), archCtx.Architecture))
			logx.Info(fmt.Sprintf("# Output: %s", archCtx.OutDir))
			logx.Info(strings.Repeat("#", 70))
		}
		notify.SetBuildContext(osName, archCtx.Architecture)
		if err := pipeline.Execute(archCtx, moduleList, available, "build"); err != nil {
			return err
		}
	}
	return nil
}
