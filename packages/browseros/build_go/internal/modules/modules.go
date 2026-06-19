// Package modules assembles the registry of build modules, mirroring
// AVAILABLE_MODULES in cli/build.py. Registry keys are the exact module
// names used by build/config/*.yaml.
package modules

import (
	"fmt"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/compile"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/extensions"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/patchmod"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/pkg"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/resources"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/setup"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/sign"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/storage"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/universal"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/pipeline"
)

// notPorted is a placeholder for modules whose port has not landed yet; it
// fails validation with a pointer at the Python tool.
type notPorted struct {
	name        string
	description string
}

func (m notPorted) Name() string        { return m.name }
func (m notPorted) Description() string { return m.description }
func (m notPorted) Validate(*buildctx.Context) error {
	return fmt.Errorf("module %s is not ported to Go yet — use `uv run browseros` meanwhile", m.name)
}
func (m notPorted) Execute(*buildctx.Context) error {
	return fmt.Errorf("module %s is not ported to Go yet", m.name)
}

func placeholder(name, description string) func() pipeline.Module {
	return func() pipeline.Module { return notPorted{name: name, description: description} }
}

// Available returns the full module registry (cli/build.py AVAILABLE_MODULES).
func Available() pipeline.Registry {
	return pipeline.Registry{
		// Setup & Environment
		"clean":         func() pipeline.Module { return setup.NewClean() },
		"git_setup":     func() pipeline.Module { return setup.NewGitSetup() },
		"sparkle_setup": func() pipeline.Module { return setup.NewSparkleSetup() },
		"configure":     func() pipeline.Module { return setup.NewConfigure() },
		// Patches & Resources
		"patches":            func() pipeline.Module { return patchmod.NewPatches() },
		"series_patches":     func() pipeline.Module { return patchmod.NewSeriesPatches() },
		"chromium_replace":   func() pipeline.Module { return resources.NewChromiumReplace() },
		"string_replaces":    func() pipeline.Module { return resources.NewStringReplaces() },
		"download_resources": func() pipeline.Module { return resources.NewDownload() },
		"resources":          func() pipeline.Module { return resources.NewCopy() },
		"bundled_extensions": func() pipeline.Module { return extensions.NewBundled() },
		// Build
		"compile":         func() pipeline.Module { return compile.NewCompile() },
		"universal_build": func() pipeline.Module { return universal.NewBuild() },
		// Sign (platform-specific, validated at runtime)
		"sign_macos":   func() pipeline.Module { return sign.NewMacOSSign() },
		"sign_windows": func() pipeline.Module { return sign.NewWindowsSign() },
		"sign_linux":   func() pipeline.Module { return sign.NewLinuxSign() },
		"sparkle_sign": func() pipeline.Module { return sign.NewSparkleSign() },
		// Package (platform-specific, validated at runtime)
		"package_macos":   func() pipeline.Module { return pkg.NewMacOSPackage() },
		"package_windows": func() pipeline.Module { return pkg.NewWindowsPackage() },
		"package_linux":   func() pipeline.Module { return pkg.NewLinuxPackage() },
		// Storage
		"upload": func() pipeline.Module { return storage.NewUpload() },
	}
}
