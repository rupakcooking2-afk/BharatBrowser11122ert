package resources

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/config"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/fsx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/r2"
)

const (
	artifactZipDownload  = "artifact_zip"
	artifactMetadataName = "artifact-metadata.json"
)

// DownloadOperation is one entry of download_resources.yaml download_operations.
type DownloadOperation struct {
	Name         string   `yaml:"name"`
	R2Key        string   `yaml:"r2_key"`
	Destination  string   `yaml:"destination"`
	DownloadType string   `yaml:"download_type"`
	Executable   bool     `yaml:"executable"`
	OS           []string `yaml:"os"`
	Arch         []string `yaml:"arch"`
	BuildType    string   `yaml:"build_type"`
}

type downloadConfig struct {
	DownloadOperations []DownloadOperation `yaml:"download_operations"`
}

// Download ports storage/download.py DownloadResourcesModule: fetch build
// resources from R2, always clearing and re-downloading.
type Download struct {
	Client *r2.Client
}

func NewDownload() *Download { return &Download{} }

func (Download) Name() string        { return "download_resources" }
func (Download) Description() string { return "Download resources from Cloudflare R2" }

func (Download) Validate(ctx *buildctx.Context) error {
	if !envx.HasR2Config() {
		return fmt.Errorf(
			"R2 configuration not set. Required env vars: R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY")
	}
	if _, err := os.Stat(ctx.DownloadResourcesConfig()); err != nil {
		return fmt.Errorf("download configuration file not found: %s", ctx.DownloadResourcesConfig())
	}
	return nil
}

func (m Download) Execute(ctx *buildctx.Context) error {
	logx.Info("\nDownloading resources from R2...")

	var cfg downloadConfig
	if err := config.LoadInto(ctx.DownloadResourcesConfig(), &cfg); err != nil {
		return err
	}
	if len(cfg.DownloadOperations) == 0 {
		logx.Info("No download_operations defined in configuration")
		return nil
	}

	filtered := FilterDownloadOperations(cfg.DownloadOperations, ctx)
	if len(filtered) == 0 {
		logx.Info("No downloads needed for current platform/architecture")
		return nil
	}
	logx.Info(fmt.Sprintf("Downloading %d resource(s)...", len(filtered)))

	client := m.Client
	if client == nil {
		var err error
		client, err = r2.NewFromEnv()
		if err != nil {
			return err
		}
	}

	for _, op := range filtered {
		name := op.Name
		if name == "" {
			name = "Unnamed"
		}
		destPath := filepath.Join(ctx.RootDir, filepath.FromSlash(op.Destination))
		logx.Info("  " + name)

		// Always clear and re-download (ensures latest).
		if _, err := os.Stat(destPath); err == nil {
			if err := fsx.RemoveAll(destPath); err != nil {
				return err
			}
			logx.Info(fmt.Sprintf("    Cleared existing: %s", filepath.Base(destPath)))
		}

		if err := downloadOperation(client, ctx, op, destPath); err != nil {
			return err
		}
	}

	logx.Success(fmt.Sprintf("Downloaded %d resource(s) from R2", len(filtered)))
	return nil
}

func downloadOperation(client *r2.Client, ctx *buildctx.Context, op DownloadOperation, destPath string) error {
	if op.DownloadType == artifactZipDownload {
		tempDir, err := os.MkdirTemp("", "artifact-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tempDir)
		archivePath := filepath.Join(tempDir, "artifact.zip")
		if err := client.GetFile(op.R2Key, archivePath); err != nil {
			return fmt.Errorf("failed to download artifact zip: %s: %w", op.R2Key, err)
		}
		extracted, err := ExtractArtifactZip(archivePath, destPath, ctx.Platform.IsWindows())
		if err != nil {
			return err
		}
		logx.Info(fmt.Sprintf("    Extracted %d artifact file(s)", len(extracted)))
		return nil
	}

	if err := client.GetFile(op.R2Key, destPath); err != nil {
		name := op.Name
		if name == "" {
			name = op.R2Key
		}
		return fmt.Errorf("failed to download: %s: %w", name, err)
	}
	if op.Executable {
		info, err := os.Stat(destPath)
		if err != nil {
			return err
		}
		if err := os.Chmod(destPath, info.Mode()|0o755); err != nil {
			return err
		}
		logx.Info("    Set executable permissions")
	}
	return nil
}

// FilterDownloadOperations applies os/arch/build_type gates; universal macOS
// builds pull arm64 + x64 + universal entries (download.py _filter_operations).
func FilterDownloadOperations(operations []DownloadOperation, ctx *buildctx.Context) []DownloadOperation {
	targetArchs := []string{ctx.Architecture}
	if ctx.Architecture == "universal" {
		targetArchs = []string{"arm64", "x64", "universal"}
	}

	var filtered []DownloadOperation
	for _, op := range operations {
		if len(op.OS) > 0 && !slicesContains(op.OS, ctx.Platform.OS) {
			continue
		}
		if len(op.Arch) > 0 {
			match := false
			for _, arch := range targetArchs {
				if slicesContains(op.Arch, arch) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		if op.BuildType != "" && op.BuildType != ctx.BuildType {
			continue
		}
		filtered = append(filtered, op)
	}
	return filtered
}

type artifactMetadata struct {
	Files []artifactFile `json:"files"`
}

type artifactFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ExtractArtifactZip extracts a BrowserOS resource artifact zip, validating
// every declared file's size and sha256 and restoring Unix mode bits
// (download.py extract_artifact_zip).
func ExtractArtifactZip(archivePath, destination string, isWindows bool) ([]string, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()

	metadataBytes, err := readArchiveFile(&archive.Reader, artifactMetadataName)
	if err != nil {
		return nil, fmt.Errorf("artifact archive is missing %s", artifactMetadataName)
	}
	var metadata artifactMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return nil, fmt.Errorf("artifact metadata is not valid JSON: %w", err)
	}
	if len(metadata.Files) == 0 {
		return nil, fmt.Errorf("artifact metadata must contain a non-empty files list")
	}

	var extracted []string
	for _, entry := range metadata.Files {
		relPath, err := normalizeArtifactPath(entry.Path)
		if err != nil {
			return nil, err
		}
		if len(entry.SHA256) != 64 {
			return nil, fmt.Errorf("artifact metadata has invalid sha256 for %s", relPath)
		}
		if entry.Size < 0 {
			return nil, fmt.Errorf("artifact metadata has invalid size for %s", relPath)
		}

		member := findArchiveMember(&archive.Reader, relPath)
		if member == nil {
			return nil, fmt.Errorf("artifact archive is missing declared file: %s", relPath)
		}

		destPath := filepath.Join(destination, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return nil, err
		}

		size, sum, err := extractMember(member, destPath)
		if err != nil {
			return nil, err
		}
		if size != entry.Size {
			return nil, fmt.Errorf("artifact file size mismatch for %s: expected %d, got %d", relPath, entry.Size, size)
		}
		if sum != strings.ToLower(entry.SHA256) {
			return nil, fmt.Errorf("artifact checksum mismatch for %s: expected %s, got %s", relPath, strings.ToLower(entry.SHA256), sum)
		}

		if !isWindows {
			restoreZipFileMode(destPath, member)
		}
		extracted = append(extracted, destPath)
	}

	if err := os.MkdirAll(destination, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(destination, artifactMetadataName), metadataBytes, 0o644); err != nil {
		return nil, err
	}
	return extracted, nil
}

func readArchiveFile(archive *zip.Reader, name string) ([]byte, error) {
	for _, f := range archive.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("not found: %s", name)
}

func findArchiveMember(archive *zip.Reader, name string) *zip.File {
	for _, f := range archive.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func extractMember(member *zip.File, destPath string) (int64, string, error) {
	rc, err := member.Open()
	if err != nil {
		return 0, "", err
	}
	defer rc.Close()
	out, err := os.Create(destPath)
	if err != nil {
		return 0, "", err
	}
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, hasher), rc)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hasher.Sum(nil)), nil
}

func normalizeArtifactPath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("artifact metadata file entry is missing path")
	}
	clean := path.Clean(raw)
	if path.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") || strings.HasSuffix(raw, "/") {
		return "", fmt.Errorf("artifact metadata path is unsafe: %s", raw)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("artifact metadata path is unsafe: %s", raw)
		}
	}
	return clean, nil
}

// restoreZipFileMode applies Unix permission bits from the zip entry
// (download.py _restore_zip_file_mode).
func restoreZipFileMode(destPath string, member *zip.File) {
	mode := member.ExternalAttrs >> 16 & 0o777
	if mode == 0 {
		parts := strings.Split(member.Name, "/")
		if len(parts) >= 2 && parts[0] == "resources" && parts[1] == "bin" {
			logx.Warning(fmt.Sprintf(
				"No Unix mode bits in zip entry %s; leaving default permissions", member.Name))
		}
		return
	}
	if info, err := os.Stat(destPath); err == nil {
		os.Chmod(destPath, info.Mode()&^0o777|os.FileMode(mode))
	}
}
