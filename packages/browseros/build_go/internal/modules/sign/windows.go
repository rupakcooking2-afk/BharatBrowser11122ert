package sign

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	compilemod "github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/compile"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/serverbin"
)

// WindowsSign is the sign_windows pipeline module (sign/windows.py).
type WindowsSign struct{}

func NewWindowsSign() *WindowsSign { return &WindowsSign{} }

func (WindowsSign) Name() string        { return "sign_windows" }
func (WindowsSign) Description() string { return "Sign Windows binaries and create signed installer" }

func (WindowsSign) Validate(ctx *buildctx.Context) error {
	if !ctx.Platform.IsWindows() {
		return fmt.Errorf("windows signing requires Windows")
	}
	if _, err := os.Stat(ctx.OutDirAbs()); err != nil {
		return fmt.Errorf("build output directory not found: %s", ctx.OutDirAbs())
	}
	if envx.CodeSignToolPath() == "" {
		return fmt.Errorf("CODE_SIGN_TOOL_PATH environment variable not set")
	}
	var missing []string
	if envx.ESignerUsername() == "" {
		missing = append(missing, "ESIGNER_USERNAME")
	}
	if envx.ESignerPassword() == "" {
		missing = append(missing, "ESIGNER_PASSWORD")
	}
	if envx.ESignerTOTPSecret() == "" {
		missing = append(missing, "ESIGNER_TOTP_SECRET")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (WindowsSign) Execute(ctx *buildctx.Context) error {
	logx.Info("\n🔏 Signing Windows binaries...")
	outDir := ctx.OutDirAbs()

	// Step 1: sign chrome.exe + server binaries before packaging.
	logx.Info("\nStep 1/3: Signing executables before packaging...")
	candidates := append([]string{filepath.Join(outDir, "chrome.exe")}, ServerBinaryPaths(outDir)...)
	var existing []string
	for _, binary := range candidates {
		if _, err := os.Stat(binary); err == nil {
			existing = append(existing, binary)
			logx.Info("Found binary to sign: " + filepath.Base(binary))
		} else {
			logx.Warning("Binary not found: " + binary)
		}
	}
	if len(existing) == 0 {
		return fmt.Errorf("no binaries found to sign")
	}
	if err := SignWithCodeSignTool(ctx, existing); err != nil {
		return fmt.Errorf("failed to sign executables: %w", err)
	}

	// Step 2: build mini_installer with the signed binaries baked in.
	logx.Info("\nStep 2/3: Building mini_installer with signed binaries...")
	if err := compilemod.BuildTarget(ctx, "mini_installer"); err != nil {
		return fmt.Errorf("failed to build mini_installer: %w", err)
	}

	// Step 3: sign the installer itself.
	logx.Info("\nStep 3/3: Signing mini_installer.exe...")
	installer := filepath.Join(outDir, "mini_installer.exe")
	if _, err := os.Stat(installer); err != nil {
		return fmt.Errorf("mini_installer.exe not found at: %s", installer)
	}
	if err := SignWithCodeSignTool(ctx, []string{installer}); err != nil {
		return fmt.Errorf("failed to sign mini_installer.exe: %w", err)
	}

	ctx.AddArtifact("signed_installer", installer)
	logx.Success("✅ All binaries signed successfully!")
	return nil
}

// ServerBinaryPaths lists the BrowserOS Server binaries to sign
// (windows.py get_browseros_server_binary_paths).
func ServerBinaryPaths(outDir string) []string {
	serverDir := filepath.Join(outDir, "BrowserOSServer", "default", "resources", "bin")
	return serverbin.ExpectedWindowsBinaryPaths(serverDir)
}

// SignWithCodeSignTool signs binaries via SSL.com CodeSignTool
// (windows.py sign_with_codesigntool). The tool is invoked through the shell
// like Python's shell=True call, with the password quoted.
func SignWithCodeSignTool(ctx *buildctx.Context, binaries []string) error {
	logx.Info("Using SSL.com CodeSignTool for signing...")

	var toolPath string
	switch {
	case envx.CodeSignToolExe() != "":
		toolPath = envx.CodeSignToolExe()
	case envx.CodeSignToolPath() != "":
		toolPath = filepath.Join(envx.CodeSignToolPath(), "CodeSignTool.bat")
	default:
		return fmt.Errorf("CODE_SIGN_TOOL_EXE or CODE_SIGN_TOOL_PATH not set")
	}
	if _, err := os.Stat(toolPath); err != nil {
		return fmt.Errorf("CodeSignTool not found at: %s", toolPath)
	}
	if envx.ESignerUsername() == "" || envx.ESignerPassword() == "" || envx.ESignerTOTPSecret() == "" {
		return fmt.Errorf("missing required eSigner environment variables (ESIGNER_USERNAME, ESIGNER_PASSWORD, ESIGNER_TOTP_SECRET)")
	}

	var firstErr error
	for _, binary := range binaries {
		logx.Info(fmt.Sprintf("Signing %s...", filepath.Base(binary)))

		tempOut := filepath.Join(filepath.Dir(binary), "signed_temp")
		if err := os.MkdirAll(tempOut, 0o755); err != nil {
			return err
		}

		parts := []string{toolPath, "sign",
			"-username", envx.ESignerUsername(),
			"-password", fmt.Sprintf("%q", envx.ESignerPassword()),
		}
		if envx.ESignerCredentialID() != "" {
			parts = append(parts, "-credential_id", envx.ESignerCredentialID())
		}
		parts = append(parts,
			"-totp_secret", envx.ESignerTOTPSecret(),
			"-input_file_path", binary,
			"-output_dir_path", tempOut,
			"-override")
		cmdStr := strings.Join(parts, " ")

		shell := []string{"sh", "-c", cmdStr}
		if ctx.Platform.IsWindows() {
			shell = []string{"cmd", "/c", cmdStr}
		}
		res, err := ctx.Runner.Run(execx.Cmd{Args: shell, Dir: filepath.Dir(toolPath), Stream: logx.Out})
		if err != nil {
			return err
		}
		if strings.Contains(res.Stdout, "Error:") {
			logx.Error(fmt.Sprintf("✗ Failed to sign %s - Authentication or signing error", filepath.Base(binary)))
			if firstErr == nil {
				firstErr = fmt.Errorf("CodeSignTool failed for %s", filepath.Base(binary))
			}
			continue
		}

		signed := filepath.Join(tempOut, filepath.Base(binary))
		if _, err := os.Stat(signed); err == nil {
			if err := os.Rename(signed, binary); err != nil {
				return err
			}
			logx.Info(fmt.Sprintf("Moved signed %s to original location", filepath.Base(binary)))
		}
		os.Remove(tempOut) // only removes when empty, matching Python's rmdir

		verify, _ := ctx.Runner.Run(execx.Cmd{Args: []string{
			"powershell", "-Command",
			fmt.Sprintf("(Get-AuthenticodeSignature '%s').Status", binary),
		}})
		if strings.Contains(verify.Stdout, "Valid") {
			logx.Success(fmt.Sprintf("✓ %s signed and verified successfully", filepath.Base(binary)))
		} else {
			logx.Warning(fmt.Sprintf("Could not verify signature for %s", filepath.Base(binary)))
		}
	}
	return firstErr
}

// LinuxSign is the sign_linux no-op module (sign/linux.py).
type LinuxSign struct{}

func NewLinuxSign() *LinuxSign { return &LinuxSign{} }

func (LinuxSign) Name() string                     { return "sign_linux" }
func (LinuxSign) Description() string              { return "Linux code signing (no-op)" }
func (LinuxSign) Validate(*buildctx.Context) error { return nil }
func (LinuxSign) Execute(ctx *buildctx.Context) error {
	logx.Info("Code signing is not required for Linux packages")
	return nil
}
