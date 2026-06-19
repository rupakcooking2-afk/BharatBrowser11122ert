package compile

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
)

func jobsCfg(plat platform.Platform, env map[string]string, memGB float64, memOK bool, cpus int) JobsConfig {
	return JobsConfig{
		Getenv:     func(k string) string { return env[k] },
		Platform:   plat,
		TotalMemGB: func() (float64, bool) { return memGB, memOK },
		NumCPU:     cpus,
	}
}

func TestComputeNinjaJobsWindowsRAMCap(t *testing.T) {
	// 64 GB / 4 GB-per-job = 16 jobs, capped at 12 cpus.
	if got := ComputeNinjaJobs(jobsCfg(winX64, nil, 64, true, 12)); got != 12 {
		t.Errorf("jobs = %d, want 12 (cpu cap)", got)
	}
	// 32 GB → 8 jobs, plenty of cpus.
	if got := ComputeNinjaJobs(jobsCfg(winX64, nil, 32, true, 64)); got != 8 {
		t.Errorf("jobs = %d, want 8 (RAM cap)", got)
	}
	// Tiny RAM still gets one job.
	if got := ComputeNinjaJobs(jobsCfg(winX64, nil, 2, true, 8)); got != 1 {
		t.Errorf("jobs = %d, want 1", got)
	}
	// RAM query failure → default.
	if got := ComputeNinjaJobs(jobsCfg(winX64, nil, 0, false, 8)); got != 0 {
		t.Errorf("jobs = %d, want 0 (autoninja default)", got)
	}
}

func TestComputeNinjaJobsEnvOverrideAndNonWindows(t *testing.T) {
	env := map[string]string{"BROWSEROS_NINJA_JOBS": "6"}
	if got := ComputeNinjaJobs(jobsCfg(macArm, env, 0, false, 8)); got != 6 {
		t.Errorf("override jobs = %d, want 6", got)
	}
	// Invalid override ignored → non-Windows default.
	env["BROWSEROS_NINJA_JOBS"] = "banana"
	if got := ComputeNinjaJobs(jobsCfg(macArm, env, 0, false, 8)); got != 0 {
		t.Errorf("invalid override = %d, want 0", got)
	}
	// Non-Windows never caps by RAM.
	if got := ComputeNinjaJobs(jobsCfg(macArm, nil, 8, true, 16)); got != 0 {
		t.Errorf("macos jobs = %d, want 0", got)
	}
}

func TestAutoninjaCommandShape(t *testing.T) {
	got := AutoninjaCommand(jobsCfg(winX64, nil, 32, true, 64), `out\Default_x64`, []string{"chrome", "chromedriver"})
	want := []string{"autoninja.bat", "-C", `out\Default_x64`, "-j", "8", "chrome", "chromedriver"}
	if !slices.Equal(got, want) {
		t.Errorf("windows argv = %v, want %v", got, want)
	}

	got = AutoninjaCommand(jobsCfg(macArm, nil, 0, false, 8), "out/Default_arm64", []string{"chrome"})
	want = []string{"autoninja", "-C", "out/Default_arm64", "chrome"}
	if !slices.Equal(got, want) {
		t.Errorf("macos argv = %v, want %v", got, want)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureCtx(t *testing.T) (*buildctx.Context, *execx.RecordingRunner) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pyproject.toml"), "name = \"browseros\"\n")
	writeFile(t, filepath.Join(root, "CHROMIUM_VERSION"), "MAJOR=148\nMINOR=0\nBUILD=7778\nPATCH=97\n")
	writeFile(t, filepath.Join(root, "build", "config", "BROWSEROS_BUILD_OFFSET"), "162\n")
	writeFile(t, filepath.Join(root, "resources", "BROWSEROS_VERSION"), "BROWSEROS_MAJOR=0\nBROWSEROS_MINOR=46\nBROWSEROS_BUILD=17\nBROWSEROS_PATCH=0\n")

	src := filepath.Join(t.TempDir(), "src")
	os.MkdirAll(src, 0o755)

	rec := &execx.RecordingRunner{}
	ctx, err := buildctx.New(buildctx.Options{
		ChromiumSrc: src, Architecture: "arm64", BuildType: "release",
		Platform: &macArm, RootDir: root, Runner: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, ctx.GNArgsFile(), "target_cpu = \"arm64\"\n")
	return ctx, rec
}

func TestCompileWritesVersionAndRunsAutoninja(t *testing.T) {
	ctx, rec := fixtureCtx(t)
	jobs := jobsCfg(macArm, nil, 0, false, 8)
	module := Compile{Jobs: &jobs}

	if err := module.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := module.Execute(ctx); err != nil {
		t.Fatal(err)
	}

	version, err := os.ReadFile(filepath.Join(ctx.ChromiumSrc, "chrome", "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	// BUILD has the +162 offset applied: 7778+162=7940.
	want := "MAJOR=148\nMINOR=0\nBUILD=7940\nPATCH=97"
	if string(version) != want {
		t.Errorf("VERSION = %q, want %q", version, want)
	}

	if got := rec.Argv()[0]; got != "autoninja -C out/Default_arm64 chrome chromedriver" {
		t.Errorf("autoninja argv = %q", got)
	}
	if artifact, ok := ctx.Artifact("built_app"); !ok || !strings.HasSuffix(artifact, "BrowserOS.app") {
		t.Errorf("built_app artifact = (%q, %v)", artifact, ok)
	}
}

func TestCompileRenamesChromiumAppWhenPresent(t *testing.T) {
	ctx, _ := fixtureCtx(t)
	// Simulate the build leaving a Chromium.app bundle.
	writeFile(t, filepath.Join(ctx.ChromiumAppPath(), "Contents", "Info.plist"), "<plist/>")

	jobs := jobsCfg(macArm, nil, 0, false, 8)
	if err := (Compile{Jobs: &jobs}).Execute(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ctx.AppPath(), "Contents", "Info.plist")); err != nil {
		t.Errorf("BrowserOS.app should exist after rename: %v", err)
	}
	if _, err := os.Stat(ctx.ChromiumAppPath()); !os.IsNotExist(err) {
		t.Error("Chromium.app should be gone after rename")
	}
}

func TestCompileValidateRequiresArgsGn(t *testing.T) {
	ctx, _ := fixtureCtx(t)
	os.Remove(ctx.GNArgsFile())
	err := (Compile{}).Validate(ctx)
	if err == nil || !strings.Contains(err.Error(), "args.gn") {
		t.Errorf("err = %v", err)
	}
}
