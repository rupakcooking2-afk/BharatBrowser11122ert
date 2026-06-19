package resolver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/config"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/paths"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

var (
	macArm = platform.Platform{OS: "macos", Arch: "arm64"}
	winX64 = platform.Platform{OS: "windows", Arch: "x64"}
	linX64 = platform.Platform{OS: "linux", Arch: "x64"}
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pyproject.toml", "name = \"browseros\"\n")
	write("CHROMIUM_VERSION", "MAJOR=148\nMINOR=0\nBUILD=7778\nPATCH=97\n")
	write("build/config/BROWSEROS_BUILD_OFFSET", "162\n")
	write("resources/BROWSEROS_VERSION", "BROWSEROS_MAJOR=0\nBROWSEROS_MINOR=46\nBROWSEROS_BUILD=17\nBROWSEROS_PATCH=0\n")
	return root
}

func opts(t *testing.T, plat platform.Platform) Options {
	return Options{Platform: plat, RootDir: fixtureRoot(t)}
}

func yamlConfig(t *testing.T, doc string) *config.BuildFile {
	t.Helper()
	var cfg config.BuildFile
	if err := config.UnmarshalWithEnv([]byte(doc), &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

func clearEnv(t *testing.T) {
	t.Setenv("CHROMIUM_SRC", "")
	os.Unsetenv("CHROMIUM_SRC")
	t.Setenv("ARCH", "")
	os.Unsetenv("ARCH")
}

func TestDirectModePrecedenceCLIOverEnvOverDefault(t *testing.T) {
	cliSrc := t.TempDir()
	envSrc := t.TempDir()

	// CLI wins over env.
	t.Setenv("CHROMIUM_SRC", envSrc)
	t.Setenv("ARCH", "x64")
	ctxs, err := ResolveContexts(CLIArgs{ChromiumSrc: cliSrc, Arch: "arm64", BuildType: "release"}, nil, opts(t, macArm))
	if err != nil {
		t.Fatal(err)
	}
	if len(ctxs) != 1 || ctxs[0].ChromiumSrc != cliSrc || ctxs[0].Architecture != "arm64" || ctxs[0].BuildType != "release" {
		t.Errorf("CLI should win: %+v", ctxs[0])
	}

	// Env wins when CLI absent.
	ctxs, err = ResolveContexts(CLIArgs{}, nil, opts(t, macArm))
	if err != nil {
		t.Fatal(err)
	}
	if ctxs[0].ChromiumSrc != envSrc || ctxs[0].Architecture != "x64" {
		t.Errorf("env should win when CLI absent: %+v", ctxs[0])
	}
	if ctxs[0].BuildType != "debug" {
		t.Errorf("default build type = %q, want debug", ctxs[0].BuildType)
	}

	// Platform default arch when neither CLI nor env.
	clearEnv(t)
	ctxs, err = ResolveContexts(CLIArgs{ChromiumSrc: cliSrc}, nil, opts(t, macArm))
	if err != nil {
		t.Fatal(err)
	}
	if ctxs[0].Architecture != "arm64" {
		t.Errorf("platform default arch = %q", ctxs[0].Architecture)
	}
}

func TestDirectModeRequiresChromiumSrc(t *testing.T) {
	clearEnv(t)
	_, err := ResolveContexts(CLIArgs{}, nil, opts(t, macArm))
	if err == nil || !strings.Contains(err.Error(), "chromium_src required") {
		t.Errorf("want chromium_src error, got %v", err)
	}

	_, err = ResolveContexts(CLIArgs{ChromiumSrc: "/does/not/exist"}, nil, opts(t, macArm))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("want existence error, got %v", err)
	}
}

func TestDirectModeRejectsInvalidArch(t *testing.T) {
	clearEnv(t)
	_, err := ResolveContexts(CLIArgs{ChromiumSrc: t.TempDir(), Arch: "mips"}, nil, opts(t, macArm))
	if err == nil || !strings.Contains(err.Error(), "invalid architecture 'mips'") {
		t.Errorf("want invalid-arch error, got %v", err)
	}
}

func TestConfigModeYAMLAuthoritativeWithMultiArch(t *testing.T) {
	clearEnv(t)
	src := t.TempDir()
	cfg := yamlConfig(t, "build:\n  type: release\n  architecture: [x64, arm64]\n")

	ctxs, err := ResolveContexts(CLIArgs{ChromiumSrc: src}, cfg, opts(t, linX64))
	if err != nil {
		t.Fatal(err)
	}
	if len(ctxs) != 2 {
		t.Fatalf("want 2 contexts for multi-arch, got %d", len(ctxs))
	}
	if ctxs[0].Architecture != "x64" || ctxs[1].Architecture != "arm64" {
		t.Errorf("arch order = %s, %s", ctxs[0].Architecture, ctxs[1].Architecture)
	}
	for _, ctx := range ctxs {
		if ctx.BuildType != "release" {
			t.Errorf("build type = %q", ctx.BuildType)
		}
	}
}

func TestConfigModeRequiresChromiumSrcFromYAMLOrCLI(t *testing.T) {
	clearEnv(t)
	cfg := yamlConfig(t, "build:\n  type: release\n")
	_, err := ResolveContexts(CLIArgs{}, cfg, opts(t, macArm))
	if err == nil || !strings.Contains(err.Error(), "CONFIG MODE: chromium_src required in YAML") {
		t.Errorf("want CONFIG MODE error, got %v", err)
	}

	// YAML-provided chromium_src works.
	src := t.TempDir()
	cfg2 := yamlConfig(t, "build:\n  type: release\n  chromium_src: "+src+"\n  architecture: arm64\n")
	ctxs, err := ResolveContexts(CLIArgs{}, cfg2, opts(t, macArm))
	if err != nil {
		t.Fatal(err)
	}
	if ctxs[0].ChromiumSrc != src {
		t.Errorf("chromium_src = %q", ctxs[0].ChromiumSrc)
	}
}

func TestConfigModeDefaultsArchToPlatform(t *testing.T) {
	clearEnv(t)
	src := t.TempDir()
	cfg := yamlConfig(t, "build:\n  type: debug\n  chromium_src: "+src+"\n")
	ctxs, err := ResolveContexts(CLIArgs{}, cfg, opts(t, winX64))
	if err != nil {
		t.Fatal(err)
	}
	if ctxs[0].Architecture != "x64" {
		t.Errorf("default arch = %q", ctxs[0].Architecture)
	}
}

func TestResolvePipelineConfigMode(t *testing.T) {
	cfg := yamlConfig(t, "modules:\n  - clean\n  - compile\n")
	got, err := ResolvePipeline(CLIArgs{}, cfg, macArm)
	if err != nil || !slices.Equal(got, []string{"clean", "compile"}) {
		t.Errorf("ResolvePipeline = (%v, %v)", got, err)
	}

	empty := yamlConfig(t, "build:\n  type: release\n")
	if _, err := ResolvePipeline(CLIArgs{}, empty, macArm); err == nil {
		t.Error("config without modules should error")
	}
}

func TestResolvePipelineModesAreExclusive(t *testing.T) {
	if _, err := ResolvePipeline(CLIArgs{}, nil, macArm); err == nil || !strings.Contains(err.Error(), "No pipeline specified") {
		t.Errorf("no mode: %v", err)
	}
	_, err := ResolvePipeline(CLIArgs{Modules: "clean", Build: true}, nil, macArm)
	if err == nil || !strings.Contains(err.Error(), "Cannot use both") {
		t.Errorf("both modes: %v", err)
	}
}

func TestResolvePipelineModulesList(t *testing.T) {
	got, err := ResolvePipeline(CLIArgs{Modules: "clean, compile ,sign_macos"}, nil, macArm)
	if err != nil || !slices.Equal(got, []string{"clean", "compile", "sign_macos"}) {
		t.Errorf("ResolvePipeline = (%v, %v)", got, err)
	}
}

func TestResolvePipelineFlagsAutoOrderAnyPermutation(t *testing.T) {
	// --package --sign --build in any order resolves to build→sign→package.
	got, err := ResolvePipeline(CLIArgs{Package: true, Sign: true, Build: true}, nil, macArm)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"compile", "sign_macos", "package_macos"}
	if !slices.Equal(got, want) {
		t.Errorf("pipeline = %v, want %v", got, want)
	}

	// Full pipeline on Windows resolves platform modules; prep excludes series_patches.
	got, err = ResolvePipeline(CLIArgs{Setup: true, Prep: true, Build: true, Sign: true, Package: true, Upload: true}, nil, winX64)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"clean", "git_setup", "sparkle_setup",
		"download_resources", "resources", "bundled_extensions",
		"chromium_replace", "string_replaces", "patches", "configure",
		"compile", "sign_windows", "package_windows", "upload",
	}
	if !slices.Equal(got, want) {
		t.Errorf("pipeline = %v, want %v", got, want)
	}
	if slices.Contains(got, "series_patches") {
		t.Error("prep must NOT include series_patches")
	}
}

// TestRealConfigsResolveLikePython pins pipeline + arch resolution for every
// real config file the Python tool ships.
func TestRealConfigsResolveLikePython(t *testing.T) {
	cwd, _ := os.Getwd()
	realRoot, err := paths.RootFrom(cwd)
	if err != nil {
		t.Skip("not inside the BrowserOS repo")
	}
	clearEnv(t)
	src := t.TempDir()

	cases := []struct {
		file       string
		plat       platform.Platform
		wantArchs  []string
		wantType   string
		wantFirst  string
		wantLast   string
		wantLength int
	}{
		{"release.macos.yaml", macArm, []string{"universal"}, "release", "clean", "universal_build", 10},
		{"release.macos.arm64.yaml", macArm, []string{"arm64"}, "release", "clean", "upload", 15},
		{"release.macos.arm64.noupload.yaml", macArm, []string{"arm64"}, "release", "clean", "package_macos", 14},
		{"release.linux.yaml", linX64, []string{"x64"}, "release", "clean", "upload", 13},
		{"release.windows.yaml", winX64, []string{"x64"}, "release", "clean", "upload", 15},
		{"debug.yaml", macArm, []string{"arm64"}, "debug", "git_setup", "package_macos", 9},
		{"sign.macos.yaml", macArm, []string{"universal"}, "release", "sign_macos", "package_macos", 2},
		{"sign.windows.yaml", winX64, []string{"x64"}, "release", "sign_windows", "package_windows", 2},
		{"package.linux.yaml", linX64, nil, "release", "package_linux", "package_linux", 1},
	}

	for _, c := range cases {
		cfg, err := config.Load(filepath.Join(realRoot, "build", "config", c.file))
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}

		wantArchs := c.wantArchs
		if wantArchs == nil {
			wantArchs = []string{c.plat.Arch} // platform default when YAML omits arch
		}
		ctxs, err := ResolveContexts(CLIArgs{ChromiumSrc: src}, cfg, Options{Platform: c.plat, RootDir: realRoot})
		if err != nil {
			t.Errorf("%s contexts: %v", c.file, err)
			continue
		}
		var gotArchs []string
		for _, ctx := range ctxs {
			gotArchs = append(gotArchs, ctx.Architecture)
			if ctx.BuildType != c.wantType {
				t.Errorf("%s build type = %q, want %q", c.file, ctx.BuildType, c.wantType)
			}
		}
		if !slices.Equal(gotArchs, wantArchs) {
			t.Errorf("%s archs = %v, want %v", c.file, gotArchs, wantArchs)
		}

		modulesList, err := ResolvePipeline(CLIArgs{}, cfg, c.plat)
		if err != nil {
			t.Errorf("%s pipeline: %v", c.file, err)
			continue
		}
		if len(modulesList) != c.wantLength {
			t.Errorf("%s pipeline length = %d (%v), want %d", c.file, len(modulesList), modulesList, c.wantLength)
		}
		if modulesList[0] != c.wantFirst || modulesList[len(modulesList)-1] != c.wantLast {
			t.Errorf("%s pipeline = %v, want first %s last %s", c.file, modulesList, c.wantFirst, c.wantLast)
		}
	}
}
