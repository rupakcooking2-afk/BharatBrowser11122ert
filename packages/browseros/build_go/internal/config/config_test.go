package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/paths"
)

func realConfigDir(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := paths.RootFrom(cwd)
	if err != nil {
		t.Fatalf("not running inside the BrowserOS repo: %v", err)
	}
	return filepath.Join(root, "build", "config")
}

func TestEnvTagSubstitution(t *testing.T) {
	t.Setenv("CONFIG_TEST_SRC", "/tmp/chromium/src")
	var out struct {
		Build struct {
			ChromiumSrc string `yaml:"chromium_src"`
			Missing     string `yaml:"missing"`
		} `yaml:"build"`
	}
	doc := "build:\n  chromium_src: !env CONFIG_TEST_SRC\n  missing: !env CONFIG_TEST_UNSET_VAR\n"
	if err := UnmarshalWithEnv([]byte(doc), &out); err != nil {
		t.Fatalf("UnmarshalWithEnv: %v", err)
	}
	if out.Build.ChromiumSrc != "/tmp/chromium/src" {
		t.Errorf("chromium_src = %q", out.Build.ChromiumSrc)
	}
	if out.Build.Missing != "" {
		t.Errorf("unset !env should yield empty string, got %q", out.Build.Missing)
	}
}

func TestStringOrListAcceptsBothShapes(t *testing.T) {
	var scalar BuildFile
	if err := UnmarshalWithEnv([]byte("build:\n  architecture: arm64\n"), &scalar); err != nil {
		t.Fatal(err)
	}
	if got := scalar.Build.Architectures(); !slices.Equal(got, []string{"arm64"}) {
		t.Errorf("scalar architectures = %v", got)
	}

	var list BuildFile
	if err := UnmarshalWithEnv([]byte("build:\n  architecture: [x64, arm64]\n"), &list); err != nil {
		t.Fatal(err)
	}
	if got := list.Build.Architectures(); !slices.Equal(got, []string{"x64", "arm64"}) {
		t.Errorf("list architectures = %v", got)
	}

	var none BuildFile
	if err := UnmarshalWithEnv([]byte("build:\n  type: debug\n"), &none); err != nil {
		t.Fatal(err)
	}
	if got := none.Build.Architectures(); got != nil {
		t.Errorf("absent architecture should be nil, got %v", got)
	}
}

func TestLoadRealReleaseMacosConfig(t *testing.T) {
	cfg, err := Load(filepath.Join(realConfigDir(t), "release.macos.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Build.Type != "release" {
		t.Errorf("build.type = %q", cfg.Build.Type)
	}
	if got := cfg.Build.Architectures(); !slices.Equal(got, []string{"universal"}) {
		t.Errorf("architectures = %v", got)
	}
	if cfg.GNFlags.File != "build/config/gn/flags.macos.release.gn" {
		t.Errorf("gn_flags.file = %q", cfg.GNFlags.File)
	}
	for _, m := range []string{"clean", "git_setup", "sparkle_setup", "universal_build"} {
		if !slices.Contains(cfg.Modules, m) {
			t.Errorf("modules missing %q: %v", m, cfg.Modules)
		}
	}
	if !slices.Contains(cfg.RequiredEnvs, "MACOS_CERTIFICATE_NAME") {
		t.Errorf("required_envs = %v", cfg.RequiredEnvs)
	}
	if !cfg.Notifications.Slack {
		t.Error("notifications.slack should be true")
	}
}

func TestLoadRealWindowsAndDebugConfigs(t *testing.T) {
	win, err := Load(filepath.Join(realConfigDir(t), "release.windows.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := win.Build.Architectures(); !slices.Equal(got, []string{"x64"}) {
		t.Errorf("windows architectures = %v", got)
	}
	if !slices.Contains(win.Modules, "sign_windows") || !slices.Contains(win.Modules, "package_windows") {
		t.Errorf("windows modules = %v", win.Modules)
	}

	dbg, err := Load(filepath.Join(realConfigDir(t), "debug.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if dbg.Build.Type != "debug" {
		t.Errorf("debug build.type = %q", dbg.Build.Type)
	}
	if dbg.Notifications.Slack {
		t.Error("debug notifications.slack should be false")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("want config-not-found error, got %v", err)
	}
}

func TestValidateRequiredEnvsReportsAllMissing(t *testing.T) {
	t.Setenv("REQ_ENV_SET", "yes")
	err := ValidateRequiredEnvs([]string{"REQ_ENV_SET", "REQ_ENV_MISSING_A", "REQ_ENV_MISSING_B"})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "REQ_ENV_MISSING_A") || !strings.Contains(msg, "REQ_ENV_MISSING_B") {
		t.Errorf("error should list all missing vars: %v", msg)
	}
	if strings.Contains(msg, "REQ_ENV_SET") {
		t.Errorf("error should not list set vars: %v", msg)
	}

	if err := ValidateRequiredEnvs(nil); err != nil {
		t.Errorf("nil list should pass: %v", err)
	}
}
