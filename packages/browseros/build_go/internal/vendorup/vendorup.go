// Package vendorup ports build/cli/storage.py: download third-party releases
// (Lima, Bun, Codex, Claude Code), verify checksums, and push normalized
// binaries + manifest.json to R2 for build:server ingestion. Any mid-flow
// failure rolls back uploaded objects so R2 never holds a mixed-version set.
package vendorup

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/fetch"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/r2"
)

// Release base URLs are vars so tests can point them at httptest servers.
var (
	LimaReleaseBase       = "https://github.com/lima-vm/lima/releases/download"
	BunReleaseBase        = "https://github.com/oven-sh/bun/releases/download"
	CodexReleaseBase      = "https://github.com/openai/codex/releases/download"
	ClaudeCodeReleaseBase = "https://downloads.claude.ai/claude-code-releases"
)

const (
	limaR2Prefix       = "artifacts/vendor/third_party/lima"
	bunR2Prefix        = "artifacts/vendor/third_party/bun"
	codexR2Prefix      = "artifacts/vendor/third_party/codex"
	claudeCodeR2Prefix = "artifacts/vendor/third_party/claude-code"

	// CodexDefaultTag and ClaudeCodeDefaultVersion are the CLI defaults.
	CodexDefaultTag          = "rust-v0.136.0"
	ClaudeCodeDefaultVersion = "2.1.159"
)

// Deps carries the injectables for one upload run.
type Deps struct {
	Fetcher fetch.Fetcher
	Client  *r2.Client // nil in dry-run
	DryRun  bool
}

func (d *Deps) fetcher() fetch.Fetcher {
	if d.Fetcher != nil {
		return d.Fetcher
	}
	return fetch.Default()
}

// Prepare validates R2 config and creates the client (storage.py
// _prepare_upload_client): dry-runs stay local.
func Prepare(dryRun bool) (*Deps, error) {
	deps := &Deps{DryRun: dryRun}
	if dryRun {
		return deps, nil
	}
	client, err := r2.NewFromEnv()
	if err != nil {
		return nil, err
	}
	deps.Client = client
	return deps, nil
}

// === tag normalization (storage.py) ===

func NormalizeVersionTag(version string) string {
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func NormalizeBunVersionTag(version string) string {
	if strings.HasPrefix(version, "bun-v") {
		return version
	}
	if strings.HasPrefix(version, "bun-") {
		return "bun-v" + strings.TrimPrefix(version, "bun-")
	}
	return "bun-" + NormalizeVersionTag(version)
}

func NormalizeCodexReleaseTag(version string) string {
	if strings.HasPrefix(version, "rust-v") {
		return version
	}
	if strings.HasPrefix(version, "rust-") {
		return "rust-" + NormalizeVersionTag(strings.TrimPrefix(version, "rust-"))
	}
	return "rust-" + NormalizeVersionTag(version)
}

func codexCLIVersion(tag string) string {
	return strings.TrimPrefix(tag, "rust-v")
}

// PlatformBinaryObjectName maps ("codex", "windows-x64") → codex-windows-x64.exe.
func PlatformBinaryObjectName(stem, target string) string {
	suffix := ""
	if strings.HasPrefix(target, "windows-") {
		suffix = ".exe"
	}
	return stem + "-" + target + suffix
}

// === shared helpers ===

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range strings.ToLower(value) {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// ParseChecksums parses "<sha256>  <name>" lines (storage.py _parse_checksums).
func ParseChecksums(contents string) (map[string]string, error) {
	entries := map[string]string{}
	for _, rawLine := range strings.Split(contents, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("malformed SHA256SUMS line: %q", rawLine)
		}
		sha := strings.ToLower(fields[0])
		name := strings.TrimPrefix(strings.Join(fields[1:], " "), "*")
		if !isSHA256(sha) {
			return nil, fmt.Errorf("invalid sha256 in SHA256SUMS: %q", rawLine)
		}
		entries[name] = sha
	}
	return entries, nil
}

func (d *Deps) fetchChecksums(url, dest string) (map[string]string, error) {
	logx.Info("Fetching " + url)
	if err := d.fetcher().Download(url, dest); err != nil {
		return nil, err
	}
	content, err := os.ReadFile(dest)
	if err != nil {
		return nil, err
	}
	return ParseChecksums(string(content))
}

func (d *Deps) downloadVerified(url, dest, expectedSHA, name string) (string, error) {
	logx.Info("Downloading " + url)
	if err := d.fetcher().Download(url, dest); err != nil {
		return "", err
	}
	actual, err := sha256File(dest)
	if err != nil {
		return "", err
	}
	if expectedSHA != "" && actual != expectedSHA {
		return "", fmt.Errorf("sha256 mismatch for %s: expected %s, got %s", name, expectedSHA, actual)
	}
	return actual, nil
}

func (d *Deps) upload(localPath, r2Key string, uploadedKeys *[]string) error {
	if d.DryRun {
		logx.Info("[dry-run] skipped upload of " + r2Key)
		return nil
	}
	if err := d.Client.PutFile(localPath, r2Key, ""); err != nil {
		return fmt.Errorf("failed to upload %s: %w", r2Key, err)
	}
	*uploadedKeys = append(*uploadedKeys, r2Key)
	return nil
}

func (d *Deps) rollback(keys []string) {
	if d.DryRun || len(keys) == 0 {
		return
	}
	logx.Warning(fmt.Sprintf("Upload failed — rolling back %d object(s)", len(keys)))
	for _, key := range keys {
		if err := d.Client.Delete(key); err != nil {
			logx.Warning(fmt.Sprintf("Rollback failed for %s: %v", key, err))
		} else {
			logx.Info("Rolled back " + key)
		}
	}
}

func (d *Deps) uploadManifest(manifest map[string]any, tmpDir, manifestKey string, uploadedKeys *[]string) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(tmpDir, "manifest.json")
	if err := os.WriteFile(manifestPath, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	if d.DryRun {
		logx.Info(fmt.Sprintf("[dry-run] manifest would be: %s", encoded))
		return nil
	}
	if err := d.Client.PutFile(manifestPath, manifestKey, "application/json"); err != nil {
		return fmt.Errorf("failed to upload %s: %w", manifestKey, err)
	}
	*uploadedKeys = append(*uploadedKeys, manifestKey)
	return nil
}

func uploadedBy() string {
	if actor := os.Getenv("GITHUB_ACTOR"); actor != "" {
		return actor
	}
	return "local"
}

// === archive extraction (storage.py _extract_* / _logical_*) ===

func logicalPathStrippingPrefix(memberName, prefix string) string {
	parts := strings.Split(strings.TrimPrefix(path.Clean(strings.TrimPrefix(memberName, "./")), "/"), "/")
	if len(parts) > 1 && strings.HasPrefix(parts[0], prefix) {
		parts = parts[1:]
	}
	return strings.Join(parts, "/")
}

// extractTarGzFile extracts the member whose logical path matches; mode bits
// are applied from the archive (or 0755 when exec is true).
func extractTarGzFile(archivePath, logicalPath, dest string, logical func(string) string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || logical(header.Name) != logicalPath {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, reader); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chmod(dest, os.FileMode(header.Mode)&0o777)
	}
	return fmt.Errorf("%s not found in archive", logicalPath)
}

func extractZipFile(archivePath, logicalPath, dest string, logical func(string) string, makeExec bool) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	for _, member := range archive.File {
		if member.FileInfo().IsDir() || logical(member.Name) != logicalPath {
			continue
		}
		src, err := member.Open()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			src.Close()
			return err
		}
		out, err := os.Create(dest)
		if err != nil {
			src.Close()
			return err
		}
		_, copyErr := io.Copy(out, src)
		src.Close()
		if closeErr := out.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			return copyErr
		}
		if makeExec {
			return os.Chmod(dest, 0o755)
		}
		return nil
	}
	return fmt.Errorf("%s not found in archive", logicalPath)
}

// === Lima (storage.py upload_lima) ===

type limaArch struct {
	Internal       string
	Upstream       string
	LinuxGuestArch string
}

var limaArches = []limaArch{
	{"arm64", "Darwin-arm64", "aarch64"},
	{"x64", "Darwin-x86_64", "x86_64"},
}

// UploadLima downloads limactl + guest agents from a Lima release and pushes
// them to R2 with a manifest.
func UploadLima(deps *Deps, version string) error {
	tag := NormalizeVersionTag(version)
	tmpDir, err := os.MkdirTemp("", "lima-upload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	checksums, err := deps.fetchChecksums(
		fmt.Sprintf("%s/%s/SHA256SUMS", LimaReleaseBase, tag),
		filepath.Join(tmpDir, "SHA256SUMS"))
	if err != nil {
		return fmt.Errorf("lima upload aborted: %w", err)
	}

	var uploadedKeys []string
	tarballSHAs := map[string]string{}
	objectSHAs := map[string]map[string]string{}

	run := func() error {
		for _, arch := range limaArches {
			versionNum := strings.TrimPrefix(tag, "v")
			tarballName := fmt.Sprintf("lima-%s-%s.tar.gz", versionNum, arch.Upstream)
			expected, ok := checksums[tarballName]
			if !ok {
				return fmt.Errorf("%s missing from SHA256SUMS (is the version tag correct?)", tarballName)
			}
			tarballPath := filepath.Join(tmpDir, tarballName)
			actualSHA, err := deps.downloadVerified(
				fmt.Sprintf("%s/%s/%s", LimaReleaseBase, tag, tarballName), tarballPath, expected, tarballName)
			if err != nil {
				return err
			}
			tarballSHAs[arch.Internal] = actualSHA

			guestAgentName := fmt.Sprintf("lima-guestagent.Linux-%s.gz", arch.LinuxGuestArch)
			limaLogical := func(name string) string { return logicalPathStrippingPrefix(name, "lima-") }
			files := []struct{ name, logicalPath, localPath, r2Key string }{
				{"limactl", "bin/limactl",
					filepath.Join(tmpDir, "limactl-darwin-"+arch.Internal),
					limaR2Prefix + "/limactl-darwin-" + arch.Internal},
				{"guest_agent", "share/lima/" + guestAgentName,
					filepath.Join(tmpDir, guestAgentName),
					limaR2Prefix + "/" + guestAgentName},
			}
			shas := map[string]string{}
			for _, file := range files {
				if err := extractTarGzFile(tarballPath, file.logicalPath, file.localPath, limaLogical); err != nil {
					return fmt.Errorf("%s not found in Lima tarball: %w", file.logicalPath, err)
				}
				sha, err := sha256File(file.localPath)
				if err != nil {
					return err
				}
				shas[file.name] = sha
			}
			objectSHAs[arch.Internal] = shas
			for _, file := range files {
				if err := deps.upload(file.localPath, file.r2Key, &uploadedKeys); err != nil {
					return err
				}
			}
		}
		manifest := map[string]any{
			"lima_version":          tag,
			"tarball_shas_upstream": tarballSHAs,
			"r2_object_shas":        objectSHAs,
			"uploaded_at":           time.Now().UTC().Format(time.RFC3339),
			"uploaded_by":           uploadedBy(),
		}
		return deps.uploadManifest(manifest, tmpDir, limaR2Prefix+"/manifest.json", &uploadedKeys)
	}

	if err := run(); err != nil {
		deps.rollback(uploadedKeys)
		return fmt.Errorf("lima upload aborted: %w", err)
	}
	logx.Success(fmt.Sprintf("Lima %s uploaded for [arm64 x64]", tag))
	return nil
}

// === Bun (storage.py upload_bun) ===

type bunTarget struct {
	Internal   string
	Upstream   string
	R2Name     string
	BinaryName string
}

var bunTargets = []bunTarget{
	{"darwin-arm64", "darwin-aarch64", "bun-darwin-arm64", "bun"},
	{"darwin-x64", "darwin-x64", "bun-darwin-x64", "bun"},
	{"linux-arm64", "linux-aarch64", "bun-linux-arm64", "bun"},
	{"linux-x64", "linux-x64-baseline", "bun-linux-x64-baseline", "bun"},
	{"windows-x64", "windows-x64-baseline", "bun-windows-x64-baseline.exe", "bun.exe"},
}

// UploadBun pushes Bun release binaries to R2.
func UploadBun(deps *Deps, version string) error {
	tag := NormalizeBunVersionTag(version)
	tmpDir, err := os.MkdirTemp("", "bun-upload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	checksums, err := deps.fetchChecksums(
		fmt.Sprintf("%s/%s/SHASUMS256.txt", BunReleaseBase, tag),
		filepath.Join(tmpDir, "SHASUMS256.txt"))
	if err != nil {
		return fmt.Errorf("bun upload aborted: %w", err)
	}

	var uploadedKeys []string
	zipSHAs := map[string]string{}
	objectSHAs := map[string]string{}

	run := func() error {
		for _, target := range bunTargets {
			zipName := fmt.Sprintf("bun-%s.zip", target.Upstream)
			expected, ok := checksums[zipName]
			if !ok {
				return fmt.Errorf("%s missing from SHASUMS256.txt (is the version tag correct?)", zipName)
			}
			zipPath := filepath.Join(tmpDir, zipName)
			actualSHA, err := deps.downloadVerified(
				fmt.Sprintf("%s/%s/%s", BunReleaseBase, tag, zipName), zipPath, expected, zipName)
			if err != nil {
				return err
			}
			zipSHAs[target.Internal] = actualSHA

			localPath := filepath.Join(tmpDir, target.R2Name)
			bunLogical := func(name string) string { return logicalPathStrippingPrefix(name, "bun-") }
			if err := extractZipFile(zipPath, target.BinaryName, localPath, bunLogical,
				!strings.HasSuffix(target.BinaryName, ".exe")); err != nil {
				return fmt.Errorf("%s not found in Bun zip: %w", target.BinaryName, err)
			}
			binarySHA, err := sha256File(localPath)
			if err != nil {
				return err
			}
			objectSHAs[target.Internal] = binarySHA
			if err := deps.upload(localPath, bunR2Prefix+"/"+target.R2Name, &uploadedKeys); err != nil {
				return err
			}
		}
		manifest := map[string]any{
			"bun_version":       tag,
			"zip_shas_upstream": zipSHAs,
			"r2_object_shas":    objectSHAs,
			"uploaded_at":       time.Now().UTC().Format(time.RFC3339),
			"uploaded_by":       uploadedBy(),
		}
		return deps.uploadManifest(manifest, tmpDir, bunR2Prefix+"/manifest.json", &uploadedKeys)
	}

	if err := run(); err != nil {
		deps.rollback(uploadedKeys)
		return fmt.Errorf("bun upload aborted: %w", err)
	}
	logx.Success(fmt.Sprintf("Bun %s uploaded for [darwin-arm64 darwin-x64 linux-arm64 linux-x64 windows-x64]", tag))
	return nil
}

// === Codex (storage.py upload_codex) ===

type codexPlatform struct {
	Target   string
	Upstream string
}

var codexPlatforms = []codexPlatform{
	{"darwin-arm64", "aarch64-apple-darwin"},
	{"darwin-x64", "x86_64-apple-darwin"},
	{"linux-arm64", "aarch64-unknown-linux-musl"},
	{"linux-x64", "x86_64-unknown-linux-musl"},
	{"windows-x64", "x86_64-pc-windows-msvc"},
}

// codexEntrypoint reads codex-package.json's entrypoint from the tarball.
func codexEntrypoint(packagePath string) (string, error) {
	f, err := os.Open(packagePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if logicalPathStrippingPrefix(header.Name, "\x00") != "codex-package.json" {
			continue
		}
		var metadata struct {
			Entrypoint string `json:"entrypoint"`
		}
		if err := json.NewDecoder(reader).Decode(&metadata); err != nil {
			return "", err
		}
		if metadata.Entrypoint == "" {
			return "", fmt.Errorf("codex package metadata is missing entrypoint")
		}
		return metadata.Entrypoint, nil
	}
	return "", fmt.Errorf("codex-package.json not found in Codex package")
}

// UploadCodex pushes Codex release binaries to R2.
func UploadCodex(deps *Deps, version string) error {
	tag := NormalizeCodexReleaseTag(version)
	tmpDir, err := os.MkdirTemp("", "codex-upload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	checksums, err := deps.fetchChecksums(
		fmt.Sprintf("%s/%s/codex-package_SHA256SUMS", CodexReleaseBase, tag),
		filepath.Join(tmpDir, "codex-package_SHA256SUMS"))
	if err != nil {
		return fmt.Errorf("codex upload aborted: %w", err)
	}

	var uploadedKeys []string
	packageSHAs := map[string]string{}
	objectSHAs := map[string]string{}

	run := func() error {
		for _, plat := range codexPlatforms {
			packageName := fmt.Sprintf("codex-package-%s.tar.gz", plat.Upstream)
			expected, ok := checksums[packageName]
			if !ok {
				return fmt.Errorf("%s missing from codex-package_SHA256SUMS (is the version tag correct?)", packageName)
			}
			packagePath := filepath.Join(tmpDir, packageName)
			actualSHA, err := deps.downloadVerified(
				fmt.Sprintf("%s/%s/%s", CodexReleaseBase, tag, packageName), packagePath, expected, packageName)
			if err != nil {
				return err
			}
			packageSHAs[plat.Target] = actualSHA

			entrypoint, err := codexEntrypoint(packagePath)
			if err != nil {
				return err
			}
			objectName := PlatformBinaryObjectName("codex", plat.Target)
			localPath := filepath.Join(tmpDir, objectName)
			identity := func(name string) string { return logicalPathStrippingPrefix(name, "\x00") }
			if err := extractTarGzFile(packagePath, entrypoint, localPath, identity); err != nil {
				return fmt.Errorf("codex entrypoint %s not found in package: %w", entrypoint, err)
			}
			if !strings.HasSuffix(entrypoint, ".exe") {
				os.Chmod(localPath, 0o755)
			}
			binarySHA, err := sha256File(localPath)
			if err != nil {
				return err
			}
			objectSHAs[plat.Target] = binarySHA
			if err := deps.upload(localPath, codexR2Prefix+"/"+objectName, &uploadedKeys); err != nil {
				return err
			}
		}
		manifest := map[string]any{
			"codex_release_tag":     tag,
			"codex_cli_version":     codexCLIVersion(tag),
			"package_shas_upstream": packageSHAs,
			"r2_object_shas":        objectSHAs,
			"uploaded_at":           time.Now().UTC().Format(time.RFC3339),
			"uploaded_by":           uploadedBy(),
		}
		return deps.uploadManifest(manifest, tmpDir, codexR2Prefix+"/manifest.json", &uploadedKeys)
	}

	if err := run(); err != nil {
		deps.rollback(uploadedKeys)
		return fmt.Errorf("codex upload aborted: %w", err)
	}
	logx.Success(fmt.Sprintf("Codex %s uploaded for [darwin-arm64 darwin-x64 linux-arm64 linux-x64 windows-x64]", tag))
	return nil
}

// === Claude Code (storage.py upload_claude_code) ===

type claudeCodePlatform struct {
	Target   string
	Upstream string
}

var claudeCodePlatforms = []claudeCodePlatform{
	{"darwin-arm64", "darwin-arm64"},
	{"darwin-x64", "darwin-x64"},
	{"linux-arm64", "linux-arm64"},
	{"linux-x64", "linux-x64"},
	{"windows-x64", "win32-x64"},
}

type claudeManifestEntry struct {
	Binary   string `json:"binary"`
	Checksum string `json:"checksum"`
	Size     int64  `json:"size"`
}

// UploadClaudeCode pushes Claude Code release binaries to R2.
func UploadClaudeCode(deps *Deps, version string) error {
	version = strings.TrimSpace(version)
	tmpDir, err := os.MkdirTemp("", "claude-code-upload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	manifestURL := fmt.Sprintf("%s/%s/manifest.json", ClaudeCodeReleaseBase, version)
	manifestPath := filepath.Join(tmpDir, "claude-code-manifest.json")
	logx.Info("Fetching " + manifestURL)
	if err := deps.fetcher().Download(manifestURL, manifestPath); err != nil {
		return fmt.Errorf("claude code upload aborted: %w", err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var upstream struct {
		Platforms map[string]claudeManifestEntry `json:"platforms"`
	}
	if err := json.Unmarshal(raw, &upstream); err != nil || upstream.Platforms == nil {
		return fmt.Errorf("claude code upload aborted: claude code manifest is missing platforms")
	}

	var uploadedKeys []string
	objectSHAs := map[string]string{}
	platformInfo := map[string]map[string]string{}

	run := func() error {
		for _, plat := range claudeCodePlatforms {
			entry, ok := upstream.Platforms[plat.Upstream]
			if !ok {
				return fmt.Errorf("%s missing from Claude Code manifest", plat.Upstream)
			}
			if entry.Binary == "" || !isSHA256(entry.Checksum) || entry.Size < 0 {
				return fmt.Errorf("claude code manifest has invalid entry for %s", plat.Upstream)
			}

			localPath := filepath.Join(tmpDir, plat.Upstream+"-"+entry.Binary)
			url := fmt.Sprintf("%s/%s/%s/%s", ClaudeCodeReleaseBase, version, plat.Upstream, entry.Binary)
			logx.Info("Downloading " + url)
			if err := deps.fetcher().Download(url, localPath); err != nil {
				return err
			}
			info, err := os.Stat(localPath)
			if err != nil {
				return err
			}
			if info.Size() != entry.Size {
				return fmt.Errorf("size mismatch for Claude Code %s: expected %d, got %d",
					plat.Upstream, entry.Size, info.Size())
			}
			actualSHA, err := sha256File(localPath)
			if err != nil {
				return err
			}
			if actualSHA != strings.ToLower(entry.Checksum) {
				return fmt.Errorf("sha256 mismatch for Claude Code %s: expected %s, got %s",
					plat.Upstream, entry.Checksum, actualSHA)
			}
			if !strings.HasSuffix(entry.Binary, ".exe") {
				os.Chmod(localPath, 0o755)
			}

			r2Key := claudeCodeR2Prefix + "/" + PlatformBinaryObjectName("claude", plat.Target)
			if err := deps.upload(localPath, r2Key, &uploadedKeys); err != nil {
				return err
			}
			objectSHAs[plat.Target] = actualSHA
			platformInfo[plat.Target] = map[string]string{"platform": plat.Upstream, "binary": entry.Binary}
		}
		manifest := map[string]any{
			"claude_code_version":  version,
			"binary_shas_upstream": objectSHAs,
			"r2_object_shas":       objectSHAs,
			"platforms":            platformInfo,
			"uploaded_at":          time.Now().UTC().Format(time.RFC3339),
			"uploaded_by":          uploadedBy(),
		}
		return deps.uploadManifest(manifest, tmpDir, claudeCodeR2Prefix+"/manifest.json", &uploadedKeys)
	}

	if err := run(); err != nil {
		deps.rollback(uploadedKeys)
		return fmt.Errorf("claude code upload aborted: %w", err)
	}
	logx.Success(fmt.Sprintf("Claude Code %s uploaded for [darwin-arm64 darwin-x64 linux-arm64 linux-x64 windows-x64]", version))
	return nil
}
