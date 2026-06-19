package pkg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/fetch"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/notify"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
	"strings"
)

// linuxArchConfig maps target arch → packaging metadata (linux.py
// LINUX_ARCHITECTURE_CONFIG): appimage_arch feeds appimagetool's ARCH env,
// deb_arch lands in the control file.
var linuxArchConfig = map[string]struct{ AppImageArch, DebArch string }{
	"x64":   {"x86_64", "amd64"},
	"arm64": {"aarch64", "arm64"},
}

// hostAppImageTool maps the BUILD machine arch → tool binary (linux.py
// LINUX_HOST_APPIMAGETOOL): cross-compiling arm64 from x64 still needs the
// x86_64 tool since it executes locally.
var hostAppImageTool = map[string]struct{ Filename, URL string }{
	"x64":   {"appimagetool-x86_64.AppImage", "https://github.com/AppImage/AppImageKit/releases/download/continuous/appimagetool-x86_64.AppImage"},
	"arm64": {"appimagetool-aarch64.AppImage", "https://github.com/AppImage/AppImageKit/releases/download/continuous/appimagetool-aarch64.AppImage"},
}

// LinuxPackage is the package_linux module (package/linux.py).
type LinuxPackage struct {
	Fetcher fetch.Fetcher
	// HostArch overrides the build-machine arch in tests.
	HostArch string
}

func NewLinuxPackage() *LinuxPackage { return &LinuxPackage{} }

func (LinuxPackage) Name() string        { return "package_linux" }
func (LinuxPackage) Description() string { return "Create AppImage and .deb packages for Linux" }

func (LinuxPackage) Validate(ctx *buildctx.Context) error {
	if !ctx.Platform.IsLinux() {
		return fmt.Errorf("linux packaging requires Linux")
	}
	if _, ok := linuxArchConfig[ctx.Architecture]; !ok {
		return fmt.Errorf("unsupported Linux architecture: %s. Supported: arm64, x64", ctx.Architecture)
	}
	chromeBinary := filepath.Join(ctx.OutDirAbs(), ctx.BrowserOSAppName)
	if _, err := os.Stat(chromeBinary); err != nil {
		return fmt.Errorf("chrome binary not found: %s", chromeBinary)
	}
	return nil
}

func (m LinuxPackage) Execute(ctx *buildctx.Context) error {
	logx.Info(fmt.Sprintf("\n📦 Packaging %s %s for Linux (%s)",
		ctx.BrowserOSAppBaseName, ctx.BrowserOSChromiumVersion, ctx.Architecture))

	packageDir := ctx.DistDir()
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		return err
	}

	appimagePath, appimageErr := m.packageAppImage(ctx, packageDir)
	debPath, debErr := m.packageDeb(ctx, packageDir)

	if appimagePath != "" {
		ctx.AddArtifact("appimage", appimagePath)
	}
	if debPath != "" {
		ctx.AddArtifact("deb", debPath)
	}
	if appimagePath == "" && debPath == "" {
		return fmt.Errorf("both AppImage and .deb packaging failed: appimage: %v; deb: %v", appimageErr, debErr)
	}

	logx.Success("✅ Linux packaging complete!")
	switch {
	case appimagePath != "" && debPath != "":
		logx.Info("   Both AppImage and .deb created successfully")
	case appimagePath != "":
		logx.Warning("   Only AppImage created (.deb failed)")
	default:
		logx.Warning("   Only .deb created (AppImage failed)")
	}

	var artifacts []string
	for _, p := range []string{appimagePath, debPath} {
		if p != "" {
			artifacts = append(artifacts, filepath.Base(p))
		}
	}
	notify.PackageCreated("📦 Package Created", "Linux packages created successfully",
		map[string]string{"Artifacts": strings.Join(artifacts, ", "), "Version": ctx.SemanticVersion},
		[]string{"Artifacts", "Version"})
	return nil
}

// browserFilesToCopy mirrors linux.py copy_browser_files exactly.
var browserFilesToCopy = []string{
	"", // placeholder replaced with ctx.BrowserOSAppName
	"chrome_crashpad_handler",
	"chrome_sandbox",
	"chromedriver",
	"libEGL.so",
	"libGLESv2.so",
	"libvk_swiftshader.so",
	"libvulkan.so.1",
	"libqt5_shim.so",
	"libqt6_shim.so",
	"vk_swiftshader_icd.json",
	"icudtl.dat",
	"snapshot_blob.bin",
	"v8_context_snapshot.bin",
	"chrome_100_percent.pak",
	"chrome_200_percent.pak",
	"resources.pak",
}

var browserDirsToCopy = []string{"locales", "MEIPreload", "BrowserOSServer"}

// CopyBrowserFiles stages binaries/resources into targetDir (linux.py
// copy_browser_files). setSandboxSUID controls the chrome_sandbox 4755 bit.
func CopyBrowserFiles(ctx *buildctx.Context, targetDir string, setSandboxSUID bool) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	outDir := ctx.OutDirAbs()

	files := append([]string{ctx.BrowserOSAppName}, browserFilesToCopy[1:]...)
	for _, name := range files {
		src := filepath.Join(outDir, name)
		if _, err := os.Stat(src); err != nil {
			logx.Warning("  ⚠ File not found: " + name)
			continue
		}
		if err := copyFile(src, filepath.Join(targetDir, name)); err != nil {
			return err
		}
		logx.Info("  ✓ Copied " + name)
	}
	for _, dirName := range browserDirsToCopy {
		src := filepath.Join(outDir, dirName)
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			if err := copyTree(src, filepath.Join(targetDir, dirName)); err != nil {
				return err
			}
			logx.Info("  ✓ Copied " + dirName + "/")
		}
	}

	if browserPath := filepath.Join(targetDir, ctx.BrowserOSAppName); fileExists(browserPath) {
		os.Chmod(browserPath, 0o755)
	}
	if sandboxPath := filepath.Join(targetDir, "chrome_sandbox"); fileExists(sandboxPath) {
		if setSandboxSUID {
			os.Chmod(sandboxPath, os.FileMode(0o755)|os.ModeSetuid)
		} else {
			os.Chmod(sandboxPath, 0o755)
		}
	}
	if crashpadPath := filepath.Join(targetDir, "chrome_crashpad_handler"); fileExists(crashpadPath) {
		os.Chmod(crashpadPath, 0o755)
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// CreateDesktopFile writes browseros.desktop (linux.py create_desktop_file).
func CreateDesktopFile(appsDir, execPath string) (string, error) {
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return "", err
	}
	content := fmt.Sprintf(`[Desktop Entry]
Version=1.0
Name=BrowserOS
GenericName=Web Browser
Comment=Browse the World Wide Web
Exec=%s %%U
Terminal=false
Type=Application
Categories=Network;WebBrowser;
MimeType=text/html;text/xml;application/xhtml+xml;application/xml;application/vnd.mozilla.xul+xml;application/rss+xml;application/rdf+xml;image/gif;image/jpeg;image/png;x-scheme-handler/http;x-scheme-handler/https;x-scheme-handler/ftp;x-scheme-handler/chrome;video/webm;application/x-xpinstall;
Icon=browseros
StartupWMClass=chromium-browser
`, execPath)
	desktopFile := filepath.Join(appsDir, "browseros.desktop")
	if err := os.WriteFile(desktopFile, []byte(content), 0o644); err != nil {
		return "", err
	}
	logx.Info("  ✓ Created desktop file")
	return desktopFile, nil
}

// CopyIcons installs hicolor icons at the standard sizes (linux.py copy_icon).
func CopyIcons(ctx *buildctx.Context, iconsDir string) bool {
	iconsBase := filepath.Join(ctx.RootDir, "resources", "icons")
	copied := false
	for _, size := range []int{16, 22, 24, 32, 48, 64, 128, 256} {
		src := filepath.Join(iconsBase, fmt.Sprintf("product_logo_%d.png", size))
		if !fileExists(src) {
			continue
		}
		dest := filepath.Join(iconsDir, fmt.Sprintf("%dx%d", size, size), "apps", "browseros.png")
		if err := copyFile(src, dest); err == nil {
			copied = true
		}
	}
	if copied {
		logx.Info("  ✓ Copied icons (multiple sizes)")
	} else {
		logx.Warning("  ⚠ No icon files found in resources/icons/")
	}
	return copied
}

// PrepareAppDir stages the AppImage AppDir (linux.py prepare_appdir).
func PrepareAppDir(ctx *buildctx.Context, appdir string) error {
	logx.Info("📁 Preparing AppDir structure...")
	appRoot := filepath.Join(appdir, "opt", "browseros")
	usrShare := filepath.Join(appdir, "usr", "share")

	if err := CopyBrowserFiles(ctx, appRoot, true); err != nil {
		return err
	}
	desktopFile, err := CreateDesktopFile(filepath.Join(usrShare, "applications"),
		"/opt/browseros/"+ctx.BrowserOSAppName)
	if err != nil {
		return err
	}
	CopyIcons(ctx, filepath.Join(usrShare, "icons", "hicolor"))

	// Root-level desktop file with Exec=AppRun.
	rootDesktop := filepath.Join(appdir, "browseros.desktop")
	if err := copyFile(desktopFile, rootDesktop); err != nil {
		return err
	}
	content, err := os.ReadFile(rootDesktop)
	if err != nil {
		return err
	}
	updated := strings.Replace(string(content),
		"Exec=/opt/browseros/"+ctx.BrowserOSAppName+" %U", "Exec=AppRun %U", 1)
	if err := os.WriteFile(rootDesktop, []byte(updated), 0o644); err != nil {
		return err
	}

	// Root-level icon (256px preferred).
	iconSrc := filepath.Join(ctx.RootDir, "resources", "icons", "product_logo_256.png")
	if !fileExists(iconSrc) {
		iconSrc = filepath.Join(ctx.RootDir, "resources", "icons", "product_logo.png")
	}
	if fileExists(iconSrc) {
		if err := copyFile(iconSrc, filepath.Join(appdir, "browseros.png")); err != nil {
			return err
		}
	}

	apprun := fmt.Sprintf(`#!/bin/sh
THIS="$(readlink -f "${0}")"
HERE="$(dirname "${THIS}")"
export LD_LIBRARY_PATH="${HERE}"/opt/browseros:$LD_LIBRARY_PATH
export CHROME_WRAPPER="${THIS}"
"${HERE}"/opt/browseros/%s "$@"
`, ctx.BrowserOSAppName)
	apprunFile := filepath.Join(appdir, "AppRun")
	if err := os.WriteFile(apprunFile, []byte(apprun), 0o755); err != nil {
		return err
	}
	logx.Info("  ✓ Created AppRun script")
	return nil
}

func (m LinuxPackage) hostArch() string {
	if m.HostArch != "" {
		return m.HostArch
	}
	return platform.Current().Arch
}

func (m LinuxPackage) downloadAppImageTool(ctx *buildctx.Context) (string, error) {
	toolDir := filepath.Join(ctx.RootDir, "build", "tools")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		return "", err
	}
	tool, ok := hostAppImageTool[m.hostArch()]
	if !ok {
		return "", fmt.Errorf("no appimagetool binary for host arch '%s'. Supported: arm64, x64", m.hostArch())
	}
	toolPath := filepath.Join(toolDir, tool.Filename)
	if fileExists(toolPath) {
		logx.Info(fmt.Sprintf("✓ appimagetool already available (%s)", tool.Filename))
		return toolPath, nil
	}
	logx.Info(fmt.Sprintf("📥 Downloading %s...", tool.Filename))
	fetcher := m.Fetcher
	if fetcher == nil {
		fetcher = fetch.Default()
	}
	if err := fetcher.Download(tool.URL, toolPath); err != nil {
		return "", fmt.Errorf("failed to download appimagetool: %w", err)
	}
	os.Chmod(toolPath, 0o755)
	logx.Success("✓ Downloaded " + tool.Filename)
	return toolPath, nil
}

func (m LinuxPackage) packageAppImage(ctx *buildctx.Context, packageDir string) (string, error) {
	logx.Info("🖼️  Building AppImage...")
	appdir := filepath.Join(packageDir, fmt.Sprintf("%s-%s.AppDir", ctx.BrowserOSAppBaseName, ctx.Architecture))
	os.RemoveAll(appdir)
	defer os.RemoveAll(appdir)

	if err := PrepareAppDir(ctx, appdir); err != nil {
		return "", err
	}
	filename, err := ctx.ArtifactName("appimage")
	if err != nil {
		return "", err
	}
	outputPath := filepath.Join(packageDir, filename)

	toolPath, err := m.downloadAppImageTool(ctx)
	if err != nil {
		return "", err
	}
	archConfig := linuxArchConfig[ctx.Architecture]
	logx.Info("📦 Creating AppImage...")
	res, err := ctx.Runner.Run(execx.Cmd{
		Args:   []string{toolPath, "--comp", "gzip", appdir, outputPath},
		Env:    map[string]string{"ARCH": archConfig.AppImageArch},
		Stream: logx.Out,
	})
	if err != nil || res.Code != 0 {
		return "", fmt.Errorf("failed to create AppImage: %s", res.Stderr)
	}
	os.Chmod(outputPath, 0o755)
	logx.Success("✅ AppImage created: " + filename)
	return outputPath, nil
}

// CreateLauncherScript writes /usr/bin/browseros (linux.py
// create_launcher_script).
func CreateLauncherScript(ctx *buildctx.Context, binDir string) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(`#!/bin/sh
# BrowserOS launcher script
export LD_LIBRARY_PATH=/usr/lib/browseros:$LD_LIBRARY_PATH
exec /usr/lib/browseros/%s "$@"
`, ctx.BrowserOSAppName)
	launcher := filepath.Join(binDir, "browseros")
	if err := os.WriteFile(launcher, []byte(content), 0o755); err != nil {
		return err
	}
	logx.Info("  ✓ Created launcher script")
	return nil
}

// DebVersion normalizes the version string for the control file.
func DebVersion(ctx *buildctx.Context) string {
	version := strings.TrimPrefix(ctx.BrowserOSChromiumVersion, "v")
	version = strings.ReplaceAll(version, " ", "")
	return strings.ReplaceAll(version, "_", ".")
}

// CreateControlFile writes DEBIAN/control (linux.py create_control_file).
func CreateControlFile(ctx *buildctx.Context, debianDir string) error {
	if err := os.MkdirAll(debianDir, 0o755); err != nil {
		return err
	}
	debArch := linuxArchConfig[ctx.Architecture].DebArch
	content := fmt.Sprintf(`Package: browseros
Version: %s
Section: web
Priority: optional
Architecture: %s
Depends: libc6 (>= 2.31), libglib2.0-0, libnss3, libnspr4, libx11-6, libatk1.0-0, libatk-bridge2.0-0, libcups2, libasound2, libdrm2, libgbm1, libpango-1.0-0, libcairo2, libudev1, libxcomposite1, libxdamage1, libxrandr2, libxkbcommon0, libgtk-3-0
Provides: www-browser, gnome-www-browser
Recommends: apparmor
Maintainer: BrowserOS Team <support@browseros.com>
Homepage: https://www.browseros.com/
Description: BrowserOS - The open source agentic browser
 BrowserOS is a privacy-focused web browser built on Chromium,
 designed for modern web browsing with AI capabilities.
`, DebVersion(ctx), debArch)
	if err := os.WriteFile(filepath.Join(debianDir, "control"), []byte(content), 0o644); err != nil {
		return err
	}
	logx.Info("  ✓ Created DEBIAN/control")
	return nil
}

const postinstScript = `#!/bin/sh
# Post-installation script for BrowserOS
set -e

# Set SUID bit on chrome_sandbox for sandboxing support
if [ -f /usr/lib/browseros/chrome_sandbox ]; then
    chmod 4755 /usr/lib/browseros/chrome_sandbox
fi

# Load AppArmor profile (required for Ubuntu 23.10+ user namespace restrictions)
if [ -d /etc/apparmor.d ] && command -v apparmor_parser >/dev/null 2>&1; then
    apparmor_parser -r -T -W /etc/apparmor.d/browseros 2>/dev/null || true
fi

# Register as a selectable default browser
if [ "$1" = "configure" ]; then
    update-alternatives --install /usr/bin/x-www-browser x-www-browser /usr/bin/browseros 40
    update-alternatives --install /usr/bin/gnome-www-browser gnome-www-browser /usr/bin/browseros 40
fi

exit 0
`

const prermScript = `#!/bin/sh
# Pre-removal script for BrowserOS
set -e

# Unregister as default browser
if [ "$1" = "remove" ] || [ "$1" = "deconfigure" ]; then
    update-alternatives --remove x-www-browser /usr/bin/browseros 2>/dev/null || true
    update-alternatives --remove gnome-www-browser /usr/bin/browseros 2>/dev/null || true
fi

# Unload AppArmor profile before files are removed
if command -v apparmor_parser >/dev/null 2>&1 && [ -f /etc/apparmor.d/browseros ]; then
    apparmor_parser -R /etc/apparmor.d/browseros 2>/dev/null || true
fi

exit 0
`

// CreateAppArmorProfile writes the userns profile (linux.py
// create_apparmor_profile, GitHub issue #165).
func CreateAppArmorProfile(ctx *buildctx.Context, apparmorDir string) error {
	if err := os.MkdirAll(apparmorDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(`# AppArmor profile for BrowserOS
# This profile allows everything and only exists to give the application
# a name instead of having the label "unconfined", and to grant permission
# to create unprivileged user namespaces (required for Chromium sandbox on
# Ubuntu 23.10+ and other distros that restrict userns via AppArmor).

abi <abi/4.0>,
include <tunables/global>

profile browseros /usr/lib/browseros/%s flags=(unconfined) {
  userns,

  include if exists <local/browseros>
}
`, ctx.BrowserOSAppName)
	if err := os.WriteFile(filepath.Join(apparmorDir, "browseros"), []byte(content), 0o644); err != nil {
		return err
	}
	logx.Info("  ✓ Created AppArmor profile")
	return nil
}

// CreateMetainfoFile writes the AppStream metainfo (linux.py
// create_metainfo_file).
func CreateMetainfoFile(ctx *buildctx.Context, metainfoDir string) error {
	if err := os.MkdirAll(metainfoDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<component type="desktop-application">
  <id>browseros.desktop</id>
  <launchable type="desktop-id">browseros.desktop</launchable>
  <name>BrowserOS</name>
  <developer id="com.browseros">
    <name>BrowserOS Team</name>
  </developer>
  <summary>The open source agentic browser</summary>
  <metadata_license>CC0-1.0</metadata_license>
  <project_license>BSD-3-Clause and LGPL-2.1+ and Apache-2.0 and IJG and MIT and GPL-2.0+ and ISC and OpenSSL and (MPL-1.1 or GPL-2.0 or LGPL-2.0)</project_license>
  <url type="homepage">https://www.browseros.com/</url>
  <url type="bugtracker">https://github.com/browseros-ai/BrowserOS/issues</url>
  <url type="help">https://docs.browseros.com/</url>
  <description>
    <p>
      BrowserOS is a privacy-focused web browser built on Chromium,
      designed for modern web browsing with AI capabilities.
    </p>
    <p>
      Browse the web with built-in agentic AI features that help you
      automate tasks and interact with web pages intelligently.
    </p>
  </description>
  <categories>
    <category>Network</category>
    <category>WebBrowser</category>
  </categories>
  <keywords>
    <keyword>web browser</keyword>
    <keyword>chromium</keyword>
    <keyword>ai</keyword>
    <keyword>agentic</keyword>
    <keyword>privacy</keyword>
  </keywords>
  <releases>
    <release version="%s" />
  </releases>
  <content_rating type="oars-1.1" />
</component>
`, DebVersion(ctx))
	if err := os.WriteFile(filepath.Join(metainfoDir, "browseros.metainfo.xml"), []byte(content), 0o644); err != nil {
		return err
	}
	logx.Info("  ✓ Created AppStream metainfo")
	return nil
}

// PrepareDebDir stages the .deb tree (linux.py prepare_debdir).
func PrepareDebDir(ctx *buildctx.Context, debdir string) error {
	logx.Info("📁 Preparing .deb directory structure...")
	share := filepath.Join(debdir, "usr", "share")

	if err := CopyBrowserFiles(ctx, filepath.Join(debdir, "usr", "lib", "browseros"), false); err != nil {
		return err
	}
	if err := CreateLauncherScript(ctx, filepath.Join(debdir, "usr", "bin")); err != nil {
		return err
	}
	if _, err := CreateDesktopFile(filepath.Join(share, "applications"), "/usr/bin/browseros"); err != nil {
		return err
	}
	CopyIcons(ctx, filepath.Join(share, "icons", "hicolor"))
	if err := CreateMetainfoFile(ctx, filepath.Join(share, "metainfo")); err != nil {
		return err
	}
	if err := CreateAppArmorProfile(ctx, filepath.Join(debdir, "etc", "apparmor.d")); err != nil {
		return err
	}
	debianDir := filepath.Join(debdir, "DEBIAN")
	if err := CreateControlFile(ctx, debianDir); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(debianDir, "postinst"), []byte(postinstScript), 0o755); err != nil {
		return err
	}
	logx.Info("  ✓ Created DEBIAN/postinst")
	if err := os.WriteFile(filepath.Join(debianDir, "prerm"), []byte(prermScript), 0o755); err != nil {
		return err
	}
	logx.Info("  ✓ Created DEBIAN/prerm")
	logx.Success("✓ .deb directory prepared")
	return nil
}

func (m LinuxPackage) packageDeb(ctx *buildctx.Context, packageDir string) (string, error) {
	logx.Info("📦 Building .deb package...")
	debdir := filepath.Join(packageDir, fmt.Sprintf("%s_%s_deb", ctx.BrowserOSAppBaseName, ctx.Architecture))
	os.RemoveAll(debdir)
	defer os.RemoveAll(debdir)

	if err := PrepareDebDir(ctx, debdir); err != nil {
		return "", err
	}
	filename, err := ctx.ArtifactName("deb")
	if err != nil {
		return "", err
	}
	outputPath := filepath.Join(packageDir, filename)

	logx.Info("📦 Creating .deb package...")
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		return "", fmt.Errorf("dpkg-deb not found. Install with: sudo apt install dpkg")
	}
	res, err := ctx.Runner.Run(execx.Cmd{
		Args:   []string{"dpkg-deb", "--build", "--root-owner-group", debdir, outputPath},
		Stream: logx.Out,
	})
	if err != nil || res.Code != 0 {
		return "", fmt.Errorf("failed to create .deb package")
	}
	os.Chmod(outputPath, 0o644)
	logx.Success("✅ .deb package created: " + filename)
	return outputPath, nil
}
