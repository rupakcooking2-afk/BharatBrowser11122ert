// Package universal ports build/modules/compile/universal.py: the macOS
// universal-binary pipeline that builds arm64 + x64, signs/packages/uploads
// each, merges them, and processes the universal bundle.
package universal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	compilemod "github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/compile"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/pkg"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/resources"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/setup"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/sign"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/storage"
)

var universalArchitectures = []string{"arm64", "x64"}

// Build is the universal_build pipeline module.
type Build struct{}

func NewBuild() *Build { return &Build{} }

func (Build) Name() string { return "universal_build" }
func (Build) Description() string {
	return "Build, sign, package, and upload universal binary (arm64 + x64) for macOS"
}

func (Build) Validate(ctx *buildctx.Context) error {
	if !ctx.Platform.IsMacOS() {
		return fmt.Errorf("universal builds only supported on macOS")
	}
	universalizer := universalizerScript(ctx)
	if _, err := os.Stat(universalizer); err != nil {
		return fmt.Errorf("universalizer script not found: %s", universalizer)
	}
	if !sign.CheckSigningEnvironment() {
		return fmt.Errorf(
			"signing environment not configured. Required: MACOS_CERTIFICATE_NAME, notarization credentials")
	}
	return nil
}

func universalizerScript(ctx *buildctx.Context) string {
	return filepath.Join(ctx.RootDir, "build", "modules", "package", "universalizer_patched.py")
}

func (Build) Execute(ctx *buildctx.Context) error {
	logx.Info("\n" + strings.Repeat("=", 70))
	logx.Info("🔄 Universal Build Mode (Full Pipeline)")
	logx.Info("Building arm64 + x64, signing, packaging, uploading each...")
	logx.Info("Then merging into universal and processing that too.")
	logx.Info(strings.Repeat("=", 70))

	// Clean all build directories before starting.
	logx.Info("\n🧹 Cleaning build directories...")
	for _, arch := range append(append([]string{}, universalArchitectures...), "universal") {
		dir := filepath.Join(ctx.ChromiumSrc, "out", "Default_"+arch)
		if _, err := os.Stat(dir); err == nil {
			logx.Info("  Removing " + dir)
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
	}
	logx.Success("✅ Build directories cleaned")

	var builtApps []string
	for _, arch := range universalArchitectures {
		logx.Info("\n" + strings.Repeat("=", 70))
		logx.Info("🏗️  Processing architecture: " + arch)
		logx.Info(strings.Repeat("=", 70))

		archCtx, err := archContext(ctx, arch)
		if err != nil {
			return err
		}
		logx.Info("📍 Chromium: " + archCtx.ChromiumVersion)
		logx.Info("📍 Output directory: " + archCtx.OutDir)

		logx.Info(fmt.Sprintf("\n📦 Copying resources for %s...", arch))
		if err := (resources.Copy{}).Execute(archCtx); err != nil {
			return err
		}
		logx.Info(fmt.Sprintf("\n🔧 Configuring %s...", arch))
		if err := (setup.Configure{}).Execute(archCtx); err != nil {
			return err
		}
		logx.Info(fmt.Sprintf("\n🏗️  Compiling %s...", arch))
		if err := (compilemod.Compile{}).Execute(archCtx); err != nil {
			return err
		}

		appPath := archCtx.AppPath()
		if _, err := os.Stat(appPath); err != nil {
			return fmt.Errorf("build failed - app not found: %s", appPath)
		}
		logx.Success(fmt.Sprintf("✅ %s build complete: %s", arch, appPath))
		builtApps = append(builtApps, appPath)

		logx.Info(fmt.Sprintf("\n🔏 Signing %s build...", arch))
		if err := (sign.MacOSSign{}).Execute(archCtx); err != nil {
			return err
		}
		logx.Info(fmt.Sprintf("\n📦 Packaging %s build...", arch))
		if err := (pkg.MacOSPackage{}).Execute(archCtx); err != nil {
			return err
		}
		logx.Info(fmt.Sprintf("\n☁️  Uploading %s artifacts...", arch))
		if err := (storage.Upload{}).Execute(archCtx); err != nil {
			logx.Warning(fmt.Sprintf("⚠️  %s upload failed (non-fatal): %v", arch, err))
		}
	}

	// Merge into universal.
	logx.Info("\n" + strings.Repeat("=", 70))
	logx.Info("🔄 Merging into universal binary...")
	logx.Info(strings.Repeat("=", 70))

	universalDir := filepath.Join(ctx.ChromiumSrc, "out", "Default_universal")
	if err := os.MkdirAll(universalDir, 0o755); err != nil {
		return err
	}
	universalApp := filepath.Join(universalDir, "BrowserOS.app")
	if err := mergeUniversal(ctx, builtApps[0], builtApps[1], universalApp); err != nil {
		return err
	}
	if _, err := os.Stat(universalApp); err != nil {
		return fmt.Errorf("universal binary not found: %s", universalApp)
	}
	logx.Success("✅ Universal binary created: " + universalApp)

	// Sign + package + upload universal.
	universalCtx, err := archContext(ctx, "universal")
	if err != nil {
		return err
	}
	logx.Info("\n🔏 Signing universal build...")
	if err := (sign.MacOSSign{}).Execute(universalCtx); err != nil {
		return err
	}
	logx.Info("\n📦 Packaging universal build...")
	if err := (pkg.MacOSPackage{}).Execute(universalCtx); err != nil {
		return err
	}
	logx.Info("\n☁️  Uploading universal artifacts...")
	if err := (storage.Upload{}).Execute(universalCtx); err != nil {
		logx.Warning(fmt.Sprintf("⚠️  Universal upload failed (non-fatal): %v", err))
	}

	logx.Success("✅ Universal build pipeline complete!")
	return nil
}

// archContext builds a per-arch context with the app path pinned
// (universal.py _create_arch_context / _create_universal_context).
func archContext(base *buildctx.Context, arch string) (*buildctx.Context, error) {
	ctx, err := buildctx.New(buildctx.Options{
		ChromiumSrc:  base.ChromiumSrc,
		Architecture: arch,
		BuildType:    base.BuildType,
		Platform:     &base.Platform,
		RootDir:      base.RootDir,
		Runner:       base.Runner,
	})
	if err != nil {
		return nil, err
	}
	ctx.FixedAppPath = filepath.Join(ctx.ChromiumSrc, "out", "Default_"+arch, ctx.BrowserOSAppName)
	return ctx, nil
}

// mergeUniversal runs the (Python) universalizer script — it stays an
// external tool like gn (universal.py _merge_universal, package/merge.py).
func mergeUniversal(ctx *buildctx.Context, arm64App, x64App, outputApp string) error {
	script := universalizerScript(ctx)
	logx.Info("📱 Input 1 (arm64): " + arm64App)
	logx.Info("📱 Input 2 (x64): " + x64App)
	logx.Info("🎯 Output (universal): " + outputApp)
	logx.Info("🔧 Universalizer: " + script)
	os.RemoveAll(outputApp)
	logx.Info("Running universalizer...")
	_, err := execx.Checked(ctx.Runner, execx.Cmd{
		Args:   []string{"python3", script, arm64App, x64App, outputApp},
		Stream: logx.Out,
	})
	if err != nil {
		return fmt.Errorf("failed to merge architectures into universal binary: %w", err)
	}
	return nil
}
