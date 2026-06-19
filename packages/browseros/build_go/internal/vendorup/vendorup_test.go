package vendorup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/r2"
)

func TestTagNormalization(t *testing.T) {
	if got := NormalizeVersionTag("1.2.3"); got != "v1.2.3" {
		t.Errorf("lima tag = %q", got)
	}
	if got := NormalizeVersionTag("v1.2.3"); got != "v1.2.3" {
		t.Errorf("lima tag = %q", got)
	}
	if got := NormalizeBunVersionTag("1.2.15"); got != "bun-v1.2.15" {
		t.Errorf("bun tag = %q", got)
	}
	if got := NormalizeBunVersionTag("bun-1.2.15"); got != "bun-v1.2.15" {
		t.Errorf("bun tag = %q", got)
	}
	if got := NormalizeCodexReleaseTag("0.136.0"); got != "rust-v0.136.0" {
		t.Errorf("codex tag = %q", got)
	}
	if got := NormalizeCodexReleaseTag("rust-0.136.0"); got != "rust-v0.136.0" {
		t.Errorf("codex tag = %q", got)
	}
}

func TestPlatformBinaryObjectName(t *testing.T) {
	if got := PlatformBinaryObjectName("codex", "windows-x64"); got != "codex-windows-x64.exe" {
		t.Errorf("windows name = %q", got)
	}
	if got := PlatformBinaryObjectName("claude", "darwin-arm64"); got != "claude-darwin-arm64" {
		t.Errorf("darwin name = %q", got)
	}
}

func TestParseChecksums(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	entries, err := ParseChecksums(sha + "  lima-1.0-Darwin-arm64.tar.gz\n\n" + sha + " *starred.zip\n")
	if err != nil {
		t.Fatal(err)
	}
	if entries["lima-1.0-Darwin-arm64.tar.gz"] != sha || entries["starred.zip"] != sha {
		t.Errorf("entries = %v", entries)
	}
	if _, err := ParseChecksums("malformed-line-without-name\n"); err == nil {
		t.Error("malformed line should error")
	}
	if _, err := ParseChecksums("nothex  file.tar.gz\n"); err == nil {
		t.Error("invalid sha should error")
	}
}

// buildLimaTarball assembles a minimal lima-<ver>-<arch>.tar.gz with the two
// runtime files the uploader extracts.
func buildLimaTarball(t *testing.T, versionNum, upstream, guestArch string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name, content string, mode int64) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(content))
	}
	prefix := fmt.Sprintf("lima-%s-%s", versionNum, upstream)
	write(prefix+"/bin/limactl", "limactl-binary-"+upstream, 0o755)
	write(prefix+"/share/lima/lima-guestagent.Linux-"+guestArch+".gz", "guestagent-"+guestArch, 0o644)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func shaOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// fixtureServers spins upstream (GitHub) + R2 endpoints for a lima run.
func limaFixture(t *testing.T) (*Deps, *sync.Map, *sync.Map) {
	t.Helper()
	armTarball := buildLimaTarball(t, "1.2.3", "Darwin-arm64", "aarch64")
	x64Tarball := buildLimaTarball(t, "1.2.3", "Darwin-x86_64", "x86_64")
	checksums := shaOf(armTarball) + "  lima-1.2.3-Darwin-arm64.tar.gz\n" +
		shaOf(x64Tarball) + "  lima-1.2.3-Darwin-x86_64.tar.gz\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS"):
			w.Write([]byte(checksums))
		case strings.HasSuffix(r.URL.Path, "Darwin-arm64.tar.gz"):
			w.Write(armTarball)
		case strings.HasSuffix(r.URL.Path, "Darwin-x86_64.tar.gz"):
			w.Write(x64Tarball)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	prev := LimaReleaseBase
	LimaReleaseBase = upstream.URL
	t.Cleanup(func() { LimaReleaseBase = prev })

	var puts, deletes sync.Map
	r2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/browseros/")
		switch r.Method {
		case http.MethodPut:
			puts.Store(key, true)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			deletes.Store(key, true)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(r2Server.Close)

	deps := &Deps{Client: &r2.Client{
		Endpoint: r2Server.URL, Bucket: "browseros",
		AccessKey: "ak", SecretKey: "sk", Region: "auto",
		HTTP: r2Server.Client(), Now: time.Now,
	}}
	return deps, &puts, &deletes
}

func countSyncMap(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ any) bool { n++; return true })
	return n
}

func TestUploadLimaPushesBinariesAndManifest(t *testing.T) {
	deps, puts, _ := limaFixture(t)
	if err := UploadLima(deps, "1.2.3"); err != nil {
		t.Fatal(err)
	}

	wantKeys := []string{
		"artifacts/vendor/third_party/lima/limactl-darwin-arm64",
		"artifacts/vendor/third_party/lima/lima-guestagent.Linux-aarch64.gz",
		"artifacts/vendor/third_party/lima/limactl-darwin-x64",
		"artifacts/vendor/third_party/lima/lima-guestagent.Linux-x86_64.gz",
		"artifacts/vendor/third_party/lima/manifest.json",
	}
	for _, key := range wantKeys {
		if _, ok := puts.Load(key); !ok {
			t.Errorf("missing upload: %s", key)
		}
	}
	if got := countSyncMap(puts); got != len(wantKeys) {
		t.Errorf("uploaded %d objects, want %d", got, len(wantKeys))
	}
}

func TestUploadLimaDryRunSkipsUploads(t *testing.T) {
	deps, puts, _ := limaFixture(t)
	deps.DryRun = true
	deps.Client = nil

	if err := UploadLima(deps, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if got := countSyncMap(puts); got != 0 {
		t.Errorf("dry run uploaded %d objects, want 0", got)
	}
}

func TestUploadLimaChecksumMismatchAborts(t *testing.T) {
	deps, puts, _ := limaFixture(t)

	// Point at an upstream whose tarball bytes don't match the checksums.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
			w.Write([]byte(strings.Repeat("ab", 32) + "  lima-1.2.3-Darwin-arm64.tar.gz\n" +
				strings.Repeat("cd", 32) + "  lima-1.2.3-Darwin-x86_64.tar.gz\n"))
			return
		}
		w.Write([]byte("tarball-bytes-that-wont-match"))
	}))
	defer bad.Close()
	prev := LimaReleaseBase
	LimaReleaseBase = bad.URL
	defer func() { LimaReleaseBase = prev }()

	err := UploadLima(deps, "1.2.3")
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v", err)
	}
	if got := countSyncMap(puts); got != 0 {
		t.Errorf("failed run uploaded %d objects", got)
	}
}

func TestUploadLimaRollsBackOnLateFailure(t *testing.T) {
	// Upstream serves arm64 fine but 404s the x64 tarball → the arm64
	// uploads must be rolled back.
	armTarball := buildLimaTarball(t, "1.2.3", "Darwin-arm64", "aarch64")
	checksums := shaOf(armTarball) + "  lima-1.2.3-Darwin-arm64.tar.gz\n" +
		strings.Repeat("ab", 32) + "  lima-1.2.3-Darwin-x86_64.tar.gz\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS"):
			w.Write([]byte(checksums))
		case strings.HasSuffix(r.URL.Path, "Darwin-arm64.tar.gz"):
			w.Write(armTarball)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	prev := LimaReleaseBase
	LimaReleaseBase = upstream.URL
	defer func() { LimaReleaseBase = prev }()

	var puts, deletes sync.Map
	r2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/browseros/")
		switch r.Method {
		case http.MethodPut:
			puts.Store(key, true)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			deletes.Store(key, true)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer r2Server.Close()
	deps := &Deps{Client: &r2.Client{
		Endpoint: r2Server.URL, Bucket: "browseros",
		AccessKey: "ak", SecretKey: "sk", Region: "auto",
		HTTP: r2Server.Client(), Now: time.Now,
	}}

	if err := UploadLima(deps, "1.2.3"); err == nil {
		t.Fatal("expected failure for missing x64 tarball")
	}
	// Both arm64 objects were uploaded then rolled back.
	if got := countSyncMap(&puts); got != 2 {
		t.Errorf("uploads before failure = %d, want 2", got)
	}
	if got := countSyncMap(&deletes); got != 2 {
		t.Errorf("rollback deletes = %d, want 2", got)
	}
}
