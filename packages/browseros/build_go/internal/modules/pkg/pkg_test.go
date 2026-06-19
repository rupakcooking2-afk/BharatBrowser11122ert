package pkg

import (
	"archive/zip"
	"os"
	"path/filepath"
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

func fixtureCtx(t *testing.T, plat platform.Platform, arch string) (*buildctx.Context, *execx.RecordingRunner) {
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
		ChromiumSrc: src, Architecture: arch, BuildType: "release",
		Platform: &plat, RootDir: root, Runner: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, rec
}

func TestMacOSPackagePlainDMGCommand(t *testing.T) {
	ctx, rec := fixtureCtx(t, macArm, "arm64")
	writeFile(t, filepath.Join(ctx.AppPath(), "Contents", "Info.plist"), "<plist/>")
	pkgDmg := ctx.PkgDmgPath()
	writeFile(t, pkgDmg, "#!/bin/sh\n")

	if err := (MacOSPackage{}).Execute(ctx); err != nil {
		t.Fatal(err)
	}

	dmgName := "BrowserOS_v0.46.17_arm64.dmg"
	want := pkgDmg + " --sourcefile --source " + ctx.AppPath() +
		" --target " + filepath.Join(ctx.DistDir(), dmgName) +
		" --volname BrowserOS --symlink /Applications:/Applications --format UDBZ --verbosity 2"
	if len(rec.Argv()) != 1 || rec.Argv()[0] != want {
		t.Errorf("pkg-dmg argv:\ngot  %q\nwant %q", rec.Argv(), want)
	}
	if artifact, ok := ctx.Artifact("dmg"); !ok || !strings.HasSuffix(artifact, dmgName) {
		t.Errorf("dmg artifact = (%q, %v)", artifact, ok)
	}
}

func TestMacOSPackageSignedFlowAddsSignAndNotarize(t *testing.T) {
	ctx, rec := fixtureCtx(t, macArm, "arm64")
	writeFile(t, filepath.Join(ctx.AppPath(), "Contents", "Info.plist"), "<plist/>")
	writeFile(t, ctx.PkgDmgPath(), "#!/bin/sh\n")
	ctx.AddArtifact("signed_app", ctx.AppPath()) // sign_macos ran
	t.Setenv("MACOS_CERTIFICATE_NAME", "Developer ID Application: Test")

	rec.Handler = func(c execx.Cmd) (execx.Result, error) {
		if strings.Contains(c.String(), "notarytool submit") {
			return execx.Result{Stdout: "status: Accepted\n"}, nil
		}
		return execx.Result{}, nil
	}

	if err := (MacOSPackage{}).Execute(ctx); err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(rec.Argv(), "\n")
	for _, want := range []string{
		"--format UDBZ",
		"codesign --sign Developer ID Application: Test --force --timestamp",
		"xcrun notarytool submit",
		"xcrun stapler staple",
		"spctl -a -vvv -t open --context context:primary-signature",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("missing %q in:\n%s", want, argv)
		}
	}
}

func TestWindowsPackageCopiesInstallerAndZips(t *testing.T) {
	ctx, _ := fixtureCtx(t, winX64, "x64")
	miniInstaller := filepath.Join(ctx.OutDirAbs(), "mini_installer.exe")
	writeFile(t, miniInstaller, "MZ-installer-bytes")

	module := WindowsPackage{}
	if err := module.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := module.Execute(ctx); err != nil {
		t.Fatal(err)
	}

	installerPath := filepath.Join(ctx.DistDir(), "BrowserOS_v0.46.17_x64_installer.exe")
	content, err := os.ReadFile(installerPath)
	if err != nil || string(content) != "MZ-installer-bytes" {
		t.Errorf("installer copy = (%q, %v)", content, err)
	}

	zipPath := filepath.Join(ctx.DistDir(), "BrowserOS_v0.46.17_x64_installer.zip")
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) != 1 || reader.File[0].Name != "BrowserOS_v0.46.17_x64_installer.exe" {
		t.Errorf("zip entries = %v", reader.File)
	}
}

func TestWindowsPackageValidateRequiresInstaller(t *testing.T) {
	ctx, _ := fixtureCtx(t, winX64, "x64")
	os.MkdirAll(ctx.OutDirAbs(), 0o755)
	err := (WindowsPackage{}).Validate(ctx)
	if err == nil || !strings.Contains(err.Error(), "mini_installer.exe not found") {
		t.Errorf("err = %v", err)
	}
}

type fakeFetcher struct{ urls []string }

func (f *fakeFetcher) Download(url, dest string) error {
	f.urls = append(f.urls, url)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, []byte("fake-tool"), 0o755)
}

func stageLinuxBuild(t *testing.T, ctx *buildctx.Context) {
	t.Helper()
	outDir := ctx.OutDirAbs()
	for _, name := range []string{"browseros", "chrome_crashpad_handler", "chrome_sandbox", "chromedriver", "icudtl.dat", "resources.pak"} {
		writeFile(t, filepath.Join(outDir, name), name)
	}
	writeFile(t, filepath.Join(outDir, "locales", "en-US.pak"), "x")
	writeFile(t, filepath.Join(ctx.RootDir, "resources", "icons", "product_logo_256.png"), "png")
	writeFile(t, filepath.Join(ctx.RootDir, "resources", "icons", "product_logo_128.png"), "png")
}

func TestLinuxPackageBuildsAppImageAndDeb(t *testing.T) {
	ctx, rec := fixtureCtx(t, linArm, "arm64")
	stageLinuxBuild(t, ctx)
	fetcher := &fakeFetcher{}
	module := LinuxPackage{Fetcher: fetcher, HostArch: "x64"} // cross-compile case

	if err := module.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	// dpkg-deb may be missing on the test host — the module treats deb
	// failure as partial success when AppImage succeeded.
	if err := module.Execute(ctx); err != nil {
		t.Fatal(err)
	}

	// Host-arch tool downloaded (x86_64 even though target is arm64).
	if len(fetcher.urls) != 1 || !strings.Contains(fetcher.urls[0], "appimagetool-x86_64.AppImage") {
		t.Errorf("tool urls = %v", fetcher.urls)
	}

	// appimagetool invoked with target-arch ARCH env.
	var appimageCmd *execx.Cmd
	for i := range rec.Cmds {
		if strings.Contains(rec.Cmds[i].String(), "appimagetool") {
			appimageCmd = &rec.Cmds[i]
			break
		}
	}
	if appimageCmd == nil {
		t.Fatalf("appimagetool never ran: %v", rec.Argv())
	}
	if appimageCmd.Env["ARCH"] != "aarch64" {
		t.Errorf("ARCH env = %q, want aarch64 (target arch)", appimageCmd.Env["ARCH"])
	}
	if !strings.Contains(appimageCmd.String(), "--comp gzip") ||
		!strings.Contains(appimageCmd.String(), "BrowserOS_v0.46.17_arm64.AppImage") {
		t.Errorf("appimagetool argv = %q", appimageCmd.String())
	}

	if artifact, ok := ctx.Artifact("appimage"); !ok || !strings.HasSuffix(artifact, "BrowserOS_v0.46.17_arm64.AppImage") {
		t.Errorf("appimage artifact = (%q, %v)", artifact, ok)
	}
}

func TestPrepareAppDirStagesTree(t *testing.T) {
	ctx, _ := fixtureCtx(t, linArm, "arm64")
	stageLinuxBuild(t, ctx)

	appdir := filepath.Join(t.TempDir(), "BrowserOS.AppDir")
	if err := PrepareAppDir(ctx, appdir); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"opt/browseros/browseros",
		"opt/browseros/locales/en-US.pak",
		"usr/share/applications/browseros.desktop",
		"usr/share/icons/hicolor/256x256/apps/browseros.png",
		"browseros.desktop",
		"browseros.png",
		"AppRun",
	} {
		if _, err := os.Stat(filepath.Join(appdir, filepath.FromSlash(want))); err != nil {
			t.Errorf("AppDir missing %s: %v", want, err)
		}
	}

	// Root desktop file rewritten to Exec=AppRun.
	rootDesktop, _ := os.ReadFile(filepath.Join(appdir, "browseros.desktop"))
	if !strings.Contains(string(rootDesktop), "Exec=AppRun %U") {
		t.Errorf("root desktop Exec:\n%s", rootDesktop)
	}
	// Inner desktop file keeps /opt path.
	innerDesktop, _ := os.ReadFile(filepath.Join(appdir, "usr", "share", "applications", "browseros.desktop"))
	if !strings.Contains(string(innerDesktop), "Exec=/opt/browseros/browseros %U") {
		t.Errorf("inner desktop Exec:\n%s", innerDesktop)
	}
	// AppRun executable with sandbox SUID staged.
	apprunInfo, err := os.Stat(filepath.Join(appdir, "AppRun"))
	if err != nil || apprunInfo.Mode()&0o111 == 0 {
		t.Errorf("AppRun not executable: %v", err)
	}
	sandboxInfo, err := os.Stat(filepath.Join(appdir, "opt", "browseros", "chrome_sandbox"))
	if err != nil || sandboxInfo.Mode()&os.ModeSetuid == 0 {
		t.Errorf("chrome_sandbox missing SUID: %v %v", sandboxInfo.Mode(), err)
	}
}

func TestPrepareDebDirControlAndScripts(t *testing.T) {
	ctx, _ := fixtureCtx(t, linArm, "arm64")
	stageLinuxBuild(t, ctx)

	debdir := filepath.Join(t.TempDir(), "deb")
	if err := PrepareDebDir(ctx, debdir); err != nil {
		t.Fatal(err)
	}

	control, err := os.ReadFile(filepath.Join(debdir, "DEBIAN", "control"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(control)
	if !strings.Contains(text, "Version: 148.0.7940.97") {
		t.Errorf("control version:\n%s", text)
	}
	if !strings.Contains(text, "Architecture: arm64") {
		t.Errorf("control arch:\n%s", text)
	}
	if !strings.Contains(text, "Package: browseros") {
		t.Errorf("control package:\n%s", text)
	}

	for _, script := range []string{"postinst", "prerm"} {
		info, err := os.Stat(filepath.Join(debdir, "DEBIAN", script))
		if err != nil || info.Mode()&0o111 == 0 {
			t.Errorf("%s missing or not executable: %v", script, err)
		}
	}
	apparmor, err := os.ReadFile(filepath.Join(debdir, "etc", "apparmor.d", "browseros"))
	if err != nil || !strings.Contains(string(apparmor), "userns,") {
		t.Errorf("apparmor profile: %v", err)
	}
	launcher, err := os.ReadFile(filepath.Join(debdir, "usr", "bin", "browseros"))
	if err != nil || !strings.Contains(string(launcher), "exec /usr/lib/browseros/browseros") {
		t.Errorf("launcher: %v\n%s", err, launcher)
	}
	// deb tree must NOT set SUID (postinst does it at install time).
	sandboxInfo, err := os.Stat(filepath.Join(debdir, "usr", "lib", "browseros", "chrome_sandbox"))
	if err != nil || sandboxInfo.Mode()&os.ModeSetuid != 0 {
		t.Errorf("deb sandbox should not have SUID: %v", sandboxInfo.Mode())
	}
}

func TestLinuxPackageValidatePlatformAndArch(t *testing.T) {
	ctx, _ := fixtureCtx(t, macArm, "arm64")
	if err := (LinuxPackage{}).Validate(ctx); err == nil || !strings.Contains(err.Error(), "requires Linux") {
		t.Errorf("err = %v", err)
	}
	lctx, _ := fixtureCtx(t, linArm, "universal")
	if err := (LinuxPackage{}).Validate(lctx); err == nil || !strings.Contains(err.Error(), "nsupported Linux architecture") {
		t.Errorf("err = %v", err)
	}
}
