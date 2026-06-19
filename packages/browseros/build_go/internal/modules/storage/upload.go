// Package storage ports build/modules/storage/upload.py: the `upload`
// pipeline module that pushes dist artifacts to R2 and writes release.json.
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/notify"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/r2"
)

// PlatformName maps the build platform to the R2 path segment
// (upload.py _get_platform): macos | win | linux.
func PlatformName(ctx *buildctx.Context) string {
	switch {
	case ctx.Platform.IsMacOS():
		return "macos"
	case ctx.Platform.IsWindows():
		return "win"
	}
	return "linux"
}

// Upload is the `upload` pipeline module.
type Upload struct {
	Client *r2.Client
}

func NewUpload() *Upload { return &Upload{} }

func (Upload) Name() string        { return "upload" }
func (Upload) Description() string { return "Upload build artifacts to Cloudflare R2" }

func (Upload) Validate(ctx *buildctx.Context) error {
	if !envx.HasR2Config() {
		return fmt.Errorf(
			"R2 configuration not set. Required env vars: R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY")
	}
	return nil
}

func (m Upload) Execute(ctx *buildctx.Context) error {
	logx.Info("\nUploading package artifacts to R2...")
	extraMetadata := map[string]map[string]any{}
	for filename, sig := range ctx.SparkleSignatures {
		extraMetadata[filename] = map[string]any{
			"sparkle_signature": sig.Signature,
			"sparkle_length":    sig.Length,
		}
	}
	_, err := m.UploadReleaseArtifacts(ctx, extraMetadata)
	if err != nil {
		return fmt.Errorf("failed to upload artifacts to R2: %w", err)
	}
	return nil
}

// DetectArtifacts globs the dist dir by platform (upload.py detect_artifacts).
func DetectArtifacts(ctx *buildctx.Context) []string {
	distDir := ctx.DistDir()
	var patterns []string
	switch {
	case ctx.Platform.IsMacOS():
		patterns = []string{"*.dmg"}
	case ctx.Platform.IsWindows():
		patterns = []string{"*.exe", "*.zip"}
	default:
		patterns = []string{"*.AppImage", "*.deb"}
	}
	var artifacts []string
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(distDir, pattern))
		artifacts = append(artifacts, matches...)
	}
	sort.Strings(artifacts)
	return artifacts
}

func linuxArtifactKey(filename string) string {
	lower := strings.ToLower(filename)
	switch {
	case strings.Contains(lower, ".appimage"):
		if strings.Contains(lower, "arm64") || strings.Contains(lower, "aarch64") {
			return "arm64_appimage"
		}
		if strings.Contains(lower, "x64") || strings.Contains(lower, "x86_64") {
			return "x64_appimage"
		}
	case strings.Contains(lower, ".deb"):
		if strings.Contains(lower, "arm64") || strings.Contains(lower, "aarch64") {
			return "arm64_deb"
		}
		if strings.Contains(lower, "amd64") || strings.Contains(lower, "x64") || strings.Contains(lower, "x86_64") {
			return "x64_deb"
		}
	}
	return ""
}

// ArtifactKey names the release.json artifact entry
// (upload.py _get_artifact_key).
func ArtifactKey(filename, platform string) string {
	lower := strings.ToLower(filename)
	switch platform {
	case "macos":
		switch {
		case strings.Contains(lower, "arm64"):
			return "arm64"
		case strings.Contains(lower, "x64"), strings.Contains(lower, "x86_64"):
			return "x64"
		case strings.Contains(lower, "universal"):
			return "universal"
		}
	case "win":
		switch {
		case strings.Contains(lower, "installer.exe"):
			return "x64_installer"
		case strings.Contains(lower, "installer.zip"):
			return "x64_zip"
		}
	case "linux":
		if key := linuxArtifactKey(filename); key != "" {
			return key
		}
		logx.Warning(fmt.Sprintf("Unrecognized Linux artifact name: %s; using stem key", filename))
	}
	return strings.TrimSuffix(filename, filepath.Ext(filename))
}

// GenerateReleaseJSON builds the release metadata
// (upload.py generate_release_json). Artifact entries keep insertion order
// via a plain map (JSON objects are unordered anyway).
func GenerateReleaseJSON(ctx *buildctx.Context, artifacts []map[string]any, platform string) map[string]any {
	release := map[string]any{
		"platform":                   platform,
		"version":                    ctx.SemanticVersion,
		"chromium_version":           ctx.ChromiumVersion,
		"browseros_chromium_version": ctx.BrowserOSChromiumVersion,
		// Python writes datetime.isoformat(): microseconds + "+00:00".
		"build_date": time.Now().UTC().Format("2006-01-02T15:04:05.000000-07:00"),
		"artifacts":  map[string]any{},
	}
	if platform == "macos" {
		if sparkleVersion, err := ctx.SparkleBuildVersion(); err == nil {
			release["sparkle_version"] = sparkleVersion
		}
	}
	baseURL := envx.R2CDNBaseURL() + "/" + ctx.ReleasePath(platform)
	artifactsMap := release["artifacts"].(map[string]any)
	for _, artifact := range artifacts {
		filename := artifact["filename"].(string)
		entry := map[string]any{
			"filename": filename,
			"url":      baseURL + filename,
		}
		for key, value := range artifact {
			if key != "filename" {
				entry[key] = value
			}
		}
		artifactsMap[ArtifactKey(filename, platform)] = entry
	}
	return release
}

// MergeReleaseMetadata overlays new metadata on existing, merging artifacts
// (upload.py merge_release_metadata — Linux x64/arm64 jobs share one file).
func MergeReleaseMetadata(existing, new map[string]any) map[string]any {
	if existing == nil {
		return new
	}
	merged := map[string]any{}
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range new {
		if k != "artifacts" {
			merged[k] = v
		}
	}
	artifacts := map[string]any{}
	if existingArtifacts, ok := existing["artifacts"].(map[string]any); ok {
		for k, v := range existingArtifacts {
			artifacts[k] = v
		}
	}
	if newArtifacts, ok := new["artifacts"].(map[string]any); ok {
		for k, v := range newArtifacts {
			artifacts[k] = v
		}
	}
	merged["artifacts"] = artifacts
	return merged
}

// GetReleaseJSON fetches releases/<version>/<platform>/release.json
// (r2.py get_release_json); nil when absent.
func GetReleaseJSON(client *r2.Client, version, platform string) map[string]any {
	key := fmt.Sprintf("releases/%s/%s/release.json", version, platform)
	data, err := client.GetObject(key)
	if err != nil {
		logx.Warning("release.json not found: " + key)
		return nil
	}
	var release map[string]any
	if err := json.Unmarshal(data, &release); err != nil {
		logx.Error("Failed to parse release.json: " + err.Error())
		return nil
	}
	return release
}

// UploadReleaseArtifacts uploads dist artifacts + release.json
// (upload.py upload_release_artifacts).
func (m Upload) UploadReleaseArtifacts(ctx *buildctx.Context, extraMetadata map[string]map[string]any) (map[string]any, error) {
	if !envx.HasR2Config() {
		logx.Warning("R2 configuration not set. Skipping upload.")
		return nil, nil
	}
	artifacts := DetectArtifacts(ctx)
	if len(artifacts) == 0 {
		logx.Info("No artifacts found to upload")
		return nil, nil
	}

	platform := PlatformName(ctx)
	releasePath := ctx.ReleasePath(platform)
	logx.Info(fmt.Sprintf("\nUploading to R2: %s/%s", envx.R2Bucket(), releasePath))
	logx.Info(fmt.Sprintf("Found %d artifact(s):", len(artifacts)))
	for _, artifact := range artifacts {
		logx.Info("  - " + filepath.Base(artifact))
	}

	client := m.Client
	if client == nil {
		var err error
		client, err = r2.NewFromEnv()
		if err != nil {
			return nil, err
		}
	}

	var artifactMetadata []map[string]any
	for _, artifactPath := range artifacts {
		name := filepath.Base(artifactPath)
		if err := client.PutFile(artifactPath, releasePath+name, ""); err != nil {
			return nil, err
		}
		info, err := os.Stat(artifactPath)
		if err != nil {
			return nil, err
		}
		metadata := map[string]any{"filename": name, "size": info.Size()}
		for key, value := range extraMetadata[name] {
			metadata[key] = value
		}
		artifactMetadata = append(artifactMetadata, metadata)
	}

	release := GenerateReleaseJSON(ctx, artifactMetadata, platform)
	if platform == "linux" {
		// Linux x64 and arm64 release jobs share release.json; merge with
		// whatever is already uploaded.
		release = MergeReleaseMetadata(GetReleaseJSON(client, ctx.SemanticVersion, platform), release)
	}
	releaseJSONPath := filepath.Join(ctx.DistDir(), "release.json")
	encoded, err := json.MarshalIndent(release, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(releaseJSONPath, encoded, 0o644); err != nil {
		return nil, err
	}
	if err := client.PutFile(releaseJSONPath, releasePath+"release.json", "application/json"); err != nil {
		return nil, err
	}

	logx.Success(fmt.Sprintf("\nSuccessfully uploaded %d artifact(s) to R2", len(artifacts)))
	notify.PackageCreated("Upload Complete",
		fmt.Sprintf("Uploaded %d artifact(s) to R2", len(artifacts)),
		map[string]string{"Version": ctx.SemanticVersion, "Platform": platform},
		[]string{"Version", "Platform"})
	return release, nil
}
