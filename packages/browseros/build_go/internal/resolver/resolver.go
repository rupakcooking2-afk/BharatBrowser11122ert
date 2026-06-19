// Package resolver ports build/common/resolver.py — the single source of
// truth for config resolution — plus the fixed EXECUTION_ORDER from
// cli/build.py.
package resolver

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/config"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

var validArchitectures = map[string]bool{"x64": true, "arm64": true, "universal": true}

func validArchList() string {
	names := make([]string, 0, len(validArchitectures))
	for name := range validArchitectures {
		names = append(names, name)
	}
	sort.Strings(names)
	return "['" + strings.Join(names, "', '") + "']"
}

// CLIArgs carries the build command's flag values (zero values = not given).
type CLIArgs struct {
	ChromiumSrc string
	Arch        string
	BuildType   string
	Modules     string
	Setup       bool
	Prep        bool
	Build       bool
	Sign        bool
	Package     bool
	Upload      bool
}

// HasPhaseFlags reports whether any phase flag is set.
func (a CLIArgs) HasPhaseFlags() bool {
	return a.Setup || a.Prep || a.Build || a.Sign || a.Package || a.Upload
}

// Options pins platform/root/runner for tests; zero values use the host.
type Options struct {
	Platform platform.Platform
	RootDir  string
	Runner   execx.Runner
}

// ResolveContexts returns one Context per architecture (resolver.py
// resolve_config). CONFIG mode when yamlCfg is non-nil.
func ResolveContexts(cli CLIArgs, yamlCfg *config.BuildFile, opts Options) ([]*buildctx.Context, error) {
	if yamlCfg != nil {
		return resolveConfigMode(cli, yamlCfg, opts)
	}
	return resolveDirectMode(cli, opts)
}

func newContexts(chromiumSrc, buildType string, architectures []string, opts Options) ([]*buildctx.Context, error) {
	contexts := make([]*buildctx.Context, 0, len(architectures))
	for _, arch := range architectures {
		ctx, err := buildctx.New(buildctx.Options{
			ChromiumSrc:  chromiumSrc,
			Architecture: arch,
			BuildType:    buildType,
			Platform:     &opts.Platform,
			RootDir:      opts.RootDir,
			Runner:       opts.Runner,
		})
		if err != nil {
			return nil, err
		}
		contexts = append(contexts, ctx)
	}
	return contexts, nil
}

func resolveConfigMode(cli CLIArgs, yamlCfg *config.BuildFile, opts Options) ([]*buildctx.Context, error) {
	// chromium_src: CLI override > YAML > error.
	chromiumSrc := cli.ChromiumSrc
	source := "cli"
	if chromiumSrc == "" {
		chromiumSrc = yamlCfg.Build.ChromiumSrc
		source = "yaml"
	}
	if chromiumSrc == "" {
		return nil, fmt.Errorf(
			"CONFIG MODE: chromium_src required in YAML!\n" +
				"Add to your config:\n" +
				"  build:\n" +
				"    chromium_src: /path/to/chromium")
	}
	if _, err := os.Stat(chromiumSrc); err != nil {
		return nil, fmt.Errorf(
			"CONFIG MODE: chromium_src does not exist: %s\n"+
				"Expected directory with Chromium source code", chromiumSrc)
	}

	// architecture: CLI override > YAML (scalar or list) > platform default.
	var architectures []string
	archSource := "cli"
	switch {
	case cli.Arch != "":
		architectures = []string{cli.Arch}
	case yamlCfg.Build.Architectures() != nil:
		architectures = yamlCfg.Build.Architectures()
		archSource = "yaml"
	default:
		architectures = []string{opts.Platform.Arch}
		archSource = "default"
		logx.Info(fmt.Sprintf("CONFIG MODE: Using platform default architecture: %s", architectures[0]))
	}
	for _, arch := range architectures {
		if !validArchitectures[arch] {
			return nil, fmt.Errorf("CONFIG MODE: invalid architecture '%s'. Valid: %s", arch, validArchList())
		}
	}

	// build_type: CLI override > YAML > debug.
	buildType := cli.BuildType
	buildTypeSource := "cli"
	if buildType == "" {
		buildType = yamlCfg.Build.Type
		buildTypeSource = "yaml"
	}
	if buildType == "" {
		buildType = "debug"
	}

	logx.Info(fmt.Sprintf("✓ CONFIG MODE: chromium_src=%s (%s)", chromiumSrc, source))
	if len(architectures) > 1 {
		logx.Info(fmt.Sprintf("✓ CONFIG MODE: architectures=%v (%s, multi-arch loop)", architectures, archSource))
	} else {
		logx.Info(fmt.Sprintf("✓ CONFIG MODE: architecture=%s (%s)", architectures[0], archSource))
	}
	logx.Info(fmt.Sprintf("✓ CONFIG MODE: build_type=%s (%s)", buildType, buildTypeSource))

	return newContexts(chromiumSrc, buildType, architectures, opts)
}

func resolveDirectMode(cli CLIArgs, opts Options) ([]*buildctx.Context, error) {
	// chromium_src: CLI > Env > Error.
	chromiumSrc := cli.ChromiumSrc
	if chromiumSrc == "" {
		chromiumSrc = envx.ChromiumSrc()
	}
	if chromiumSrc == "" {
		return nil, fmt.Errorf(
			"DIRECT MODE: chromium_src required!\n" +
				"Provide via one of:\n" +
				"  --chromium-src PATH\n" +
				"  CHROMIUM_SRC environment variable")
	}
	if _, err := os.Stat(chromiumSrc); err != nil {
		return nil, fmt.Errorf(
			"DIRECT MODE: chromium_src does not exist: %s\n"+
				"Expected directory with Chromium source code", chromiumSrc)
	}

	// architecture: CLI > Env > platform default.
	arch := cli.Arch
	if arch == "" {
		arch = envx.Arch()
	}
	if arch == "" {
		arch = opts.Platform.Arch
		logx.Info(fmt.Sprintf("DIRECT MODE: Using platform default architecture: %s", arch))
	}
	if !validArchitectures[arch] {
		return nil, fmt.Errorf("DIRECT MODE: invalid architecture '%s'. Valid: %s", arch, validArchList())
	}

	// build_type: CLI > debug.
	buildType := cli.BuildType
	if buildType == "" {
		buildType = "debug"
	}

	logx.Info(fmt.Sprintf("✓ DIRECT MODE: chromium_src=%s (cli/env)", chromiumSrc))
	logx.Info(fmt.Sprintf("✓ DIRECT MODE: architecture=%s (cli/env/default)", arch))
	logx.Info(fmt.Sprintf("✓ DIRECT MODE: build_type=%s (cli/default)", buildType))

	return newContexts(chromiumSrc, buildType, []string{arch}, opts)
}

// Phase pairs a phase flag with its modules.
type Phase struct {
	Name    string
	Modules []string
}

// SignModule returns the platform sign module name (cli/build.py
// _get_sign_module).
func SignModule(plat platform.Platform) (string, error) {
	switch {
	case plat.IsMacOS():
		return "sign_macos", nil
	case plat.IsWindows():
		return "sign_windows", nil
	case plat.IsLinux():
		return "sign_linux", nil
	}
	return "", fmt.Errorf("unsupported platform for signing: %s", plat.OS)
}

// PackageModule returns the platform package module name.
func PackageModule(plat platform.Platform) (string, error) {
	switch {
	case plat.IsMacOS():
		return "package_macos", nil
	case plat.IsWindows():
		return "package_windows", nil
	case plat.IsLinux():
		return "package_linux", nil
	}
	return "", fmt.Errorf("unsupported platform for packaging: %s", plat.OS)
}

// ExecutionOrder is the fixed phase order from cli/build.py EXECUTION_ORDER.
// NOTE: prep intentionally excludes series_patches.
func ExecutionOrder(plat platform.Platform) ([]Phase, error) {
	signModule, err := SignModule(plat)
	if err != nil {
		return nil, err
	}
	packageModule, err := PackageModule(plat)
	if err != nil {
		return nil, err
	}
	return []Phase{
		{"setup", []string{"clean", "git_setup", "sparkle_setup"}},
		{"prep", []string{
			"download_resources",
			"resources",
			"bundled_extensions",
			"chromium_replace",
			"string_replaces",
			"patches",
			"configure",
		}},
		{"build", []string{"compile"}},
		{"sign", []string{signModule}},
		{"package", []string{packageModule}},
		{"upload", []string{"upload"}},
	}, nil
}

// ResolvePipeline returns the module list (resolver.py resolve_pipeline).
func ResolvePipeline(cli CLIArgs, yamlCfg *config.BuildFile, plat platform.Platform) ([]string, error) {
	if yamlCfg != nil {
		if len(yamlCfg.Modules) == 0 {
			return nil, fmt.Errorf(
				"CONFIG MODE: modules required in YAML!\n" +
					"Add to your config:\n" +
					"  modules: [clean, configure, compile, sign_macos]")
		}
		logx.Info(fmt.Sprintf("✓ CONFIG MODE: pipeline=%v (yaml)", yamlCfg.Modules))
		return yamlCfg.Modules, nil
	}

	hasModules := cli.Modules != ""
	hasFlags := cli.HasPhaseFlags()
	if !hasModules && !hasFlags {
		return nil, fmt.Errorf(
			"DIRECT MODE: No pipeline specified!\n" +
				"Use one of:\n" +
				"  --modules clean,compile,...\n" +
				"  --setup --build --sign  (phase flags)")
	}
	if hasModules && hasFlags {
		return nil, fmt.Errorf(
			"DIRECT MODE: Cannot use both --modules and phase flags!\n" +
				"Choose one approach.")
	}

	if hasModules {
		var pipeline []string
		for _, name := range strings.Split(cli.Modules, ",") {
			pipeline = append(pipeline, strings.TrimSpace(name))
		}
		logx.Info(fmt.Sprintf("✓ DIRECT MODE: pipeline=%v (--modules)", pipeline))
		return pipeline, nil
	}

	order, err := ExecutionOrder(plat)
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{
		"setup":   cli.Setup,
		"prep":    cli.Prep,
		"build":   cli.Build,
		"sign":    cli.Sign,
		"package": cli.Package,
		"upload":  cli.Upload,
	}
	var pipeline []string
	for _, phase := range order {
		if enabled[phase.Name] {
			pipeline = append(pipeline, phase.Modules...)
		}
	}
	logx.Info(fmt.Sprintf("✓ DIRECT MODE: pipeline=%v (phase flags)", pipeline))
	return pipeline, nil
}
