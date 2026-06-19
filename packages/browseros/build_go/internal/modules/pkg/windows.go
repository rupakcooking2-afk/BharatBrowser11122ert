package pkg

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/notify"
)

// WindowsPackage is the package_windows module (package/windows.py).
type WindowsPackage struct{}

func NewWindowsPackage() *WindowsPackage { return &WindowsPackage{} }

func (WindowsPackage) Name() string        { return "package_windows" }
func (WindowsPackage) Description() string { return "Create Windows installer and portable ZIP" }

func (WindowsPackage) Validate(ctx *buildctx.Context) error {
	if !ctx.Platform.IsWindows() {
		return fmt.Errorf("windows packaging requires Windows")
	}
	installer := filepath.Join(ctx.OutDirAbs(), "mini_installer.exe")
	if _, err := os.Stat(installer); err != nil {
		return fmt.Errorf("mini_installer.exe not found: %s", installer)
	}
	return nil
}

func (WindowsPackage) Execute(ctx *buildctx.Context) error {
	logx.Info("\n📦 Creating Windows packages...")

	installerPath, err := createInstaller(ctx)
	if err != nil {
		return err
	}
	zipPath, err := createPortableZip(ctx)
	if err != nil {
		return err
	}

	ctx.AddArtifact("installer", installerPath)
	ctx.AddArtifact("installer_zip", zipPath)
	logx.Success("Windows packages created successfully")

	notify.PackageCreated("📦 Package Created", "Windows packages created successfully",
		map[string]string{
			"Artifacts": filepath.Base(installerPath) + ", " + filepath.Base(zipPath),
			"Version":   ctx.SemanticVersion,
		},
		[]string{"Artifacts", "Version"})
	return nil
}

// createInstaller copies mini_installer.exe to the versioned artifact name.
func createInstaller(ctx *buildctx.Context) (string, error) {
	miniInstaller := filepath.Join(ctx.OutDirAbs(), "mini_installer.exe")
	if err := os.MkdirAll(ctx.DistDir(), 0o755); err != nil {
		return "", err
	}
	installerName, err := ctx.ArtifactName("installer")
	if err != nil {
		return "", err
	}
	installerPath := filepath.Join(ctx.DistDir(), installerName)
	if err := copyFile(miniInstaller, installerPath); err != nil {
		return "", fmt.Errorf("failed to create installer: %w", err)
	}
	logx.Success("Installer created: " + installerName)
	return installerPath, nil
}

// createPortableZip zips the installer for distribution.
func createPortableZip(ctx *buildctx.Context) (string, error) {
	miniInstaller := filepath.Join(ctx.OutDirAbs(), "mini_installer.exe")
	zipName, err := ctx.ArtifactName("installer_zip")
	if err != nil {
		return "", err
	}
	installerName, err := ctx.ArtifactName("installer")
	if err != nil {
		return "", err
	}
	zipPath := filepath.Join(ctx.DistDir(), zipName)

	out, err := os.Create(zipPath)
	if err != nil {
		return "", err
	}
	writer := zip.NewWriter(out)
	entry, err := writer.Create(installerName)
	if err != nil {
		out.Close()
		return "", err
	}
	source, err := os.Open(miniInstaller)
	if err != nil {
		out.Close()
		return "", err
	}
	size, err := io.Copy(entry, source)
	source.Close()
	if err != nil {
		out.Close()
		return "", fmt.Errorf("failed to create installer ZIP: %w", err)
	}
	if err := writer.Close(); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	logx.Info(fmt.Sprintf("Added installer to ZIP (%d MB)", size/(1024*1024)))
	logx.Success("Installer ZIP created: " + zipName)
	return zipPath, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode())
}
