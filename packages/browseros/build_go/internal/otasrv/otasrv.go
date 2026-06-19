// Package otasrv ports build/modules/ota: the BrowserOS Server OTA release
// flow — download staged server resources from R2, codesign, zip, Sparkle-
// sign, upload, and maintain the channel appcasts.
package otasrv

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/resources"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/modules/sign"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/r2"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/serverbin"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/sparkle"
)

// artifactR2Key is the staged-server zip location (ota/server.py).
const artifactR2Key = "artifacts/server/latest/browseros-server-resources-%s.zip"

// Platform describes one server OTA target (ota/common.py SERVER_PLATFORMS).
type Platform struct {
	Name   string
	Binary string
	Target string
	OS     string
	Arch   string
}

// ServerPlatforms in release order.
var ServerPlatforms = []Platform{
	{"darwin_arm64", "browseros-server-darwin-arm64", "darwin-arm64", "macos", "arm64"},
	{"darwin_x64", "browseros-server-darwin-x64", "darwin-x64", "macos", "x86_64"},
	{"linux_arm64", "browseros-server-linux-arm64", "linux-arm64", "linux", "arm64"},
	{"linux_x64", "browseros-server-linux-x64", "linux-x64", "linux", "x86_64"},
	{"windows_x64", "browseros-server-windows-x64.exe", "windows-x64", "windows", "x86_64"},
}

// SignedArtifact pairs a built zip with its Sparkle signature.
type SignedArtifact struct {
	Platform  string
	ZipName   string
	Signature string
	Length    int64
	OS        string
	Arch      string
}

// ExistingAppcast is parsed from a channel appcast file.
type ExistingAppcast struct {
	Version   string
	PubDate   string
	Artifacts map[string]SignedArtifact
}

// AppcastPath returns the channel appcast under build/config/appcast
// (ota/common.py get_appcast_path).
func AppcastPath(rootDir, channel string) string {
	name := "appcast-server.xml"
	if channel == "alpha" {
		name = "appcast-server.alpha.xml"
	}
	return filepath.Join(rootDir, "build", "config", "appcast", name)
}

// AppcastR2Key is the published key for a channel.
func AppcastR2Key(channel string) string {
	if channel == "alpha" {
		return "appcast-server.alpha.xml"
	}
	return "appcast-server.xml"
}

type xmlEnclosure struct {
	URL       string `xml:"url,attr"`
	OS        string `xml:"os,attr"`
	Arch      string `xml:"arch,attr"`
	Signature string `xml:"edSignature,attr"`
	Length    int64  `xml:"length,attr"`
}

type xmlAppcast struct {
	Channel struct {
		Item struct {
			Version    string         `xml:"version"`
			PubDate    string         `xml:"pubDate"`
			Enclosures []xmlEnclosure `xml:"enclosure"`
		} `xml:"item"`
	} `xml:"channel"`
}

var platformFromZipRe = regexp.MustCompile(`_([a-z]+_[a-z0-9]+)\.zip$`)

// ParseExistingAppcast reads a single-item appcast
// (ota/common.py parse_existing_appcast). nil when missing/unparseable.
func ParseExistingAppcast(path string) *ExistingAppcast {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc xmlAppcast
	if err := xml.Unmarshal(content, &doc); err != nil {
		logx.Error("Malformed appcast XML: " + err.Error())
		return nil
	}
	item := doc.Channel.Item
	if item.Version == "" {
		return nil
	}
	existing := &ExistingAppcast{Version: item.Version, PubDate: item.PubDate, Artifacts: map[string]SignedArtifact{}}
	for _, enclosure := range item.Enclosures {
		if enclosure.URL == "" || enclosure.OS == "" || enclosure.Arch == "" || enclosure.Signature == "" {
			continue
		}
		filename := enclosure.URL[strings.LastIndex(enclosure.URL, "/")+1:]
		match := platformFromZipRe.FindStringSubmatch(filename)
		if match == nil {
			continue
		}
		existing.Artifacts[match[1]] = SignedArtifact{
			Platform:  match[1],
			ZipName:   filename,
			Signature: enclosure.Signature,
			Length:    enclosure.Length,
			OS:        enclosure.OS,
			Arch:      enclosure.Arch,
		}
	}
	return existing
}

// GenerateServerAppcast renders the channel appcast, merging with an existing
// same-version appcast (ota/common.py generate_server_appcast).
func GenerateServerAppcast(version string, artifacts []SignedArtifact, channel string, existing *ExistingAppcast) string {
	title, appcastURL := "BrowserOS Server", "https://cdn.browseros.com/appcast-server.xml"
	if channel == "alpha" {
		title, appcastURL = "BrowserOS Server (Alpha)", "https://cdn.browseros.com/appcast-server.alpha.xml"
	}

	var pubDate string
	var final []SignedArtifact
	if existing != nil && existing.Version == version {
		pubDate = existing.PubDate
		merged := map[string]SignedArtifact{}
		for platform, artifact := range existing.Artifacts {
			merged[platform] = artifact
		}
		for _, artifact := range artifacts {
			merged[platform(artifact)] = artifact
		}
		for _, artifact := range merged {
			final = append(final, artifact)
		}
		logx.Info(fmt.Sprintf("Merging with existing appcast (kept %d existing, added/updated %d platforms)",
			len(existing.Artifacts), len(artifacts)))
	} else {
		pubDate = time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000")
		final = artifacts
		if existing != nil {
			logx.Info(fmt.Sprintf("Version changed (%s -> %s), replacing appcast", existing.Version, version))
		}
	}
	sort.Slice(final, func(i, j int) bool { return platform(final[i]) < platform(final[j]) })

	var enclosures []string
	for _, artifact := range final {
		comment := strings.ToUpper(artifact.OS[:1]) + artifact.OS[1:] + " " + artifact.Arch
		if artifact.OS == "macos" {
			comment = "macOS " + artifact.Arch
		}
		zipName := fmt.Sprintf("browseros_server_%s_%s.zip", version, platform(artifact))
		enclosures = append(enclosures, fmt.Sprintf(`      <!-- %s -->
      <enclosure
        url="https://cdn.browseros.com/server/%s"
        sparkle:os="%s"
        sparkle:arch="%s"
        sparkle:edSignature="%s"
        length="%d"
        type="application/zip"/>`, comment, zipName, artifact.OS, artifact.Arch, artifact.Signature, artifact.Length))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<rss xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle" version="2.0">
  <channel>
    <title>%s</title>
    <link>%s</link>
    <description>BrowserOS Server binary updates</description>
    <language>en</language>

    <item>
      <sparkle:version>%s</sparkle:version>
      <pubDate>%s</pubDate>

%s
    </item>

  </channel>
</rss>
`, title, appcastURL, version, pubDate, strings.Join(enclosures, "\n\n"))
}

func platform(a SignedArtifact) string { return a.Platform }

// CreateServerBundleZip zips an extracted resources/ tree into a Sparkle
// payload with resources/-rooted entries (ota/common.py).
func CreateServerBundleZip(resourcesDir, outputZip string) error {
	info, err := os.Stat(resourcesDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("resources dir not found: %s", resourcesDir)
	}
	bundleRoot := filepath.Dir(resourcesDir)
	out, err := os.Create(outputZip)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(out)

	var files []string
	filepath.WalkDir(resourcesDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	for _, path := range files {
		rel, err := filepath.Rel(bundleRoot, path)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate}
		header.SetMode(info.Mode())
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, src)
		src.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	if err := writer.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	logx.Success("Created " + filepath.Base(outputZip))
	return nil
}

// Deps carries injectables for the OTA flow.
type Deps struct {
	Client *r2.Client
}

func (d *Deps) client() (*r2.Client, error) {
	if d.Client != nil {
		return d.Client, nil
	}
	return r2.NewFromEnv()
}

// FilterPlatforms applies the comma-separated --platform filter.
func FilterPlatforms(filter string) []Platform {
	if filter == "" {
		return ServerPlatforms
	}
	requested := map[string]bool{}
	for _, name := range strings.Split(filter, ",") {
		requested[strings.TrimSpace(name)] = true
	}
	var out []Platform
	for _, platform := range ServerPlatforms {
		if requested[platform.Name] {
			out = append(out, platform)
		}
	}
	return out
}

// signBundleMacOS codesigns every known server binary in the staged tree
// (ota/sign_binary.py sign_server_bundle_macos).
func signBundleMacOS(ctx *buildctx.Context, stagingResources string) error {
	binDir := filepath.Join(stagingResources, "bin")
	var binaries []string
	filepath.WalkDir(binDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if _, ok := serverbin.MacOSSignSpecFor(path); ok {
			binaries = append(binaries, path)
		}
		return nil
	})
	sort.Strings(binaries)
	if len(binaries) == 0 {
		return fmt.Errorf("no signable server binaries found in %s", binDir)
	}
	cert := envx.MacOSCertificateName()
	for _, binary := range binaries {
		spec, _ := serverbin.MacOSSignSpecFor(binary)
		entitlements := ""
		if spec.Entitlements != "" {
			candidate := filepath.Join(ctx.EntitlementsDir(), spec.Entitlements)
			if _, err := os.Stat(candidate); err == nil {
				entitlements = candidate
			}
		}
		cmd := []string{"codesign", "--sign", cert, "--force", "--timestamp",
			"--identifier", "com.browseros." + spec.IdentifierSuffix,
			"--options", spec.Options}
		if entitlements != "" {
			cmd = append(cmd, "--entitlements", entitlements)
		}
		cmd = append(cmd, binary)
		if _, err := execx.Checked(ctx.Runner, execx.Cmd{Args: cmd, Stream: logx.Out}); err != nil {
			return err
		}
		res, _ := ctx.Runner.Run(execx.Cmd{Args: []string{"codesign", "--verify", "--verbose=2", binary}})
		if res.Code != 0 {
			return fmt.Errorf("codesign verification failed for %s", binary)
		}
	}
	return nil
}

// notarizeZip submits a zip and requires "status: Accepted"
// (ota/sign_binary.py notarize_macos_zip; zips cannot be stapled).
func notarizeZip(ctx *buildctx.Context, zipPath string) error {
	store, _ := ctx.Runner.Run(execx.Cmd{Args: []string{
		"xcrun", "notarytool", "store-credentials", "notarytool-profile",
		"--apple-id", envx.MacOSNotarizationAppleID(),
		"--team-id", envx.MacOSNotarizationTeamID(),
		"--password", envx.MacOSNotarizationPassword()}})
	args := []string{"xcrun", "notarytool", "submit", zipPath, "--wait"}
	if store.Code == 0 {
		args = append(args, "--keychain-profile", "notarytool-profile")
	} else {
		args = append(args,
			"--apple-id", envx.MacOSNotarizationAppleID(),
			"--team-id", envx.MacOSNotarizationTeamID(),
			"--password", envx.MacOSNotarizationPassword())
	}
	res, _ := ctx.Runner.Run(execx.Cmd{Args: args, Stream: logx.Out})
	if res.Code != 0 || !strings.Contains(res.Stdout, "status: Accepted") {
		return fmt.Errorf("notarization failed for %s", filepath.Base(zipPath))
	}
	return nil
}

// ServerRelease runs the full OTA flow (ota/server.py ServerOTAModule).
func ServerRelease(ctx *buildctx.Context, deps *Deps, version, channel, platformFilter string) error {
	if version == "" {
		return fmt.Errorf("version is required")
	}
	if channel != "alpha" && channel != "prod" {
		return fmt.Errorf("channel must be 'alpha' or 'prod'")
	}
	if ctx.Platform.IsMacOS() && envx.MacOSCertificateName() == "" {
		return fmt.Errorf("MACOS_CERTIFICATE_NAME required for signing")
	}
	if ctx.Platform.IsWindows() && envx.CodeSignToolPath() == "" {
		return fmt.Errorf("CODE_SIGN_TOOL_PATH required for signing")
	}
	if !envx.HasR2Config() {
		return fmt.Errorf("R2 configuration not set. Required env vars: R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY")
	}

	client, err := deps.client()
	if err != nil {
		return err
	}
	platforms := FilterPlatforms(platformFilter)
	if len(platforms) == 0 {
		return fmt.Errorf("no matching platforms for filter %q", platformFilter)
	}

	logx.Info(fmt.Sprintf("\n🚀 BrowserOS Server OTA v%s (%s)", version, channel))
	binariesDir, err := os.MkdirTemp("", "ota_artifacts_")
	if err != nil {
		return err
	}
	defer os.RemoveAll(binariesDir)
	stagingDir, err := os.MkdirTemp("", "ota_staging_")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stagingDir)

	// Download + extract staged server resources.
	logx.Info("📥 Downloading server artifacts from R2...")
	for _, platform := range platforms {
		key := fmt.Sprintf(artifactR2Key, platform.Target)
		zipPath := filepath.Join(binariesDir, platform.Target+".zip")
		logx.Info("  Downloading " + platform.Target + "...")
		if err := client.GetFile(key, zipPath); err != nil {
			return fmt.Errorf("failed to download artifact: %s: %w", key, err)
		}
		if _, err := resources.ExtractArtifactZip(zipPath, filepath.Join(binariesDir, platform.Target), ctx.Platform.IsWindows()); err != nil {
			return err
		}
		os.Remove(zipPath)
	}

	// Sign + zip + sparkle-sign per platform; fail fast.
	var signedArtifacts []SignedArtifact
	for _, platform := range platforms {
		logx.Info(fmt.Sprintf("\n📦 Processing %s...", platform.Name))
		sourceResources := filepath.Join(binariesDir, platform.Target, "resources")
		if info, err := os.Stat(sourceResources); err != nil || !info.IsDir() {
			return fmt.Errorf("resources dir not found for %s", platform.Name)
		}
		stagingResources := filepath.Join(stagingDir, platform.Name, "resources")
		if err := copyTree(sourceResources, stagingResources); err != nil {
			return err
		}

		switch platform.OS {
		case "macos":
			if !ctx.Platform.IsMacOS() {
				logx.Warning(fmt.Sprintf("macOS signing requires macOS - leaving %s unsigned", platform.Name))
			} else if err := signBundleMacOS(ctx, stagingResources); err != nil {
				return fmt.Errorf("signing failed for %s: %w", platform.Name, err)
			}
		case "windows":
			binaries := serverbin.ExpectedWindowsBinaryPaths(filepath.Join(stagingResources, "bin"))
			var existing []string
			for _, binary := range binaries {
				if _, err := os.Stat(binary); err == nil {
					existing = append(existing, binary)
				}
			}
			if len(existing) > 0 {
				if err := sign.SignWithCodeSignTool(ctx, existing); err != nil {
					return fmt.Errorf("signing failed for %s: %w", platform.Name, err)
				}
			}
		default:
			logx.Info("No code signing for Linux binaries")
		}

		zipName := fmt.Sprintf("browseros_server_%s_%s.zip", version, platform.Name)
		zipPath := filepath.Join(stagingDir, zipName)
		if err := CreateServerBundleZip(stagingResources, zipPath); err != nil {
			return fmt.Errorf("failed to create bundle for %s: %w", platform.Name, err)
		}
		if platform.OS == "macos" && ctx.Platform.IsMacOS() {
			if err := notarizeZip(ctx, zipPath); err != nil {
				return err
			}
		}

		logx.Info("Signing " + zipName + " with Sparkle...")
		signature, length, err := sparkle.SignFileWithEnv(zipPath)
		if err != nil {
			return fmt.Errorf("sparkle signing failed for %s: %w", platform.Name, err)
		}
		logx.Success(fmt.Sprintf("  %s: %d bytes", platform.Name, length))
		signedArtifacts = append(signedArtifacts, SignedArtifact{
			Platform: platform.Name, ZipName: zipName,
			Signature: signature, Length: length,
			OS: platform.OS, Arch: platform.Arch,
		})
	}

	// Appcast + uploads.
	logx.Info("\n📝 Generating appcast...")
	appcastPath := AppcastPath(ctx.RootDir, channel)
	existing := ParseExistingAppcast(appcastPath)
	content := GenerateServerAppcast(version, signedArtifacts, channel, existing)
	if err := os.MkdirAll(filepath.Dir(appcastPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(appcastPath, []byte(content), 0o644); err != nil {
		return err
	}
	logx.Success("Appcast saved to: " + appcastPath)

	logx.Info("\n📤 Uploading artifacts to R2...")
	for _, artifact := range signedArtifacts {
		if err := client.PutFile(filepath.Join(stagingDir, artifact.ZipName), "server/"+artifact.ZipName, "application/zip"); err != nil {
			return fmt.Errorf("failed to upload %s: %w", artifact.ZipName, err)
		}
	}

	logx.Success(fmt.Sprintf("✅ Server OTA v%s (%s) artifacts ready!", version, channel))
	for _, artifact := range signedArtifacts {
		logx.Info("  https://cdn.browseros.com/server/" + artifact.ZipName)
	}
	logx.Info("\n📋 Next step: Run 'browseros ota server release-appcast' to make the release live")
	return nil
}

// ReleaseAppcast publishes the channel appcast (cli/ota.py
// server_release_appcast).
func ReleaseAppcast(rootDir string, deps *Deps, channel, customFile string) error {
	sourcePath := customFile
	if sourcePath == "" {
		sourcePath = AppcastPath(rootDir, channel)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return fmt.Errorf("appcast file not found: %s (run 'browseros ota server release' first)", sourcePath)
	}
	key := AppcastR2Key(channel)
	logx.Info(fmt.Sprintf("📤 Uploading %s to %s...", filepath.Base(sourcePath), key))
	if !envx.HasR2Config() {
		return fmt.Errorf("R2 configuration not set. Required env vars: R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY")
	}
	client, err := deps.client()
	if err != nil {
		return err
	}
	if err := client.PutFile(sourcePath, key, "application/xml"); err != nil {
		return err
	}
	logx.Success("✅ Published: https://cdn.browseros.com/" + key)
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode())
	})
}
