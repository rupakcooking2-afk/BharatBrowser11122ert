package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// execBuild runs the build command via the root command with a fresh flag
// state and captured logx output.
func execBuild(t *testing.T, args ...string) (string, error) {
	t.Helper()
	prev := buildOpts
	prevOut := logx.Out
	var logBuf bytes.Buffer
	logx.Out = &logBuf
	buildOpts = buildFlags{}
	t.Cleanup(func() {
		buildOpts = prev
		logx.Out = prevOut
	})

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(append([]string{"build"}, args...))
	err := rootCmd.Execute()
	rootCmd.SetArgs(nil)
	return logBuf.String() + out.String(), err
}

func TestBuildRequiresAMode(t *testing.T) {
	_, err := execBuild(t)
	if err == nil || !strings.Contains(err.Error(), "specify --config, --modules, or phase flags") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildModesAreMutuallyExclusive(t *testing.T) {
	_, err := execBuild(t, "--config", "x.yaml", "--modules", "clean")
	if err == nil || !strings.Contains(err.Error(), "only ONE of") {
		t.Errorf("config+modules: %v", err)
	}
	_, err = execBuild(t, "--modules", "clean", "--build")
	if err == nil || !strings.Contains(err.Error(), "only ONE of") {
		t.Errorf("modules+flags: %v", err)
	}
}

func TestBuildConfigModeRejectsArchAndBuildType(t *testing.T) {
	_, err := execBuild(t, "--config", "whatever.yaml", "--arch", "arm64")
	if err == nil || !strings.Contains(err.Error(), "CONFIG MODE: Cannot use --arch") {
		t.Errorf("err = %v", err)
	}
	_, err = execBuild(t, "--config", "whatever.yaml", "--build-type", "release")
	if err == nil || !strings.Contains(err.Error(), "--build-type") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildConfigModeMissingFile(t *testing.T) {
	_, err := execBuild(t, "--config", "/nonexistent/x.yaml")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildListShowsAllRegistryModules(t *testing.T) {
	out, err := execBuild(t, "--list")
	if err != nil {
		t.Fatalf("--list: %v", err)
	}
	// The exact 21 module names from cli/build.py AVAILABLE_MODULES.
	for _, name := range []string{
		"clean", "git_setup", "sparkle_setup", "configure",
		"patches", "series_patches", "chromium_replace", "string_replaces",
		"download_resources", "resources", "bundled_extensions",
		"compile", "universal_build",
		"sign_macos", "sign_windows", "sign_linux", "sparkle_sign",
		"package_macos", "package_windows", "package_linux",
		"upload",
	} {
		if !strings.Contains(out, name) {
			t.Errorf("--list output missing module %q", name)
		}
	}
}
