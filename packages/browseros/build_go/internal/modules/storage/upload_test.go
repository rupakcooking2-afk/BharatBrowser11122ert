package storage

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/r2"
)

var (
	macArm = platform.Platform{OS: "macos", Arch: "arm64"}
	linX64 = platform.Platform{OS: "linux", Arch: "x64"}
	winX64 = platform.Platform{OS: "windows", Arch: "x64"}
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

func fixtureCtx(t *testing.T, plat platform.Platform, arch string) *buildctx.Context {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pyproject.toml"), "name = \"browseros\"\n")
	writeFile(t, filepath.Join(root, "CHROMIUM_VERSION"), "MAJOR=148\nMINOR=0\nBUILD=7778\nPATCH=97\n")
	writeFile(t, filepath.Join(root, "build", "config", "BROWSEROS_BUILD_OFFSET"), "162\n")
	writeFile(t, filepath.Join(root, "resources", "BROWSEROS_VERSION"), "BROWSEROS_MAJOR=0\nBROWSEROS_MINOR=46\nBROWSEROS_BUILD=17\nBROWSEROS_PATCH=0\n")
	ctx, err := buildctx.New(buildctx.Options{
		ChromiumSrc: t.TempDir(), Architecture: arch, BuildType: "release",
		Platform: &plat, RootDir: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestArtifactKeyPerPlatform(t *testing.T) {
	cases := []struct{ filename, platform, want string }{
		{"BrowserOS_v0.31.0_arm64.dmg", "macos", "arm64"},
		{"BrowserOS_v0.31.0_x64.dmg", "macos", "x64"},
		{"BrowserOS_v0.31.0_universal.dmg", "macos", "universal"},
		{"BrowserOS_v0.31.0_x64_installer.exe", "win", "x64_installer"},
		{"BrowserOS_v0.31.0_x64_installer.zip", "win", "x64_zip"},
		{"BrowserOS_v0.31.0_x64.AppImage", "linux", "x64_appimage"},
		{"BrowserOS_v0.31.0_arm64.AppImage", "linux", "arm64_appimage"},
		{"browseros_0.31.0_amd64.deb", "linux", "x64_deb"},
		{"BrowserOS_v0.31.0_arm64.deb", "linux", "arm64_deb"},
		{"strange-artifact.bin", "linux", "strange-artifact"},
	}
	for _, c := range cases {
		if got := ArtifactKey(c.filename, c.platform); got != c.want {
			t.Errorf("ArtifactKey(%s, %s) = %q, want %q", c.filename, c.platform, got, c.want)
		}
	}
}

func TestDetectArtifactsByPlatform(t *testing.T) {
	mac := fixtureCtx(t, macArm, "arm64")
	writeFile(t, filepath.Join(mac.DistDir(), "BrowserOS_v0.46.17_arm64.dmg"), "x")
	writeFile(t, filepath.Join(mac.DistDir(), "ignored.exe"), "x")
	if got := DetectArtifacts(mac); len(got) != 1 || !strings.HasSuffix(got[0], ".dmg") {
		t.Errorf("macos artifacts = %v", got)
	}

	win := fixtureCtx(t, winX64, "x64")
	writeFile(t, filepath.Join(win.DistDir(), "a_installer.exe"), "x")
	writeFile(t, filepath.Join(win.DistDir(), "a_installer.zip"), "x")
	if got := DetectArtifacts(win); len(got) != 2 {
		t.Errorf("win artifacts = %v", got)
	}

	lin := fixtureCtx(t, linX64, "x64")
	writeFile(t, filepath.Join(lin.DistDir(), "a.AppImage"), "x")
	writeFile(t, filepath.Join(lin.DistDir(), "a.deb"), "x")
	if got := DetectArtifacts(lin); len(got) != 2 {
		t.Errorf("linux artifacts = %v", got)
	}
}

func TestGenerateReleaseJSONShape(t *testing.T) {
	ctx := fixtureCtx(t, macArm, "arm64")
	t.Setenv("R2_CDN_BASE_URL", "https://cdn.test")
	ctx.SparkleSignatures["BrowserOS_v0.46.17_arm64.dmg"] = buildctx.SparkleSig{Signature: "sig==", Length: 9}

	artifacts := []map[string]any{{
		"filename":          "BrowserOS_v0.46.17_arm64.dmg",
		"size":              int64(1234),
		"sparkle_signature": "sig==",
		"sparkle_length":    int64(9),
	}}
	release := GenerateReleaseJSON(ctx, artifacts, "macos")

	if release["version"] != "0.46.17" || release["platform"] != "macos" {
		t.Errorf("release header = %v", release)
	}
	if release["sparkle_version"] != "7940.97" {
		t.Errorf("sparkle_version = %v", release["sparkle_version"])
	}
	entry := release["artifacts"].(map[string]any)["arm64"].(map[string]any)
	if entry["url"] != "https://cdn.test/releases/0.46.17/macos/BrowserOS_v0.46.17_arm64.dmg" {
		t.Errorf("url = %v", entry["url"])
	}
	if entry["sparkle_signature"] != "sig==" {
		t.Errorf("sparkle metadata missing: %v", entry)
	}
}

func TestMergeReleaseMetadataKeepsOtherArchArtifacts(t *testing.T) {
	existing := map[string]any{
		"version":   "0.46.17",
		"artifacts": map[string]any{"x64_appimage": map[string]any{"filename": "x64.AppImage"}},
	}
	new := map[string]any{
		"version":    "0.46.17",
		"build_date": "2026-06-10",
		"artifacts":  map[string]any{"arm64_appimage": map[string]any{"filename": "arm64.AppImage"}},
	}
	merged := MergeReleaseMetadata(existing, new)
	artifacts := merged["artifacts"].(map[string]any)
	if len(artifacts) != 2 {
		t.Errorf("merged artifacts = %v", artifacts)
	}
	if merged["build_date"] != "2026-06-10" {
		t.Errorf("new fields should win: %v", merged)
	}
	if got := MergeReleaseMetadata(nil, new); got["build_date"] != "2026-06-10" {
		t.Error("nil existing should return new")
	}
}

// r2Fixture spins an httptest S3 endpoint that records PUTs.
func r2Fixture(t *testing.T) (*r2.Client, *sync.Map) {
	t.Helper()
	var puts sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			puts.Store(strings.TrimPrefix(r.URL.Path, "/browseros/"), body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return &r2.Client{
		Endpoint: server.URL, Bucket: "browseros",
		AccessKey: "ak", SecretKey: "sk", Region: "auto",
		HTTP: server.Client(), Now: time.Now,
	}, &puts
}

func TestUploadReleaseArtifactsUploadsAndWritesReleaseJSON(t *testing.T) {
	t.Setenv("R2_ACCOUNT_ID", "acct")
	t.Setenv("R2_ACCESS_KEY_ID", "ak")
	t.Setenv("R2_SECRET_ACCESS_KEY", "sk")
	t.Setenv("R2_CDN_BASE_URL", "https://cdn.test")

	ctx := fixtureCtx(t, macArm, "arm64")
	writeFile(t, filepath.Join(ctx.DistDir(), "BrowserOS_v0.46.17_arm64.dmg"), "dmg-bytes")
	ctx.SparkleSignatures["BrowserOS_v0.46.17_arm64.dmg"] = buildctx.SparkleSig{Signature: "abc==", Length: 9}

	client, puts := r2Fixture(t)
	module := Upload{Client: client}
	if err := module.Execute(ctx); err != nil {
		t.Fatal(err)
	}

	if _, ok := puts.Load("releases/0.46.17/macos/BrowserOS_v0.46.17_arm64.dmg"); !ok {
		t.Error("dmg not uploaded")
	}
	raw, ok := puts.Load("releases/0.46.17/macos/release.json")
	if !ok {
		t.Fatal("release.json not uploaded")
	}
	var release map[string]any
	if err := json.Unmarshal(raw.([]byte), &release); err != nil {
		t.Fatal(err)
	}
	entry := release["artifacts"].(map[string]any)["arm64"].(map[string]any)
	if entry["sparkle_signature"] != "abc==" || entry["size"] != float64(len("dmg-bytes")) {
		t.Errorf("artifact entry = %v", entry)
	}

	// Local copy written too.
	if _, err := os.Stat(filepath.Join(ctx.DistDir(), "release.json")); err != nil {
		t.Error("local release.json missing")
	}
}

func TestUploadValidateRequiresR2(t *testing.T) {
	for _, name := range []string{"R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	ctx := fixtureCtx(t, macArm, "arm64")
	if err := (Upload{}).Validate(ctx); err == nil || !strings.Contains(err.Error(), "R2 configuration not set") {
		t.Errorf("err = %v", err)
	}
}
