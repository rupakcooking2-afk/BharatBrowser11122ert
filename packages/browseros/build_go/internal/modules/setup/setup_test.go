package setup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

var (
	macArm = platform.Platform{OS: "macos", Arch: "arm64"}
	winX64 = platform.Platform{OS: "windows", Arch: "x64"}
	linArm = platform.Platform{OS: "linux", Arch: "arm64"}
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureCtx builds a Context over a fake package root + chromium src with a
// recording runner.
func fixtureCtx(t *testing.T, plat platform.Platform, arch, buildType string) (*buildctx.Context, *execx.RecordingRunner) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pyproject.toml"), "name = \"browseros\"\n")
	writeFile(t, filepath.Join(root, "CHROMIUM_VERSION"), "MAJOR=148\nMINOR=0\nBUILD=7778\nPATCH=97\n")
	writeFile(t, filepath.Join(root, "build", "config", "BROWSEROS_BUILD_OFFSET"), "162\n")
	writeFile(t, filepath.Join(root, "resources", "BROWSEROS_VERSION"),
		"BROWSEROS_MAJOR=0\nBROWSEROS_MINOR=46\nBROWSEROS_BUILD=17\nBROWSEROS_PATCH=0\n")
	writeFile(t, filepath.Join(root, "build", "config", "gn", "flags.macos.release.gn"), "is_official_build = true\n")
	writeFile(t, filepath.Join(root, "build", "config", "gn", "flags.macos.debug.gn"), "is_debug = true\n")
	writeFile(t, filepath.Join(root, "build", "config", "gn", "flags.windows.release.gn"), "is_official_build = true\n")
	writeFile(t, filepath.Join(root, "build", "config", "gn", "flags.linux.release.gn"), "is_official_build = true\n")

	chromiumSrc := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(chromiumSrc, 0o755); err != nil {
		t.Fatal(err)
	}

	rec := &execx.RecordingRunner{}
	ctx, err := buildctx.New(buildctx.Options{
		ChromiumSrc:  chromiumSrc,
		Architecture: arch,
		BuildType:    buildType,
		Platform:     &plat,
		RootDir:      root,
		Runner:       rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, rec
}

type fakeFetcher struct {
	urls []string
}

func (f *fakeFetcher) Download(url, dest string) error {
	f.urls = append(f.urls, url)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, []byte("fake-archive"), 0o644)
}

func TestCleanRemovesOutDirAndRunsGitResetSequence(t *testing.T) {
	ctx, rec := fixtureCtx(t, macArm, "arm64", "release")
	outDir := ctx.OutDirAbs()
	writeFile(t, filepath.Join(outDir, "obj", "stale.o"), "stale")
	writeFile(t, filepath.Join(ctx.SparkleDir(), "Sparkle.framework", "f"), "x")

	if err := (Clean{}).Execute(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Error("out dir should be removed")
	}
	if _, err := os.Stat(ctx.SparkleDir()); !os.IsNotExist(err) {
		t.Error("sparkle dir should be removed")
	}

	want := []string{
		"git reset --hard HEAD",
		"git submodule foreach --recursive git checkout -- . && git clean -fd",
		"git clean -fdx chrome/ components/ third_party/ --exclude=build_tools/ --exclude=uc_staging/ --exclude=buildtools/ --exclude=tools/ --exclude=build/",
	}
	if !slices.Equal(rec.Argv(), want) {
		t.Errorf("git sequence = %v, want %v", rec.Argv(), want)
	}
	for _, c := range rec.Cmds {
		if c.Dir != ctx.ChromiumSrc {
			t.Errorf("command dir = %q, want chromium src", c.Dir)
		}
	}
}

func TestCleanValidateRequiresChromiumSrc(t *testing.T) {
	ctx, _ := fixtureCtx(t, macArm, "arm64", "release")
	ctx.ChromiumSrc = "/does/not/exist"
	if err := (Clean{}).Validate(ctx); err == nil {
		t.Error("expected validation error for missing chromium src")
	}
}

func TestGitSetupCommandSequence(t *testing.T) {
	ctx, rec := fixtureCtx(t, macArm, "arm64", "release")
	rec.Handler = func(c execx.Cmd) (execx.Result, error) {
		if strings.HasPrefix(c.String(), "git tag -l") {
			return execx.Result{Stdout: "148.0.7778.97\n"}, nil
		}
		return execx.Result{}, nil
	}

	if err := (GitSetup{}).Execute(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"git fetch --tags --force",
		"git tag -l 148.0.7778.97",
		"git checkout tags/148.0.7778.97",
		"gclient sync -D --no-history --shallow",
	}
	if !slices.Equal(rec.Argv(), want) {
		t.Errorf("sequence = %v, want %v", rec.Argv(), want)
	}
}

func TestGitSetupUsesBatOnWindows(t *testing.T) {
	ctx, rec := fixtureCtx(t, winX64, "x64", "release")
	rec.Handler = func(c execx.Cmd) (execx.Result, error) {
		if strings.HasPrefix(c.String(), "git tag -l") {
			return execx.Result{Stdout: "148.0.7778.97"}, nil
		}
		return execx.Result{}, nil
	}
	if err := (GitSetup{}).Execute(ctx); err != nil {
		t.Fatal(err)
	}
	if got := rec.Argv()[3]; got != "gclient.bat sync -D --no-history --shallow" {
		t.Errorf("windows gclient = %q", got)
	}
}

func TestGitSetupFailsWhenTagMissing(t *testing.T) {
	ctx, rec := fixtureCtx(t, macArm, "arm64", "release")
	rec.Handler = func(c execx.Cmd) (execx.Result, error) {
		return execx.Result{Stdout: ""}, nil // tag list comes back empty
	}
	err := (GitSetup{}).Execute(ctx)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
	for _, argv := range rec.Argv() {
		if strings.HasPrefix(argv, "git checkout") {
			t.Error("must not checkout when tag is missing")
		}
	}
}

func TestEnsureGclientTargetCPUs(t *testing.T) {
	// Appended when absent.
	ctx, _ := fixtureCtx(t, linArm, "arm64", "release")
	gclient := filepath.Join(filepath.Dir(ctx.ChromiumSrc), ".gclient")
	writeFile(t, gclient, "solutions = [\n  { \"name\": \"src\" },\n]\n")
	if err := ensureGclientTargetCPUs(ctx, []string{"x64", "arm64"}); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(gclient)
	if !strings.Contains(string(content), "target_cpus = ['x64', 'arm64']") {
		t.Errorf("append failed:\n%s", content)
	}

	// Merged (sorted) when partially present.
	writeFile(t, gclient, "solutions = []\ntarget_cpus = ['x64']\n")
	if err := ensureGclientTargetCPUs(ctx, []string{"x64", "arm64"}); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(gclient)
	if !strings.Contains(string(content), "target_cpus = ['arm64', 'x64']") {
		t.Errorf("merge failed:\n%s", content)
	}

	// Unchanged when complete.
	writeFile(t, gclient, "solutions = []\ntarget_cpus = ['arm64', 'x64']\n")
	before, _ := os.ReadFile(gclient)
	if err := ensureGclientTargetCPUs(ctx, []string{"x64", "arm64"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(gclient)
	if string(before) != string(after) {
		t.Error(".gclient should be untouched when target_cpus already complete")
	}
}

func TestSparkleSetupMacOnlyDownloadsAndExtracts(t *testing.T) {
	if err := (SparkleSetup{}).Validate(func() *buildctx.Context {
		ctx, _ := fixtureCtx(t, linArm, "arm64", "release")
		return ctx
	}()); err == nil {
		t.Error("sparkle setup must require macOS")
	}

	ctx, rec := fixtureCtx(t, macArm, "arm64", "release")
	fetcher := &fakeFetcher{}
	if err := (SparkleSetup{Fetcher: fetcher}).Execute(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.urls) != 1 || !strings.Contains(fetcher.urls[0], "Sparkle-2.7.0.tar.xz") {
		t.Errorf("download urls = %v", fetcher.urls)
	}
	archive := filepath.Join(ctx.SparkleDir(), "sparkle.tar.xz")
	wantTar := "tar -xf " + archive + " -C " + ctx.SparkleDir()
	if !slices.Contains(rec.Argv(), wantTar) {
		t.Errorf("tar extraction not run: %v", rec.Argv())
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Error("archive should be deleted after extraction")
	}
}

func TestConfigureWritesArgsGnAndRunsGnGen(t *testing.T) {
	ctx, rec := fixtureCtx(t, macArm, "arm64", "release")
	if err := (Configure{}).Execute(ctx); err != nil {
		t.Fatal(err)
	}

	args, err := os.ReadFile(ctx.GNArgsFile())
	if err != nil {
		t.Fatal(err)
	}
	content := string(args)
	if !strings.HasPrefix(content, "is_official_build = true\n") {
		t.Errorf("args.gn should start with gn flags content:\n%s", content)
	}
	if !strings.Contains(content, "target_cpu = \"arm64\"") {
		t.Errorf("args.gn missing target_cpu injection:\n%s", content)
	}

	want := "gn gen out/Default_arm64 --fail-on-unused-args"
	if !slices.Contains(rec.Argv(), want) {
		t.Errorf("gn gen = %v, want %q", rec.Argv(), want)
	}
}

func TestConfigureDebugOmitsFailOnUnusedArgs(t *testing.T) {
	ctx, rec := fixtureCtx(t, macArm, "arm64", "debug")
	if err := (Configure{}).Execute(ctx); err != nil {
		t.Fatal(err)
	}
	for _, argv := range rec.Argv() {
		if strings.Contains(argv, "--fail-on-unused-args") {
			t.Errorf("debug build must not pass --fail-on-unused-args: %v", rec.Argv())
		}
	}
}

func TestConfigureLinuxEnsuresSysroot(t *testing.T) {
	ctx, rec := fixtureCtx(t, linArm, "arm64", "release")
	script := filepath.Join(ctx.ChromiumSrc, "build", "linux", "sysroot_scripts", "install-sysroot.py")
	writeFile(t, script, "# fake")

	if err := (Configure{}).Execute(ctx); err != nil {
		t.Fatal(err)
	}
	if got := rec.Argv()[0]; got != "python3 "+script+" --arch=arm64" {
		t.Errorf("sysroot call = %q", got)
	}
}

func TestConfigureValidateRequiresGNFlagsFile(t *testing.T) {
	ctx, _ := fixtureCtx(t, macArm, "arm64", "release")
	os.Remove(ctx.GNFlagsFile())
	if err := (Configure{}).Validate(ctx); err == nil || !strings.Contains(err.Error(), "GN flags file not found") {
		t.Errorf("err = %v", err)
	}
}
