package release

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/r2"
)

func TestVersionSortDescending(t *testing.T) {
	versions := []string{"0.9.0", "0.10.0", "0.46.17", "0.46.2"}
	want := "0.46.17"
	// Build the same comparator ListAllVersions uses.
	best := versions[0]
	for _, v := range versions[1:] {
		if versionLess(best, v) {
			best = v
		}
	}
	if best != want {
		t.Errorf("max version = %s, want %s (numeric, not lexicographic)", best, want)
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{
		512:           "512 B",
		2048:          "2 KB",
		5 * (1 << 20): "5 MB",
		2 * (1 << 30): "2.0 GB",
	}
	for size, want := range cases {
		if got := FormatSize(size); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", size, got, want)
		}
	}
}

func TestGenerateAppcastItemRendersEnclosure(t *testing.T) {
	artifact := map[string]any{
		"url":               "https://cdn.test/releases/0.46.17/macos/BrowserOS_v0.46.17_arm64.dmg",
		"sparkle_signature": "SIG==",
		"sparkle_length":    float64(12345),
	}
	item := GenerateAppcastItem(artifact, "0.46.17", "7940.97", "2026-06-10T12:00:00+00:00")
	for _, want := range []string{
		"<title>BrowserOS - 0.46.17</title>",
		"<sparkle:version>7940.97</sparkle:version>",
		"<sparkle:shortVersionString>0.46.17</sparkle:shortVersionString>",
		`sparkle:edSignature="SIG=="`,
		`length="12345"`,
		"<sparkle:minimumSystemVersion>10.15</sparkle:minimumSystemVersion>",
		"Wed, 10 Jun 2026 12:00:00 +0000",
	} {
		if !strings.Contains(item, want) {
			t.Errorf("appcast item missing %q:\n%s", want, item)
		}
	}
}

func TestGenerateReleaseNotes(t *testing.T) {
	metadata := map[string]map[string]any{
		"macos": {
			"chromium_version": "148.0.7778.97",
			"artifacts": map[string]any{
				"arm64": map[string]any{"filename": "a.dmg", "url": "https://cdn/a.dmg"},
			},
		},
		"linux": {
			"artifacts": map[string]any{
				"x64_deb": map[string]any{"filename": "b.deb", "url": "https://cdn/b.deb"},
			},
		},
	}
	notes := GenerateReleaseNotes("0.46.17", metadata)
	for _, want := range []string{"## BrowserOS v0.46.17", "Chromium version: 148.0.7778.97",
		"**macOS:**", "[a.dmg](https://cdn/a.dmg)", "**Linux:**", "[b.deb](https://cdn/b.deb)"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q:\n%s", want, notes)
		}
	}
}

func r2Fixture(t *testing.T, handler http.HandlerFunc) *r2.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &r2.Client{
		Endpoint: server.URL, Bucket: "browseros",
		AccessKey: "ak", SecretKey: "sk", Region: "auto",
		HTTP: server.Client(), Now: time.Now,
	}
}

func TestPublishCopiesMappedArtifacts(t *testing.T) {
	var copies []string
	client := r2Fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "macos/release.json"):
			w.Write([]byte(`{"version":"0.46.17","artifacts":{
				"arm64":{"filename":"BrowserOS_v0.46.17_arm64.dmg","url":"u"},
				"unmapped_key":{"filename":"x.bin","url":"u"}}}`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut:
			copies = append(copies, r.Header.Get("x-amz-copy-source")+" -> "+strings.TrimPrefix(r.URL.Path, "/browseros/"))
			w.Write([]byte("<CopyObjectResult/>"))
		}
	})

	deps := &Deps{Client: client}
	if err := deps.Publish("0.46.17"); err != nil {
		t.Fatal(err)
	}
	if len(copies) != 1 {
		t.Fatalf("copies = %v", copies)
	}
	want := "/browseros/releases/0.46.17/macos/BrowserOS_v0.46.17_arm64.dmg -> download/BrowserOS-arm64.dmg"
	if copies[0] != want {
		t.Errorf("copy = %q, want %q", copies[0], want)
	}
}

func TestGithubCreateCommandSequence(t *testing.T) {
	client := r2Fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "macos/release.json") {
			w.Write([]byte(`{"version":"0.46.17","chromium_version":"148.0.7778.97","artifacts":{}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	rec := &execx.RecordingRunner{}
	deps := &Deps{Client: client, Runner: rec}

	err := deps.GithubCreate(GithubOptions{Version: "0.46.17", Repo: "browseros-ai/BrowserOS", Draft: true, SkipUpload: true})
	if err != nil {
		t.Fatal(err)
	}
	argv := rec.Argv()
	if argv[0] != "gh --version" {
		t.Errorf("first cmd = %q", argv[0])
	}
	create := argv[1]
	for _, want := range []string{"gh release create v0.46.17", "--repo browseros-ai/BrowserOS", "--title v0.46.17", "--draft"} {
		if !strings.Contains(create, want) {
			t.Errorf("create cmd missing %q: %q", want, create)
		}
	}
}

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"0.46.17":   "0.46.17",
		"0.46.17.3": "0.46.17", // patch stripped like github.py normalize_version
		"0.46":      "0.46",
	}
	for in, want := range cases {
		if got := NormalizeVersion(in); got != want {
			t.Errorf("NormalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoFromGitParsesRemotes(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:browseros-ai/BrowserOS.git": "browseros-ai/BrowserOS",
		"https://github.com/browseros-ai/BrowserOS": "browseros-ai/BrowserOS",
		"https://gitlab.com/other/repo.git":         "",
	} {
		rec := &execx.RecordingRunner{Results: []execx.Result{{Stdout: remote + "\n"}}}
		deps := &Deps{Runner: rec}
		if got := deps.RepoFromGit(); got != want {
			t.Errorf("RepoFromGit(%q) = %q, want %q", remote, got, want)
		}
	}
}
