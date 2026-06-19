package buildctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/paths"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

var (
	macArm = platform.Platform{OS: "macos", Arch: "arm64"}
	winX64 = platform.Platform{OS: "windows", Arch: "x64"}
	linX64 = platform.Platform{OS: "linux", Arch: "x64"}
)

// fixtureRoot builds a fake packages/browseros tree with version files.
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

func newCtx(t *testing.T, plat platform.Platform, arch, buildType string) *Context {
	t.Helper()
	ctx, err := New(Options{
		ChromiumSrc:  "/tmp/chromium/src",
		Architecture: arch,
		BuildType:    buildType,
		Platform:     &plat,
		RootDir:      fixtureRoot(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestVersionAndOffsetMath(t *testing.T) {
	ctx := newCtx(t, macArm, "arm64", "release")
	if ctx.ChromiumVersion != "148.0.7778.97" {
		t.Errorf("ChromiumVersion = %q", ctx.ChromiumVersion)
	}
	if ctx.BrowserOSBuildOffset != "162" {
		t.Errorf("BrowserOSBuildOffset = %q", ctx.BrowserOSBuildOffset)
	}
	if ctx.BrowserOSChromiumVersion != "148.0.7940.97" {
		t.Errorf("BrowserOSChromiumVersion = %q (BUILD 7778+162=7940)", ctx.BrowserOSChromiumVersion)
	}
	if ctx.SemanticVersion != "0.46.17" {
		t.Errorf("SemanticVersion = %q (patch 0 omitted)", ctx.SemanticVersion)
	}
	sparkle, err := ctx.SparkleBuildVersion()
	if err != nil || sparkle != "7940.97" {
		t.Errorf("SparkleBuildVersion = (%q, %v)", sparkle, err)
	}
}

func TestSemanticVersionIncludesNonZeroPatch(t *testing.T) {
	root := fixtureRoot(t)
	content := "BROWSEROS_MAJOR=0\nBROWSEROS_MINOR=46\nBROWSEROS_BUILD=17\nBROWSEROS_PATCH=3\n"
	if err := os.WriteFile(filepath.Join(root, "resources", "BROWSEROS_VERSION"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, err := New(Options{Platform: &macArm, RootDir: root, ChromiumSrc: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.SemanticVersion != "0.46.17.3" {
		t.Errorf("SemanticVersion = %q, want 0.46.17.3", ctx.SemanticVersion)
	}
}

func TestOutDirPerArchAndPlatformSeparators(t *testing.T) {
	if got := newCtx(t, macArm, "arm64", "debug").OutDir; got != "out/Default_arm64" {
		t.Errorf("macos out dir = %q", got)
	}
	if got := newCtx(t, linX64, "x64", "release").OutDir; got != "out/Default_x64" {
		t.Errorf("linux out dir = %q", got)
	}
	if got := newCtx(t, winX64, "x64", "release").OutDir; got != `out\Default_x64` {
		t.Errorf("windows out dir = %q", got)
	}
	if got := newCtx(t, macArm, "universal", "release").OutDir; got != "out/Default_universal" {
		t.Errorf("universal out dir = %q", got)
	}
}

func TestPlatformAppNames(t *testing.T) {
	mac := newCtx(t, macArm, "arm64", "release")
	if mac.BrowserOSAppName != "BrowserOS.app" || mac.ChromiumAppName != "Chromium.app" {
		t.Errorf("macos app names = %q / %q", mac.BrowserOSAppName, mac.ChromiumAppName)
	}
	win := newCtx(t, winX64, "x64", "release")
	if win.BrowserOSAppName != "BrowserOS.exe" || win.ChromiumAppName != "chrome.exe" {
		t.Errorf("windows app names = %q / %q", win.BrowserOSAppName, win.ChromiumAppName)
	}
	lin := newCtx(t, linX64, "x64", "release")
	if lin.BrowserOSAppName != "browseros" || lin.ChromiumAppName != "chrome" {
		t.Errorf("linux app names = %q / %q", lin.BrowserOSAppName, lin.ChromiumAppName)
	}
}

func TestArtifactNames(t *testing.T) {
	cases := []struct {
		plat     platform.Platform
		arch     string
		artifact string
		want     string
	}{
		{macArm, "arm64", "dmg", "BrowserOS_v0.46.17_arm64.dmg"},
		{linX64, "x64", "appimage", "BrowserOS_v0.46.17_x64.AppImage"},
		{linX64, "x64", "deb", "BrowserOS_v0.46.17_amd64.deb"},
		{linX64, "arm64", "deb", "BrowserOS_v0.46.17_arm64.deb"},
		{winX64, "x64", "installer", "BrowserOS_v0.46.17_x64_installer.exe"},
		{winX64, "x64", "installer_zip", "BrowserOS_v0.46.17_x64_installer.zip"},
	}
	for _, c := range cases {
		ctx := newCtx(t, c.plat, c.arch, "release")
		got, err := ctx.ArtifactName(c.artifact)
		if err != nil || got != c.want {
			t.Errorf("ArtifactName(%s/%s %s) = (%q, %v), want %q", c.plat.OS, c.arch, c.artifact, got, err, c.want)
		}
	}

	if _, err := newCtx(t, macArm, "arm64", "release").ArtifactName("tarball"); err == nil {
		t.Error("unknown artifact type should error")
	}
}

func TestGNFlagsFilePerPlatformAndBuildType(t *testing.T) {
	ctx := newCtx(t, macArm, "arm64", "release")
	want := filepath.Join(ctx.RootDir, "build", "config", "gn", "flags.macos.release.gn")
	if got := ctx.GNFlagsFile(); got != want {
		t.Errorf("GNFlagsFile = %q, want %q", got, want)
	}
	dbg := newCtx(t, linX64, "x64", "debug")
	if got := dbg.GNFlagsFile(); !strings.HasSuffix(got, "flags.linux.debug.gn") {
		t.Errorf("GNFlagsFile = %q", got)
	}
}

func TestAppPathUsesFixedPathOverride(t *testing.T) {
	ctx := newCtx(t, macArm, "arm64", "release")
	if got := ctx.AppPath(); !strings.HasSuffix(got, filepath.Join("out", "Default_arm64", "BrowserOS.app")) {
		t.Errorf("AppPath = %q", got)
	}
	ctx.FixedAppPath = "/fixed/BrowserOS.app"
	if got := ctx.AppPath(); got != "/fixed/BrowserOS.app" {
		t.Errorf("AppPath with FixedAppPath = %q", got)
	}
}

func TestReleasePathAndDistDir(t *testing.T) {
	ctx := newCtx(t, macArm, "arm64", "release")
	if got := ctx.ReleasePath("macos"); got != "releases/0.46.17/macos/" {
		t.Errorf("ReleasePath = %q", got)
	}
	// Python's get_release_path ignores release_version — parity.
	ctx.ReleaseVersion = "0.50.0"
	if got := ctx.ReleasePath("win"); got != "releases/0.46.17/win/" {
		t.Errorf("ReleasePath must ignore ReleaseVersion (parity): %q", got)
	}
	if got := ctx.DistDir(); !strings.HasSuffix(got, filepath.Join("releases", "0.46.17")) {
		t.Errorf("DistDir = %q", got)
	}
}

func TestRealRepoVersionsLoad(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := paths.RootFrom(cwd)
	if err != nil {
		t.Skip("not inside the BrowserOS repo")
	}
	ctx, err := New(Options{RootDir: root, ChromiumSrc: "/x", Platform: &macArm})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ChromiumVersion == "" || ctx.SemanticVersion == "" || ctx.BrowserOSChromiumVersion == "" {
		t.Errorf("real repo versions incomplete: chromium=%q semantic=%q combined=%q",
			ctx.ChromiumVersion, ctx.SemanticVersion, ctx.BrowserOSChromiumVersion)
	}
}
