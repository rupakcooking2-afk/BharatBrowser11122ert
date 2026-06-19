// Package compile ports build/modules/compile/standard.py: the autoninja
// build module with Windows RAM-capped parallelism (commit 8a94455c).
package compile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

const gbPerCompileJob = 4

// JobsConfig pins the inputs of the -j computation for tests.
type JobsConfig struct {
	Getenv     func(string) string
	Platform   platform.Platform
	TotalMemGB func() (float64, bool)
	NumCPU     int
}

func defaultJobsConfig(plat platform.Platform) JobsConfig {
	return JobsConfig{
		Getenv:     os.Getenv,
		Platform:   plat,
		TotalMemGB: totalMemoryGB,
		NumCPU:     runtime.NumCPU(),
	}
}

// ComputeNinjaJobs resolves the -j value: BROWSEROS_NINJA_JOBS override, else
// the Windows RAM cap, else 0 (= autoninja default). Mirrors
// standard.py compute_ninja_jobs.
func ComputeNinjaJobs(cfg JobsConfig) int {
	if override := cfg.Getenv("BROWSEROS_NINJA_JOBS"); override != "" {
		jobs, err := strconv.Atoi(override)
		if err == nil && jobs > 0 {
			logx.Info(fmt.Sprintf("Ninja parallelism: -j %d (BROWSEROS_NINJA_JOBS override)", jobs))
			return jobs
		}
		logx.Warning(fmt.Sprintf("Ignoring invalid BROWSEROS_NINJA_JOBS=%q", override))
	}

	if !cfg.Platform.IsWindows() {
		return 0
	}

	totalGB, ok := cfg.TotalMemGB()
	if !ok {
		logx.Warning("Could not query physical memory; using autoninja default parallelism")
		return 0
	}

	// Windows has no overcommit: official+ThinLTO clang-cl jobs peak ~4 GB
	// each, and one-job-per-core exhausts commit (LLVM ERROR: out of memory).
	jobs := int(totalGB) / gbPerCompileJob
	if jobs < 1 {
		jobs = 1
	}
	if cfg.NumCPU > 0 && jobs > cfg.NumCPU {
		jobs = cfg.NumCPU
	}
	logx.Info(fmt.Sprintf(
		"Ninja parallelism: -j %d (capped by %d GB RAM / %d GB per job; override with BROWSEROS_NINJA_JOBS)",
		jobs, int(totalGB), gbPerCompileJob))
	return jobs
}

// AutoninjaCommand assembles the autoninja argv (standard.py
// autoninja_command).
func AutoninjaCommand(cfg JobsConfig, outDir string, targets []string) []string {
	bin := "autoninja"
	if cfg.Platform.IsWindows() {
		bin = "autoninja.bat"
	}
	cmd := []string{bin, "-C", outDir}
	if jobs := ComputeNinjaJobs(cfg); jobs > 0 {
		cmd = append(cmd, "-j", strconv.Itoa(jobs))
	} else {
		logx.Info("Ninja parallelism: autoninja default")
	}
	return append(cmd, targets...)
}

// Compile is the `compile` pipeline module.
type Compile struct {
	// Jobs overrides the parallelism inputs in tests; nil uses the host.
	Jobs *JobsConfig
}

func NewCompile() *Compile { return &Compile{} }

func (Compile) Name() string        { return "compile" }
func (Compile) Description() string { return "Build BrowserOS using autoninja" }

func (Compile) Validate(ctx *buildctx.Context) error {
	if _, err := os.Stat(ctx.ChromiumSrc); err != nil {
		return fmt.Errorf("chromium source not found: %s", ctx.ChromiumSrc)
	}
	if ctx.BrowserOSChromiumVersion == "" {
		return fmt.Errorf("BrowserOS chromium version not set")
	}
	if _, err := os.Stat(ctx.GNArgsFile()); err != nil {
		return fmt.Errorf("build not configured - args.gn not found: %s", ctx.GNArgsFile())
	}
	return nil
}

func (m Compile) Execute(ctx *buildctx.Context) error {
	logx.Info("\n🔨 Building BrowserOS (this will take a while)...")

	if err := createVersionFile(ctx); err != nil {
		return err
	}

	cfg := defaultJobsConfig(ctx.Platform)
	if m.Jobs != nil {
		cfg = *m.Jobs
	}
	cmd := AutoninjaCommand(cfg, ctx.OutDir, []string{"chrome", "chromedriver"})
	if _, err := execx.Checked(ctx.Runner, execx.Cmd{Args: cmd, Dir: ctx.ChromiumSrc, Stream: logx.Out}); err != nil {
		return err
	}

	// Rename Chromium.app → BrowserOS.app when the build produced the
	// Chromium-branded bundle (standard.py).
	chromiumApp := ctx.ChromiumAppPath()
	browserApp := ctx.AppPath()
	if _, err := os.Stat(chromiumApp); err == nil {
		if _, err := os.Stat(browserApp); os.IsNotExist(err) {
			if err := os.Rename(chromiumApp, browserApp); err != nil {
				return err
			}
		}
	}
	ctx.AddArtifact("built_app", browserApp)

	logx.Success("Build complete!")
	return nil
}

func createVersionFile(ctx *buildctx.Context) error {
	parts := strings.Split(ctx.BrowserOSChromiumVersion, ".")
	if len(parts) != 4 {
		logx.Warning(fmt.Sprintf("Invalid version format: %s", ctx.BrowserOSChromiumVersion))
		return nil
	}
	content := fmt.Sprintf("MAJOR=%s\nMINOR=%s\nBUILD=%s\nPATCH=%s", parts[0], parts[1], parts[2], parts[3])
	versionPath := filepath.Join(ctx.ChromiumSrc, "chrome", "VERSION")
	if err := os.MkdirAll(filepath.Dir(versionPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(versionPath, []byte(content), 0o644); err != nil {
		return err
	}
	logx.Info(fmt.Sprintf("Created VERSION file: %s", ctx.BrowserOSChromiumVersion))
	return nil
}

// BuildTarget builds one extra target, e.g. mini_installer (standard.py
// build_target).
func BuildTarget(ctx *buildctx.Context, target string) error {
	logx.Info(fmt.Sprintf("\n🔨 Building target: %s", target))
	cmd := AutoninjaCommand(defaultJobsConfig(ctx.Platform), ctx.OutDir, []string{target})
	if _, err := execx.Checked(ctx.Runner, execx.Cmd{Args: cmd, Dir: ctx.ChromiumSrc, Stream: logx.Out}); err != nil {
		return err
	}
	logx.Success(fmt.Sprintf("Target %s built successfully", target))
	return nil
}
