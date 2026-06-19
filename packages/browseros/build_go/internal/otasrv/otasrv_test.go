package otasrv

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestFilterPlatforms(t *testing.T) {
	if got := FilterPlatforms(""); len(got) != 5 {
		t.Errorf("default platforms = %d, want 5", len(got))
	}
	got := FilterPlatforms("darwin_arm64, windows_x64")
	if len(got) != 2 || got[0].Name != "darwin_arm64" || got[1].Name != "windows_x64" {
		t.Errorf("filtered = %+v", got)
	}
	if got := FilterPlatforms("nonexistent"); len(got) != 0 {
		t.Errorf("unknown filter = %+v", got)
	}
}

func TestGenerateServerAppcastFreshAndMerge(t *testing.T) {
	artifacts := []SignedArtifact{
		{Platform: "darwin_arm64", Signature: "SIGA==", Length: 100, OS: "macos", Arch: "arm64"},
		{Platform: "linux_x64", Signature: "SIGB==", Length: 200, OS: "linux", Arch: "x86_64"},
	}
	content := GenerateServerAppcast("0.0.69", artifacts, "alpha", nil)
	for _, want := range []string{
		"<title>BrowserOS Server (Alpha)</title>",
		"https://cdn.browseros.com/appcast-server.alpha.xml",
		"<sparkle:version>0.0.69</sparkle:version>",
		`url="https://cdn.browseros.com/server/browseros_server_0.0.69_darwin_arm64.zip"`,
		`sparkle:edSignature="SIGA=="`,
		`sparkle:os="linux"`,
		"<!-- macOS arm64 -->",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("appcast missing %q:\n%s", want, content)
		}
	}

	// Round-trip: parse what we generated, then merge a new platform at the
	// same version — existing artifacts and pubDate must survive.
	path := filepath.Join(t.TempDir(), "appcast.xml")
	writeFile(t, path, content)
	existing := ParseExistingAppcast(path)
	if existing == nil || existing.Version != "0.0.69" || len(existing.Artifacts) != 2 {
		t.Fatalf("parsed = %+v", existing)
	}

	newArtifact := []SignedArtifact{{Platform: "windows_x64", Signature: "SIGC==", Length: 300, OS: "windows", Arch: "x86_64"}}
	merged := GenerateServerAppcast("0.0.69", newArtifact, "alpha", existing)
	for _, want := range []string{"SIGA==", "SIGB==", "SIGC==", existing.PubDate} {
		if !strings.Contains(merged, want) {
			t.Errorf("merged appcast missing %q", want)
		}
	}

	// Version bump replaces instead of merging.
	replaced := GenerateServerAppcast("0.0.70", newArtifact, "alpha", existing)
	if strings.Contains(replaced, "SIGA==") {
		t.Error("version change must drop old artifacts")
	}
	if !strings.Contains(replaced, "<sparkle:version>0.0.70</sparkle:version>") {
		t.Error("new version missing")
	}
}

func TestGenerateServerAppcastProdChannel(t *testing.T) {
	content := GenerateServerAppcast("1.0.0", nil, "prod", nil)
	if !strings.Contains(content, "<title>BrowserOS Server</title>") ||
		!strings.Contains(content, "https://cdn.browseros.com/appcast-server.xml") {
		t.Errorf("prod channel header wrong:\n%s", content)
	}
}

func TestCreateServerBundleZipPreservesTreeAndModes(t *testing.T) {
	staging := t.TempDir()
	resourcesDir := filepath.Join(staging, "resources")
	writeFile(t, filepath.Join(resourcesDir, "bin", "browseros_server"), "#!/bin/sh\n")
	os.Chmod(filepath.Join(resourcesDir, "bin", "browseros_server"), 0o755)
	writeFile(t, filepath.Join(resourcesDir, "bin", "third_party", "codex"), "bin")
	writeFile(t, filepath.Join(resourcesDir, "config.json"), "{}")

	zipPath := filepath.Join(t.TempDir(), "bundle.zip")
	if err := CreateServerBundleZip(resourcesDir, zipPath); err != nil {
		t.Fatal(err)
	}

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	names := map[string]os.FileMode{}
	for _, file := range reader.File {
		names[file.Name] = file.Mode()
	}
	for _, want := range []string{"resources/bin/browseros_server", "resources/bin/third_party/codex", "resources/config.json"} {
		if _, ok := names[want]; !ok {
			t.Errorf("zip missing %s: %v", want, names)
		}
	}
	if names["resources/bin/browseros_server"]&0o111 == 0 {
		t.Error("executable bit lost in zip")
	}
}

func TestAppcastPaths(t *testing.T) {
	root := "/repo/packages/browseros"
	if got := AppcastPath(root, "alpha"); !strings.HasSuffix(got, filepath.FromSlash("build/config/appcast/appcast-server.alpha.xml")) {
		t.Errorf("alpha path = %s", got)
	}
	if got := AppcastPath(root, "prod"); !strings.HasSuffix(got, filepath.FromSlash("build/config/appcast/appcast-server.xml")) {
		t.Errorf("prod path = %s", got)
	}
	if AppcastR2Key("alpha") != "appcast-server.alpha.xml" || AppcastR2Key("prod") != "appcast-server.xml" {
		t.Error("appcast R2 keys wrong")
	}
}

func TestParseRealAppcastConfigs(t *testing.T) {
	// Alpha appcast ships with an <item> and must parse; the prod appcast is
	// an empty channel, which Python's parser also treats as "no existing"
	// (parse_existing_appcast returns None when item is missing).
	alphaPath, err := filepath.Abs("../../../build/config/appcast/appcast-server.alpha.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(alphaPath); statErr != nil {
		t.Skip("appcast configs not present")
	}
	alpha := ParseExistingAppcast(alphaPath)
	if alpha == nil || alpha.Version == "" || len(alpha.Artifacts) == 0 {
		t.Errorf("failed to parse real alpha appcast: %+v", alpha)
	}

	prodPath, _ := filepath.Abs("../../../build/config/appcast/appcast-server.xml")
	if prod := ParseExistingAppcast(prodPath); prod != nil {
		t.Errorf("empty-channel prod appcast should parse as nil, got %+v", prod)
	}
}
