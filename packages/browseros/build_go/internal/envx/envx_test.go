package envx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotenvLine(t *testing.T) {
	cases := []struct {
		line     string
		key, val string
		ok       bool
	}{
		{"FOO=bar", "FOO", "bar", true},
		{"  FOO = bar baz ", "FOO", "bar baz", true},
		{"export FOO=bar", "FOO", "bar", true},
		{`FOO="quoted value"`, "FOO", "quoted value", true},
		{"FOO='single # not comment'", "FOO", "single # not comment", true},
		{"FOO=bar # trailing comment", "FOO", "bar", true},
		{"# full comment", "", "", false},
		{"", "", "", false},
		{"NOEQUALS", "", "", false},
	}
	for _, c := range cases {
		key, val, ok := parseDotenvLine(c.line)
		if ok != c.ok || key != c.key || val != c.val {
			t.Errorf("parseDotenvLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.line, key, val, ok, c.key, c.val, c.ok)
		}
	}
}

func TestLoadDotenvFileDoesNotClobberExistingEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := "PRESET_VAR_TEST=from_file\nFRESH_VAR_TEST=fresh\n"
	if err := os.WriteFile(envPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PRESET_VAR_TEST", "from_process")
	t.Setenv("FRESH_VAR_TEST", "")
	os.Unsetenv("FRESH_VAR_TEST")

	if !loadDotenvFile(envPath) {
		t.Fatal("loadDotenvFile returned false for existing file")
	}
	if got := os.Getenv("PRESET_VAR_TEST"); got != "from_process" {
		t.Errorf("existing env clobbered: %q", got)
	}
	if got := os.Getenv("FRESH_VAR_TEST"); got != "fresh" {
		t.Errorf("fresh var not loaded: %q", got)
	}
}

func TestR2Defaults(t *testing.T) {
	t.Setenv("R2_BUCKET", "")
	os.Unsetenv("R2_BUCKET")
	if got := R2Bucket(); got != "browseros" {
		t.Errorf("R2Bucket default = %q, want browseros", got)
	}
	t.Setenv("R2_BUCKET", "custom")
	if got := R2Bucket(); got != "custom" {
		t.Errorf("R2Bucket override = %q, want custom", got)
	}

	t.Setenv("R2_CDN_BASE_URL", "")
	os.Unsetenv("R2_CDN_BASE_URL")
	if got := R2CDNBaseURL(); got != "http://cdn.browseros.com" {
		t.Errorf("R2CDNBaseURL default = %q", got)
	}

	t.Setenv("R2_ACCOUNT_ID", "abc123")
	if got := R2EndpointURL(); got != "https://abc123.r2.cloudflarestorage.com" {
		t.Errorf("R2EndpointURL = %q", got)
	}
}

func TestDepotToolsWinToolchainDefaultsToZero(t *testing.T) {
	t.Setenv("DEPOT_TOOLS_WIN_TOOLCHAIN", "")
	os.Unsetenv("DEPOT_TOOLS_WIN_TOOLCHAIN")
	if got := DepotToolsWinToolchain(); got != "0" {
		t.Errorf("DepotToolsWinToolchain default = %q, want 0", got)
	}
}
