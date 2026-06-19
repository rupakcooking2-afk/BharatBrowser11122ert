package extensions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/fetch"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

const manifestXML = `<?xml version="1.0" encoding="UTF-8"?>
<gupdate xmlns="http://www.google.com/update2/response" protocol="2.0">
  <app appid="aaaabbbbccccddddeeeeffffgggghhhh">
    <updatecheck codebase="%s/ext1.crx" version="1.2.3" />
  </app>
  <app appid="zzzzyyyyxxxxwwwwvvvvuuuuttttssss">
    <updatecheck codebase="%s/ext2.crx" version="4.5.6" />
  </app>
  <app appid="no-updatecheck-app"></app>
</gupdate>`

func TestParseManifestXMLHandlesNamespaceAndSkipsIncomplete(t *testing.T) {
	content := strings.ReplaceAll(manifestXML, "%s", "https://cdn.example.com")
	extensions, err := ParseManifestXML([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(extensions) != 2 {
		t.Fatalf("extensions = %+v", extensions)
	}
	if extensions[0].ID != "aaaabbbbccccddddeeeeffffgggghhhh" || extensions[0].Version != "1.2.3" {
		t.Errorf("first = %+v", extensions[0])
	}
	if !strings.HasSuffix(extensions[1].Codebase, "/ext2.crx") {
		t.Errorf("second codebase = %q", extensions[1].Codebase)
	}
}

func TestParseManifestXMLWithoutNamespace(t *testing.T) {
	plain := `<gupdate><app appid="abc"><updatecheck codebase="https://x/y.crx" version="9.9"/></app></gupdate>`
	extensions, err := ParseManifestXML([]byte(plain))
	if err != nil || len(extensions) != 1 || extensions[0].ID != "abc" {
		t.Errorf("extensions = (%+v, %v)", extensions, err)
	}
}

func TestParseManifestXMLBadXML(t *testing.T) {
	if _, err := ParseManifestXML([]byte("not xml <")); err == nil {
		t.Error("expected parse error")
	}
}

func fixtureCtx(t *testing.T) *buildctx.Context {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte("name = \"browseros\"\n"), 0o644)
	chromiumSrc := filepath.Join(t.TempDir(), "src")
	os.MkdirAll(chromiumSrc, 0o755)
	plat := platform.Platform{OS: "macos", Arch: "arm64"}
	ctx, err := buildctx.New(buildctx.Options{ChromiumSrc: chromiumSrc, Platform: &plat, RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// urlRewritingFetcher redirects the production manifest URL to a test server.
type urlRewritingFetcher struct {
	inner    fetch.Fetcher
	manifest string
}

func (f urlRewritingFetcher) Download(url, dest string) error {
	if strings.Contains(url, "update-manifest") {
		url = f.manifest
	}
	return f.inner.Download(url, dest)
}

func TestBundledDownloadsCrxAndWritesJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "manifest.xml"):
			w.Write([]byte(strings.ReplaceAll(manifestXML, "%s", "http://"+r.Host)))
		case strings.HasSuffix(r.URL.Path, ".crx"):
			w.Write([]byte("crx-data-" + filepath.Base(r.URL.Path)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx := fixtureCtx(t)
	module := Bundled{Fetcher: urlRewritingFetcher{inner: fetch.Default(), manifest: server.URL + "/manifest.xml"}}
	if err := module.Execute(ctx); err != nil {
		t.Fatal(err)
	}

	outDir := OutputDir(ctx)
	for _, id := range []string{"aaaabbbbccccddddeeeeffffgggghhhh", "zzzzyyyyxxxxwwwwvvvvuuuuttttssss"} {
		if _, err := os.Stat(filepath.Join(outDir, id+".crx")); err != nil {
			t.Errorf("missing crx for %s: %v", id, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "bundled_extensions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]map[string]string
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	entry := data["aaaabbbbccccddddeeeeffffgggghhhh"]
	if entry["external_crx"] != "aaaabbbbccccddddeeeeffffgggghhhh.crx" || entry["external_version"] != "1.2.3" {
		t.Errorf("json entry = %v", entry)
	}
}

func TestBundledFailsWhenManifestUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx := fixtureCtx(t)
	module := Bundled{Fetcher: urlRewritingFetcher{inner: fetch.Default(), manifest: server.URL + "/manifest.xml"}}
	err := module.Execute(ctx)
	if err == nil || !strings.Contains(err.Error(), "failed to fetch manifest") {
		t.Errorf("err = %v", err)
	}
}
