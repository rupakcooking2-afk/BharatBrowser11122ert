// Package release ports build/modules/release: list/appcast/publish/download
// operations over R2 release metadata plus GitHub release creation via gh.
package release

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/fetch"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/r2"
)

// Platforms in display order (release/common.py PLATFORMS).
var Platforms = []string{"macos", "win", "linux"}

var platformDisplayNames = map[string]string{"macos": "macOS", "win": "Windows", "linux": "Linux"}

// DownloadPathMapping maps artifact keys to the stable download/ paths
// (release/common.py DOWNLOAD_PATH_MAPPING).
var DownloadPathMapping = map[string]map[string]string{
	"macos": {
		"arm64":     "download/BrowserOS-arm64.dmg",
		"x64":       "download/BrowserOS-x86_64.dmg",
		"universal": "download/BrowserOS.dmg",
	},
	"win": {
		"x64_installer": "download/BrowserOS_installer.exe",
	},
	"linux": {
		"x64_appimage":   "download/BrowserOS.AppImage",
		"x64_deb":        "download/BrowserOS.deb",
		"arm64_appimage": "download/BrowserOS-arm64.AppImage",
		"arm64_deb":      "download/BrowserOS-arm64.deb",
	},
}

// Deps carries injectables for release operations.
type Deps struct {
	Client  *r2.Client
	Runner  execx.Runner
	Fetcher fetch.Fetcher
}

func (d *Deps) client() (*r2.Client, error) {
	if d.Client != nil {
		return d.Client, nil
	}
	return r2.NewFromEnv()
}

func (d *Deps) runner() execx.Runner {
	if d.Runner != nil {
		return d.Runner
	}
	return execx.Default()
}

func (d *Deps) fetcher() fetch.Fetcher {
	if d.Fetcher != nil {
		return d.Fetcher
	}
	return fetch.Default()
}

// FetchAllReleaseMetadata pulls release.json for every platform
// (release/common.py fetch_all_release_metadata).
func FetchAllReleaseMetadata(client *r2.Client, version string) map[string]map[string]any {
	metadata := map[string]map[string]any{}
	for _, platform := range Platforms {
		key := fmt.Sprintf("releases/%s/%s/release.json", version, platform)
		data, err := client.GetObject(key)
		if err != nil {
			continue
		}
		var release map[string]any
		if err := json.Unmarshal(data, &release); err != nil {
			continue
		}
		metadata[platform] = release
	}
	return metadata
}

// ListAllVersions enumerates releases/<version>/ prefixes, newest first
// (release/common.py list_all_versions).
func ListAllVersions(client *r2.Client) ([]string, error) {
	prefixes, err := client.ListPrefixes("releases/")
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, prefix := range prefixes {
		version := strings.TrimSuffix(strings.TrimPrefix(prefix, "releases/"), "/")
		if version != "" {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		return versionLess(versions[j], versions[i]) // descending
	})
	return versions, nil
}

func versionLess(a, b string) bool {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		ai, bi := 0, 0
		if i < len(aParts) {
			ai, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bi, _ = strconv.Atoi(bParts[i])
		}
		if ai != bi {
			return ai < bi
		}
	}
	return false
}

// FormatSize renders a human size (release/common.py format_size).
func FormatSize(sizeBytes int64) string {
	switch {
	case sizeBytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(sizeBytes)/(1<<30))
	case sizeBytes >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(sizeBytes)/(1<<20))
	case sizeBytes >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(sizeBytes)/(1<<10))
	}
	return fmt.Sprintf("%d B", sizeBytes)
}

// GenerateAppcastItem renders the Sparkle <item> XML
// (release/common.py generate_appcast_item).
func GenerateAppcastItem(artifact map[string]any, version, sparkleVersion, buildDate string) string {
	pubDate := buildDate
	if parsed, err := time.Parse(time.RFC3339, strings.Replace(buildDate, "Z", "+00:00", 1)); err == nil {
		pubDate = parsed.Format("Mon, 02 Jan 2006 15:04:05 -0700")
	} else if parsed, err := time.Parse(time.RFC3339, buildDate); err == nil {
		pubDate = parsed.Format("Mon, 02 Jan 2006 15:04:05 -0700")
	}

	signature, _ := artifact["sparkle_signature"].(string)
	length := artifact["sparkle_length"]
	if length == nil {
		length = artifact["size"]
	}
	if length == nil {
		length = 0
	}
	lengthStr := fmt.Sprintf("%v", length)
	if f, ok := length.(float64); ok {
		lengthStr = strconv.FormatInt(int64(f), 10)
	}

	return fmt.Sprintf(`<item>
  <title>BrowserOS - %s</title>
  <description sparkle:format="plain-text">
  </description>
  <sparkle:version>%s</sparkle:version>
  <sparkle:shortVersionString>%s</sparkle:shortVersionString>
  <pubDate>%s</pubDate>
  <link>https://browseros.com</link>
  <enclosure
    url="%v"
    sparkle:edSignature="%s"
    length="%s"
    type="application/octet-stream" />
  <sparkle:minimumSystemVersion>10.15</sparkle:minimumSystemVersion>
</item>`, version, sparkleVersion, version, pubDate, artifact["url"], signature, lengthStr)
}

// List prints all versions or one version's artifacts
// (release/list.py ListModule).
func (d *Deps) List(version string) error {
	client, err := d.client()
	if err != nil {
		return err
	}
	if version == "" {
		versions, err := ListAllVersions(client)
		if err != nil || len(versions) == 0 {
			logx.Info("No releases found in R2")
			return err
		}
		logx.Info(fmt.Sprintf("\nAvailable releases (%d total):", len(versions)))
		logx.Info(strings.Repeat("=", 40))
		for _, v := range versions {
			logx.Info("  " + v)
		}
		logx.Info(strings.Repeat("=", 40))
		logx.Info("\nUse --version <version> for details")
		return nil
	}

	metadata := FetchAllReleaseMetadata(client, version)
	if len(metadata) == 0 {
		logx.Info("No release metadata found for version " + version)
		return nil
	}
	logx.Info(fmt.Sprintf("\n%s\nRelease: v%s\n%s", strings.Repeat("=", 60), version, strings.Repeat("=", 60)))
	for _, platform := range Platforms {
		release, ok := metadata[platform]
		if !ok {
			continue
		}
		logx.Info("\n" + platformDisplayNames[platform] + ":")
		logx.Info(fmt.Sprintf("  Build Date: %v", release["build_date"]))
		logx.Info(fmt.Sprintf("  Chromium: %v", release["chromium_version"]))
		if artifacts, ok := release["artifacts"].(map[string]any); ok {
			keys := sortedKeys(artifacts)
			for _, key := range keys {
				artifact := artifacts[key].(map[string]any)
				size := int64(0)
				if f, ok := artifact["size"].(float64); ok {
					size = int64(f)
				}
				signed := ""
				if _, ok := artifact["sparkle_signature"]; ok {
					signed = " [signed]"
				}
				logx.Info(fmt.Sprintf("  - %s: %v (%s)%s", key, artifact["filename"], FormatSize(size), signed))
			}
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Appcast prints appcast snippets for the macOS release
// (release/appcast.py AppcastModule).
func (d *Deps) Appcast(version string) error {
	client, err := d.client()
	if err != nil {
		return err
	}
	metadata := FetchAllReleaseMetadata(client, version)
	release, ok := metadata["macos"]
	if !ok {
		logx.Info("No macOS release metadata found for version " + version)
		return nil
	}
	sparkleVersion, _ := release["sparkle_version"].(string)
	buildDate, _ := release["build_date"].(string)
	artifacts, _ := release["artifacts"].(map[string]any)

	logx.Info(fmt.Sprintf("\n%s\nAPPCAST SNIPPETS FOR v%s\n%s", strings.Repeat("=", 60), version, strings.Repeat("=", 60)))
	archToFile := map[string]string{"arm64": "appcast.xml", "x64": "appcast-x86_64.xml", "universal": "appcast.xml"}
	for _, arch := range []string{"arm64", "x64", "universal"} {
		artifact, ok := artifacts[arch].(map[string]any)
		if !ok {
			continue
		}
		if _, ok := artifact["sparkle_signature"]; !ok {
			logx.Warning(arch + " artifact missing sparkle_signature")
		}
		logx.Info(fmt.Sprintf("\n%s (%s):", archToFile[arch], arch))
		fmt.Fprintln(logx.Out, GenerateAppcastItem(artifact, version, sparkleVersion, buildDate))
	}
	return nil
}

// Publish copies versioned artifacts to download/ paths
// (release/publish.py PublishModule).
func (d *Deps) Publish(version string) error {
	client, err := d.client()
	if err != nil {
		return err
	}
	metadata := FetchAllReleaseMetadata(client, version)
	if len(metadata) == 0 {
		return fmt.Errorf("no release metadata found for version %s", version)
	}
	logx.Info(fmt.Sprintf("\nPublishing v%s to download/ paths", version))

	succeeded, failed := 0, 0
	for _, platform := range Platforms {
		release, ok := metadata[platform]
		if !ok {
			logx.Warning("Skipping " + platform + ": no release metadata")
			continue
		}
		artifacts, _ := release["artifacts"].(map[string]any)
		mapping := DownloadPathMapping[platform]
		logx.Info("\n" + platformDisplayNames[platform] + ":")
		for _, artifactKey := range sortedKeys(artifacts) {
			destPath, ok := mapping[artifactKey]
			if !ok {
				logx.Info(fmt.Sprintf("  Skipping %s: no download path mapping", artifactKey))
				continue
			}
			artifact := artifacts[artifactKey].(map[string]any)
			filename, _ := artifact["filename"].(string)
			sourceKey := fmt.Sprintf("releases/%s/%s/%s", version, platform, filename)
			logx.Info(fmt.Sprintf("  Copying %s → %s", filename, destPath))
			if err := client.Copy(sourceKey, destPath); err != nil {
				logx.Error(fmt.Sprintf("Failed to copy %s → %s: %v", sourceKey, destPath, err))
				failed++
				continue
			}
			logx.Success(fmt.Sprintf("    ✓ Published to %s/%s", envx.R2CDNBaseURL(), destPath))
			succeeded++
		}
	}
	if failed == 0 {
		logx.Success(fmt.Sprintf("Published %d artifact(s) to download/ paths", succeeded))
		return nil
	}
	logx.Warning(fmt.Sprintf("Published %d/%d artifact(s)", succeeded, succeeded+failed))
	return fmt.Errorf("%d publish copies failed", failed)
}

// Download fetches a version's artifacts (release/download.py DownloadModule).
func (d *Deps) Download(version, osFilter, outputDir string) error {
	osNameMap := map[string]string{"macos": "macos", "mac": "macos", "darwin": "macos",
		"windows": "win", "win": "win", "linux": "linux"}
	if osFilter != "" {
		normalized, ok := osNameMap[strings.ToLower(osFilter)]
		if !ok {
			return fmt.Errorf("invalid --os value: %s. Valid: macos, windows, linux", osFilter)
		}
		osFilter = normalized
	}

	client, err := d.client()
	if err != nil {
		return err
	}
	metadata := FetchAllReleaseMetadata(client, version)
	if len(metadata) == 0 {
		return fmt.Errorf("no release metadata found for version %s", version)
	}

	downloadDir := filepath.Join(os.TempDir(), "browseros-releases", version)
	if outputDir != "" {
		downloadDir = filepath.Join(outputDir, version)
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return err
	}
	logx.Info("\nDownloading to " + downloadDir + "\n")

	platforms := Platforms
	if osFilter != "" {
		platforms = []string{osFilter}
	}
	for _, platform := range platforms {
		release, ok := metadata[platform]
		if !ok {
			continue
		}
		artifacts, _ := release["artifacts"].(map[string]any)
		if len(artifacts) == 0 {
			continue
		}
		logx.Info(platformDisplayNames[platform] + ":")
		for _, key := range sortedKeys(artifacts) {
			artifact := artifacts[key].(map[string]any)
			url, _ := artifact["url"].(string)
			filename, _ := artifact["filename"].(string)
			if url == "" || filename == "" {
				continue
			}
			dest := filepath.Join(downloadDir, filename)
			if err := d.fetcher().Download(url, dest); err != nil {
				logx.Error(fmt.Sprintf("  %s - FAILED: %v", filename, err))
				continue
			}
			info, _ := os.Stat(dest)
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			logx.Info(fmt.Sprintf("  %s (%s)", filename, FormatSize(size)))
		}
		logx.Info("")
	}
	logx.Info("Downloaded to: " + downloadDir)
	return nil
}

// RepoFromGit derives owner/name from the origin remote
// (release/common.py get_repo_from_git).
func (d *Deps) RepoFromGit() string {
	res, err := d.runner().Run(execx.Cmd{Args: []string{"git", "remote", "get-url", "origin"}})
	if err != nil || res.Code != 0 {
		return ""
	}
	remote := strings.TrimSpace(res.Stdout)
	if !strings.Contains(remote, "github.com") {
		return ""
	}
	if strings.HasPrefix(remote, "git@") {
		parts := strings.Split(remote, ":")
		return strings.TrimSuffix(parts[len(parts)-1], ".git")
	}
	segments := strings.Split(remote, "/")
	if len(segments) < 2 {
		return ""
	}
	return strings.TrimSuffix(strings.Join(segments[len(segments)-2:], "/"), ".git")
}

// GenerateReleaseNotes builds the markdown body
// (release/common.py generate_release_notes).
func GenerateReleaseNotes(version string, metadata map[string]map[string]any) string {
	chromiumVersion := "unknown"
	for _, platform := range Platforms {
		if release, ok := metadata[platform]; ok {
			if cv, ok := release["chromium_version"].(string); ok && cv != "" {
				chromiumVersion = cv
				break
			}
		}
	}
	notes := fmt.Sprintf("## BrowserOS v%s\n\nChromium version: %s\n\n### Downloads\n\n", version, chromiumVersion)
	for _, platform := range Platforms {
		release, ok := metadata[platform]
		if !ok {
			continue
		}
		notes += "**" + platformDisplayNames[platform] + ":**\n"
		artifacts, _ := release["artifacts"].(map[string]any)
		for _, key := range sortedKeys(artifacts) {
			artifact := artifacts[key].(map[string]any)
			notes += fmt.Sprintf("- [%v](%v)\n", artifact["filename"], artifact["url"])
		}
		notes += "\n"
	}
	return notes
}

// NormalizeVersion strips a version to MAJOR.MINOR.BUILD
// (release/github.py normalize_version).
func NormalizeVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ".")
	}
	return version
}

// GithubOptions configures GithubCreate.
type GithubOptions struct {
	Version    string
	Repo       string // "" → derive from git remote
	Title      string // "" → v{version}
	Draft      bool
	SkipUpload bool
}

// GithubCreate creates a GitHub release from R2 artifacts via gh
// (release/github.py GithubModule).
func (d *Deps) GithubCreate(opts GithubOptions) error {
	runner := d.runner()
	if res, err := runner.Run(execx.Cmd{Args: []string{"gh", "--version"}}); err != nil || res.Code != 0 {
		return fmt.Errorf("gh CLI not found. Install from: https://cli.github.com")
	}
	repo := opts.Repo
	if repo == "" {
		repo = d.RepoFromGit()
	}
	if repo == "" {
		return fmt.Errorf("could not determine GitHub repo; pass --repo owner/name")
	}
	// Tag/title/notes use the 3-part version; the R2 metadata fetch keeps the
	// raw version (github.py execute()).
	tagVersion := NormalizeVersion(opts.Version)
	title := opts.Title
	if title == "" {
		title = "v" + tagVersion
	}

	client, err := d.client()
	if err != nil {
		return err
	}
	metadata := FetchAllReleaseMetadata(client, opts.Version)
	if len(metadata) == 0 {
		return fmt.Errorf("no release metadata found for version %s", opts.Version)
	}
	notes := GenerateReleaseNotes(tagVersion, metadata)

	createArgs := []string{"gh", "release", "create", "v" + tagVersion,
		"--repo", repo, "--title", title, "--notes", notes}
	if opts.Draft {
		createArgs = append(createArgs, "--draft")
	}
	res, err := runner.Run(execx.Cmd{Args: createArgs})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		if strings.Contains(res.Stderr, "already exists") {
			return fmt.Errorf("release v%s already exists", tagVersion)
		}
		return fmt.Errorf("gh release create failed: %s", strings.TrimSpace(res.Stderr))
	}
	logx.Success("Created GitHub release " + title)

	if opts.SkipUpload {
		return nil
	}
	tmpDir, err := os.MkdirTemp("", "gh-release-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	for _, platform := range Platforms {
		release, ok := metadata[platform]
		if !ok {
			continue
		}
		artifacts, _ := release["artifacts"].(map[string]any)
		for _, key := range sortedKeys(artifacts) {
			artifact := artifacts[key].(map[string]any)
			url, _ := artifact["url"].(string)
			filename, _ := artifact["filename"].(string)
			if url == "" || filename == "" {
				continue
			}
			localPath := filepath.Join(tmpDir, filename)
			logx.Info("  Downloading " + filename + "...")
			if err := d.fetcher().Download(url, localPath); err != nil {
				return fmt.Errorf("failed to download %s: %w", filename, err)
			}
			logx.Info("  Uploading " + filename + " to GitHub...")
			res, err := runner.Run(execx.Cmd{Args: []string{
				"gh", "release", "upload", "v" + tagVersion, localPath, "--repo", repo}})
			if err != nil || res.Code != 0 {
				return fmt.Errorf("failed to upload %s to GitHub release", filename)
			}
		}
	}
	logx.Success("GitHub release assets uploaded")
	return nil
}
