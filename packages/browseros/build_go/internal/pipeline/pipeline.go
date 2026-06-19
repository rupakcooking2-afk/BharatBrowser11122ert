// Package pipeline ports the module system (build/common/module.py,
// build/common/pipeline.py) and the pipeline executor from cli/build.py.
package pipeline

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/notify"
)

// Module is one discrete step of the build pipeline (common/module.py).
type Module interface {
	Name() string
	Description() string
	// Validate checks preconditions; an error stops the pipeline before
	// Execute (fail fast).
	Validate(ctx *buildctx.Context) error
	// Execute performs the module's work.
	Execute(ctx *buildctx.Context) error
}

// Registry maps the module names used in build/config/*.yaml to constructors.
type Registry map[string]func() Module

// Names returns the sorted registry keys.
func (r Registry) Names() []string {
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// notifyModules mirrors cli/build.py NOTIFY_MODULES — only key modules ping
// Slack to reduce verbosity.
var notifyModules = map[string]bool{
	"compile":         true,
	"sign_macos":      true,
	"sign_windows":    true,
	"sign_linux":      true,
	"package_macos":   true,
	"package_windows": true,
	"package_linux":   true,
	"upload":          true,
}

// Validate checks that every pipeline entry exists in the registry, listing
// all invalid names (common/pipeline.py:validate_pipeline).
func Validate(pipeline []string, registry Registry) error {
	var invalid []string
	for _, name := range pipeline {
		if _, ok := registry[name]; !ok {
			invalid = append(invalid, name)
		}
	}
	if len(invalid) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "invalid module names in pipeline:\n")
	for _, name := range invalid {
		fmt.Fprintf(&b, "  - %s\n", name)
	}
	b.WriteString("\nAvailable modules:\n")
	for _, name := range registry.Names() {
		fmt.Fprintf(&b, "  - %s: %s\n", name, registry[name]().Description())
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

// moduleGroups mirrors common/pipeline.py:show_available_modules grouping.
var moduleGroups = []struct {
	Name    string
	Modules []string
}{
	{"Setup & Environment", []string{"clean", "git_setup", "sparkle_setup", "configure"}},
	{"Patches & Resources", []string{"patches", "chromium_replace", "string_replaces", "resources"}},
	{"Build", []string{"compile"}},
	{"Code Signing", []string{"sign_macos", "sign_windows", "sign_linux"}},
	{"Packaging", []string{"package_macos", "package_windows", "package_linux"}},
	{"Upload", []string{"upload"}},
}

// ShowAvailableModules prints the grouped module list for --list.
func ShowAvailableModules(registry Registry) {
	logx.Info("\n" + strings.Repeat("=", 70))
	logx.Info("Available Build Modules")
	logx.Info(strings.Repeat("=", 70))

	grouped := map[string]bool{}
	for _, group := range moduleGroups {
		var present []string
		for _, name := range group.Modules {
			if _, ok := registry[name]; ok {
				present = append(present, name)
				grouped[name] = true
			}
		}
		if len(present) == 0 {
			continue
		}
		logx.Info("\n" + group.Name + ":")
		logx.Info(strings.Repeat("-", 70))
		for _, name := range present {
			logx.Info(fmt.Sprintf("  %-20s %s", name, registry[name]().Description()))
		}
	}

	var ungrouped []string
	for _, name := range registry.Names() {
		if !grouped[name] {
			ungrouped = append(ungrouped, name)
		}
	}
	if len(ungrouped) > 0 {
		logx.Info("\nOther:")
		logx.Info(strings.Repeat("-", 70))
		for _, name := range ungrouped {
			logx.Info(fmt.Sprintf("  %-20s %s", name, registry[name]().Description()))
		}
	}

	logx.Info("\n" + strings.Repeat("=", 70))
	logx.Info("Example Usage:")
	logx.Info(strings.Repeat("=", 70))
	logx.Info("  browseros build --modules clean,git_setup,configure,compile")
	logx.Info("  browseros build --modules compile,sign_macos,package_macos")
	logx.Info("  browseros build --config release.yaml")
	logx.Info(strings.Repeat("=", 70) + "\n")
}

// Execute runs the pipeline sequentially with fail-fast validation, timing,
// and Slack notifications (cli/build.py:execute_pipeline).
func Execute(ctx *buildctx.Context, pipeline []string, registry Registry, pipelineName string) error {
	start := time.Now()
	notify.PipelineStart(pipelineName, pipeline)

	for _, moduleName := range pipeline {
		logx.Info("\n" + strings.Repeat("=", 70))
		logx.Info(fmt.Sprintf("🔧 Running module: %s", moduleName))
		logx.Info(strings.Repeat("=", 70))

		constructor, ok := registry[moduleName]
		if !ok {
			err := fmt.Errorf("unknown module: %s", moduleName)
			notify.PipelineError(pipelineName, err.Error())
			return err
		}
		module := constructor()

		if notifyModules[moduleName] {
			notify.ModuleStart(moduleName)
		}
		moduleStart := time.Now()

		if err := module.Validate(ctx); err != nil {
			logx.Error(fmt.Sprintf("Validation failed for %s: %v", moduleName, err))
			notify.PipelineError(pipelineName, fmt.Sprintf("%s validation failed: %v", moduleName, err))
			return fmt.Errorf("validation failed for %s: %w", moduleName, err)
		}

		if err := module.Execute(ctx); err != nil {
			logx.Error(fmt.Sprintf("Module %s failed: %v", moduleName, err))
			notify.PipelineError(pipelineName, fmt.Sprintf("%s failed: %v", moduleName, err))
			return fmt.Errorf("module %s failed: %w", moduleName, err)
		}

		duration := time.Since(moduleStart)
		if notifyModules[moduleName] {
			notify.ModuleCompletion(moduleName, duration)
		}
		logx.Success(fmt.Sprintf("Module %s completed in %.1fs", moduleName, duration.Seconds()))
	}

	duration := time.Since(start)
	logx.Info("\n" + strings.Repeat("=", 70))
	logx.Success(fmt.Sprintf("✅ Pipeline completed successfully in %dm %ds",
		int(duration.Minutes()), int(duration.Seconds())%60))
	logx.Info(strings.Repeat("=", 70))
	notify.PipelineEnd(pipelineName, duration)
	notify.Flush(2 * time.Second)
	return nil
}
