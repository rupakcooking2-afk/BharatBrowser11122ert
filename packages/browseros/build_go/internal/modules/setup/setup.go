// Package setup ports build/modules/setup: clean, git_setup, sparkle_setup,
// and configure.
package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/fetch"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/fsx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// stream runs a command checked with merged output streamed to the console,
// matching utils.py run_command.
func stream(ctx *buildctx.Context, dir string, args ...string) error {
	_, err := execx.Checked(ctx.Runner, execx.Cmd{Args: args, Dir: dir, Stream: logx.Out})
	return err
}

func requireChromiumSrc(ctx *buildctx.Context) error {
	if ctx.ChromiumSrc == "" {
		return fmt.Errorf("chromium source not set")
	}
	if _, err := os.Stat(ctx.ChromiumSrc); err != nil {
		return fmt.Errorf("chromium source not found: %s", ctx.ChromiumSrc)
	}
	return nil
}

// === clean (setup/clean.py) ===

type Clean struct{}

func NewClean() *Clean { return &Clean{} }

func (Clean) Name() string        { return "clean" }
func (Clean) Description() string { return "Clean build artifacts and reset git state" }

func (Clean) Validate(ctx *buildctx.Context) error { return requireChromiumSrc(ctx) }

func (Clean) Execute(ctx *buildctx.Context) error {
	logx.Info("🧹 Cleaning build artifacts...")
	outPath := ctx.OutDirAbs()
	if _, err := os.Stat(outPath); err == nil {
		if err := fsx.RemoveAll(outPath); err != nil {
			return err
		}
		logx.Success("Cleaned build directory")
	}

	logx.Info("\n🔀 Resetting git branch and removing tracked files...")
	if err := stream(ctx, ctx.ChromiumSrc, "git", "reset", "--hard", "HEAD"); err != nil {
		return err
	}
	logx.Info("🧹 Resetting dirty submodules...")
	if err := stream(ctx, ctx.ChromiumSrc,
		"git", "submodule", "foreach", "--recursive",
		"git checkout -- . && git clean -fd"); err != nil {
		return err
	}
	logx.Info("🧹 Running git clean with exclusions...")
	if err := stream(ctx, ctx.ChromiumSrc,
		"git", "clean", "-fdx",
		"chrome/", "components/", "third_party/",
		"--exclude=build_tools/", "--exclude=uc_staging/",
		"--exclude=buildtools/", "--exclude=tools/", "--exclude=build/"); err != nil {
		return err
	}
	logx.Success("Git reset and clean complete")

	logx.Info("\n🧹 Cleaning Sparkle build artifacts...")
	if err := fsx.RemoveAll(ctx.SparkleDir()); err != nil {
		return err
	}
	logx.Success("Cleaned Sparkle build directory")
	return nil
}

// === git_setup (setup/git.py GitSetupModule) ===

type GitSetup struct{}

func NewGitSetup() *GitSetup { return &GitSetup{} }

func (GitSetup) Name() string        { return "git_setup" }
func (GitSetup) Description() string { return "Checkout Chromium version and sync dependencies" }

func (GitSetup) Validate(ctx *buildctx.Context) error {
	if err := requireChromiumSrc(ctx); err != nil {
		return err
	}
	if ctx.ChromiumVersion == "" {
		return fmt.Errorf("chromium version not set")
	}
	return nil
}

func (m GitSetup) Execute(ctx *buildctx.Context) error {
	logx.Info(fmt.Sprintf("\n🔀 Setting up Chromium %s...", ctx.ChromiumVersion))

	logx.Info("📥 Fetching all tags from remote...")
	if err := stream(ctx, ctx.ChromiumSrc, "git", "fetch", "--tags", "--force"); err != nil {
		return err
	}

	if err := m.verifyTagExists(ctx); err != nil {
		return err
	}

	logx.Info(fmt.Sprintf("🔀 Checking out tag: %s", ctx.ChromiumVersion))
	if err := stream(ctx, ctx.ChromiumSrc, "git", "checkout", "tags/"+ctx.ChromiumVersion); err != nil {
		return err
	}

	if ctx.Platform.IsLinux() {
		if err := ensureGclientTargetCPUs(ctx, []string{"x64", "arm64"}); err != nil {
			return err
		}
	}

	logx.Info("📥 Syncing dependencies (this may take a while)...")
	gclient := "gclient"
	if ctx.Platform.IsWindows() {
		gclient = "gclient.bat"
	}
	if err := stream(ctx, ctx.ChromiumSrc, gclient, "sync", "-D", "--no-history", "--shallow"); err != nil {
		return err
	}

	logx.Success("Git setup complete")
	return nil
}

func (GitSetup) verifyTagExists(ctx *buildctx.Context) error {
	res, err := ctx.Runner.Run(execx.Cmd{
		Args: []string{"git", "tag", "-l", ctx.ChromiumVersion},
		Dir:  ctx.ChromiumSrc,
	})
	if err != nil {
		return err
	}
	if strings.Contains(res.Stdout, ctx.ChromiumVersion) {
		return nil
	}
	logx.Error(fmt.Sprintf("Tag %s not found!", ctx.ChromiumVersion))
	logx.Info("Available tags (last 10):")
	list, listErr := ctx.Runner.Run(execx.Cmd{
		Args: []string{"git", "tag", "-l", "--sort=-version:refname"},
		Dir:  ctx.ChromiumSrc,
	})
	if listErr == nil {
		tags := strings.Split(strings.TrimSpace(list.Stdout), "\n")
		if len(tags) > 10 {
			tags = tags[:10]
		}
		for _, tag := range tags {
			if tag != "" {
				logx.Info("  " + tag)
			}
		}
	}
	return fmt.Errorf("git tag %s not found", ctx.ChromiumVersion)
}

var targetCPUsRe = regexp.MustCompile(`(?m)^\s*target_cpus\s*=\s*\[([^\]]*)\]`)
var quotedRe = regexp.MustCompile(`['"]([^'"]+)['"]`)

// ensureGclientTargetCPUs idempotently merges target_cpus into ../.gclient
// (git.py _ensure_gclient_target_cpus) so depot_tools fetches cross-arch
// Linux sysroots.
func ensureGclientTargetCPUs(ctx *buildctx.Context, required []string) error {
	gclientPath := filepath.Join(filepath.Dir(ctx.ChromiumSrc), ".gclient")
	raw, err := os.ReadFile(gclientPath)
	if err != nil {
		logx.Warning(fmt.Sprintf(
			"⚠️  .gclient not found at %s; skipping target_cpus bootstrap. "+
				"Cross-arch builds may fail until you run `fetch chromium`.", gclientPath))
		return nil
	}
	content := string(raw)

	if match := targetCPUsRe.FindStringSubmatchIndex(content); match != nil {
		inner := content[match[2]:match[3]]
		var existing []string
		for _, m := range quotedRe.FindAllStringSubmatch(inner, -1) {
			existing = append(existing, m[1])
		}
		missing := false
		for _, arch := range required {
			if !contains(existing, arch) {
				missing = true
			}
		}
		if !missing {
			logx.Info(fmt.Sprintf("✓ .gclient target_cpus already includes %s", pyList(required)))
			return nil
		}
		merged := mergeSorted(existing, required)
		newLine := "target_cpus = " + pyList(merged)
		content = content[:match[0]] + newLine + content[match[1]:]
		logx.Info(fmt.Sprintf("📝 Updating .gclient target_cpus: %s → %s", pyList(existing), pyList(merged)))
	} else {
		content = strings.TrimRight(content, "\n") + "\n\ntarget_cpus = " + pyList(required) + "\n"
		logx.Info(fmt.Sprintf("📝 Adding target_cpus = %s to .gclient", pyList(required)))
	}
	return os.WriteFile(gclientPath, []byte(content), 0o644)
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func mergeSorted(a, b []string) []string {
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		seen[s] = true
	}
	merged := make([]string, 0, len(seen))
	for s := range seen {
		merged = append(merged, s)
	}
	sort.Strings(merged)
	return merged
}

// pyList renders like Python's repr of a list of str — .gclient is a Python
// file, so keep the exact format git.py writes.
func pyList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = "'" + item + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// === sparkle_setup (setup/git.py SparkleSetupModule) ===

type SparkleSetup struct {
	Fetcher fetch.Fetcher
}

func NewSparkleSetup() *SparkleSetup { return &SparkleSetup{} }

func (SparkleSetup) Name() string { return "sparkle_setup" }
func (SparkleSetup) Description() string {
	return "Download and setup Sparkle framework (macOS only)"
}

func (SparkleSetup) Validate(ctx *buildctx.Context) error {
	if !ctx.Platform.IsMacOS() {
		return fmt.Errorf("sparkle setup requires macOS")
	}
	return nil
}

func (m SparkleSetup) Execute(ctx *buildctx.Context) error {
	logx.Info("\n✨ Setting up Sparkle framework...")
	fetcher := m.Fetcher
	if fetcher == nil {
		fetcher = fetch.Default()
	}

	sparkleDir := ctx.SparkleDir()
	if err := fsx.RemoveAll(sparkleDir); err != nil {
		return err
	}
	if err := os.MkdirAll(sparkleDir, 0o755); err != nil {
		return err
	}

	archive := filepath.Join(sparkleDir, "sparkle.tar.xz")
	logx.Info(fmt.Sprintf("Downloading Sparkle from %s...", ctx.SparkleURL()))
	if err := fetcher.Download(ctx.SparkleURL(), archive); err != nil {
		return err
	}

	logx.Info("Extracting Sparkle...")
	// bsdtar on macOS handles .tar.xz natively; Python used tarfile(r:xz).
	if err := stream(ctx, sparkleDir, "tar", "-xf", archive, "-C", sparkleDir); err != nil {
		return err
	}
	if err := os.Remove(archive); err != nil {
		return err
	}

	logx.Success("Sparkle setup complete")
	return nil
}

// === configure (setup/configure.py) ===

type Configure struct{}

func NewConfigure() *Configure { return &Configure{} }

func (Configure) Name() string        { return "configure" }
func (Configure) Description() string { return "Configure build with GN" }

func (Configure) Validate(ctx *buildctx.Context) error {
	if err := requireChromiumSrc(ctx); err != nil {
		return err
	}
	if _, err := os.Stat(ctx.GNFlagsFile()); err != nil {
		return fmt.Errorf("GN flags file not found: %s", ctx.GNFlagsFile())
	}
	return nil
}

func (m Configure) Execute(ctx *buildctx.Context) error {
	logx.Info(fmt.Sprintf("\n⚙️  Configuring %s build for %s...", ctx.BuildType, ctx.Architecture))

	if ctx.Platform.IsLinux() {
		if err := ensureLinuxSysroot(ctx); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(ctx.OutDirAbs(), 0o755); err != nil {
		return err
	}

	flags, err := os.ReadFile(ctx.GNFlagsFile())
	if err != nil {
		return err
	}
	argsContent := string(flags) + fmt.Sprintf("\ntarget_cpu = %q\n", ctx.Architecture)
	if err := os.WriteFile(ctx.GNArgsFile(), []byte(argsContent), 0o644); err != nil {
		return err
	}

	gn := "gn"
	if ctx.Platform.IsWindows() {
		gn = "gn.bat"
	}
	gnArgs := []string{gn, "gen", ctx.OutDir}
	if ctx.BuildType != "debug" {
		gnArgs = append(gnArgs, "--fail-on-unused-args")
	}
	if err := stream(ctx, ctx.ChromiumSrc, gnArgs...); err != nil {
		return err
	}

	logx.Success("Build configured")
	return nil
}

// ensureLinuxSysroot installs the target-arch Debian sysroot before gn gen
// (configure.py _ensure_linux_sysroot); install-sysroot.py is idempotent.
func ensureLinuxSysroot(ctx *buildctx.Context) error {
	script := filepath.Join(ctx.ChromiumSrc, "build", "linux", "sysroot_scripts", "install-sysroot.py")
	if _, err := os.Stat(script); err != nil {
		logx.Warning(fmt.Sprintf(
			"⚠️  install-sysroot.py not found at %s; skipping sysroot bootstrap. "+
				"gn gen will fail if the %s sysroot is missing.", script, ctx.Architecture))
		return nil
	}
	logx.Info(fmt.Sprintf("📦 Ensuring Linux sysroot for %s (idempotent)...", ctx.Architecture))
	return stream(ctx, ctx.ChromiumSrc, "python3", script, "--arch="+ctx.Architecture)
}
