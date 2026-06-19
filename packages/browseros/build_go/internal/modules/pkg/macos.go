// Package pkg ports build/modules/package: platform packaging into DMG
// (macOS), installer+zip (Windows), and AppImage+deb (Linux).
package pkg

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/notify"
	"path/filepath"
)

func run(ctx *buildctx.Context, args ...string) (execx.Result, error) {
	return execx.Checked(ctx.Runner, execx.Cmd{Args: args, Dir: ctx.ChromiumSrc, Stream: logx.Out})
}

func runUnchecked(ctx *buildctx.Context, args ...string) execx.Result {
	res, _ := ctx.Runner.Run(execx.Cmd{Args: args, Dir: ctx.ChromiumSrc, Stream: logx.Out})
	return res
}

// MacOSPackage is the package_macos module (package/macos.py).
type MacOSPackage struct{}

func NewMacOSPackage() *MacOSPackage { return &MacOSPackage{} }

func (MacOSPackage) Name() string        { return "package_macos" }
func (MacOSPackage) Description() string { return "Create DMG package for macOS" }

func (MacOSPackage) Validate(ctx *buildctx.Context) error {
	if !ctx.Platform.IsMacOS() {
		return fmt.Errorf("DMG creation requires macOS")
	}
	if _, err := os.Stat(ctx.AppPath()); err != nil {
		return fmt.Errorf("app not found: %s", ctx.AppPath())
	}
	return nil
}

func (MacOSPackage) Execute(ctx *buildctx.Context) error {
	logx.Info("\n📀 Creating DMG package...")

	appPath := ctx.AppPath()
	dmgName, err := ctx.ArtifactName("dmg")
	if err != nil {
		return err
	}
	dmgPath := filepath.Join(ctx.DistDir(), dmgName)

	// If sign_macos ran (signed_app artifact present), produce a signed +
	// notarized DMG; plain DMG otherwise.
	if _, signed := ctx.Artifact("signed_app"); signed {
		if envx.MacOSCertificateName() == "" {
			return fmt.Errorf("signing environment not configured")
		}
		if err := CreateSignedNotarizedDMG(ctx, appPath, dmgPath, envx.MacOSCertificateName(), "BrowserOS"); err != nil {
			return err
		}
	} else {
		if err := CreateDMG(ctx, appPath, dmgPath, "BrowserOS"); err != nil {
			return err
		}
	}

	ctx.AddArtifact("dmg", dmgPath)
	logx.Success("DMG created: " + dmgName)
	notify.PackageCreated("📀 Package Created", "DMG package created successfully",
		map[string]string{"Artifact": dmgName, "Version": ctx.SemanticVersion, "Path": dmgPath},
		[]string{"Artifact", "Version", "Path"})
	return nil
}

// CreateDMG builds a DMG with Chromium's pkg-dmg (package/macos.py
// create_dmg).
func CreateDMG(ctx *buildctx.Context, appPath, dmgPath, volumeName string) error {
	logx.Info(fmt.Sprintf("\n📀 Creating DMG package: %s", filepath.Base(dmgPath)))
	if _, err := os.Stat(appPath); err != nil {
		return fmt.Errorf("app not found at: %s", appPath)
	}
	if err := os.MkdirAll(filepath.Dir(dmgPath), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(dmgPath); err == nil {
		logx.Info("  Removing existing DMG: " + filepath.Base(dmgPath))
		os.Remove(dmgPath)
	}

	var cmd []string
	usingChromiumTool := false
	if pkgDmg := ctx.PkgDmgPath(); fileExists(pkgDmg) {
		cmd = []string{pkgDmg}
		usingChromiumTool = true
	} else if systemTool, err := exec.LookPath("pkg-dmg"); err == nil {
		cmd = []string{systemTool}
	} else {
		return fmt.Errorf("no pkg-dmg tool found")
	}

	cmd = append(cmd,
		"--sourcefile",
		"--source", appPath,
		"--target", dmgPath,
		"--volname", volumeName,
		"--symlink", "/Applications:/Applications",
		"--format", "UDBZ")
	if usingChromiumTool {
		cmd = append(cmd, "--verbosity", "2")
	}
	if _, err := run(ctx, cmd...); err != nil {
		return fmt.Errorf("failed to create DMG: %w", err)
	}
	logx.Success("DMG created: " + dmgPath)
	return nil
}

// SignDMG codesigns the DMG and verifies it (package/macos.py sign_dmg).
func SignDMG(ctx *buildctx.Context, dmgPath, certificate string) error {
	logx.Info(fmt.Sprintf("\n🔏 Signing DMG: %s", filepath.Base(dmgPath)))
	if _, err := run(ctx, "codesign", "--sign", certificate, "--force", "--timestamp", dmgPath); err != nil {
		return fmt.Errorf("failed to sign DMG: %w", err)
	}
	logx.Info("🔍 Verifying DMG signature...")
	if _, err := run(ctx, "codesign", "-vvv", dmgPath); err != nil {
		return fmt.Errorf("failed to verify DMG signature: %w", err)
	}
	logx.Success("DMG signed successfully")
	return nil
}

// NotarizeDMG submits, staples, and validates the DMG
// (package/macos.py notarize_dmg).
func NotarizeDMG(ctx *buildctx.Context, dmgPath, keychainProfile string) error {
	logx.Info(fmt.Sprintf("\n📤 Notarizing DMG: %s", filepath.Base(dmgPath)))
	logx.Info("📤 Submitting DMG for notarization (this may take a while)...")
	res := runUnchecked(ctx, "xcrun", "notarytool", "submit", dmgPath,
		"--keychain-profile", keychainProfile, "--wait")
	if res.Code != 0 {
		return fmt.Errorf("DMG notarization submission failed")
	}
	if !strings.Contains(res.Stdout, "status: Accepted") {
		return fmt.Errorf("DMG notarization failed - status was not 'Accepted'")
	}
	logx.Success("DMG notarization successful - status: Accepted")

	logx.Info("📎 Stapling notarization ticket to DMG...")
	if res := runUnchecked(ctx, "xcrun", "stapler", "staple", dmgPath); res.Code != 0 {
		return fmt.Errorf("failed to staple notarization ticket to DMG")
	}
	logx.Info("🔍 Verifying DMG stapling...")
	if res := runUnchecked(ctx, "xcrun", "stapler", "validate", dmgPath); res.Code != 0 {
		return fmt.Errorf("DMG stapling verification failed")
	}
	logx.Info("🔍 Performing final security assessment...")
	if res := runUnchecked(ctx, "spctl", "-a", "-vvv", "-t", "open",
		"--context", "context:primary-signature", dmgPath); res.Code != 0 {
		return fmt.Errorf("final security assessment failed")
	}
	logx.Success("Final security assessment passed")
	return nil
}

// CreateSignedNotarizedDMG chains create → sign → notarize
// (package/macos.py create_signed_notarized_dmg).
func CreateSignedNotarizedDMG(ctx *buildctx.Context, appPath, dmgPath, certificate, volumeName string) error {
	logx.Info(strings.Repeat("=", 70))
	logx.Info("📦 Creating signed and notarized DMG package")
	logx.Info(strings.Repeat("=", 70))
	if err := CreateDMG(ctx, appPath, dmgPath, volumeName); err != nil {
		return err
	}
	if err := SignDMG(ctx, dmgPath, certificate); err != nil {
		return err
	}
	if err := NotarizeDMG(ctx, dmgPath, "notarytool-profile"); err != nil {
		return err
	}
	logx.Success("DMG package ready: " + dmgPath)
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
