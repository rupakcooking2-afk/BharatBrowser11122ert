// Package buildctx ports build/common/context.py: the Context object that
// threads all build state (paths, versions, platform, artifact naming)
// through the module pipeline.
package buildctx

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/paths"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

// Context is the one place for all build state (common/context.py:Context).
type Context struct {
	RootDir      string // packages/browseros
	ChromiumSrc  string
	OutDir       string // relative to ChromiumSrc, e.g. out/Default_arm64
	Architecture string
	BuildType    string

	ChromiumVersion          string // e.g. 148.0.7778.97
	BrowserOSBuildOffset     string // e.g. 162
	BrowserOSChromiumVersion string // e.g. 148.0.7940.97
	SemanticVersion          string // e.g. 0.46.17
	ReleaseVersion           string // overrides SemanticVersion for release ops
	GithubRepo               string

	ChromiumAppName      string
	BrowserOSAppName     string
	BrowserOSAppBaseName string
	SparkleVersion       string

	Platform  platform.Platform
	Runner    execx.Runner
	StartTime time.Time

	// FixedAppPath, when set, is returned by AppPath() directly —
	// UniversalBuildModule pins per-arch and universal app paths through it.
	FixedAppPath string

	// SparkleSignatures maps artifact filename → Ed25519 signature, set by
	// the sparkle_sign module and consumed by upload/appcast generation
	// (Python kept this in ctx.artifacts["sparkle_signatures"]).
	SparkleSignatures map[string]SparkleSig

	artifacts map[string]string
}

// SparkleSig pairs a base64 Ed25519 signature with the signed file's length.
type SparkleSig struct {
	Signature string
	Length    int64
}

// Options configures New; zero values resolve like the Python defaults.
type Options struct {
	ChromiumSrc  string
	Architecture string
	BuildType    string
	Platform     *platform.Platform // nil → host platform
	RootDir      string             // "" → paths.Root()
	Runner       execx.Runner       // nil → execx.Default()
}

// New builds a fully derived Context (mirrors Context.__post_init__).
func New(opts Options) (*Context, error) {
	plat := platform.Current()
	if opts.Platform != nil {
		plat = *opts.Platform
	}
	root := opts.RootDir
	if root == "" {
		var err error
		root, err = paths.Root()
		if err != nil {
			return nil, err
		}
	}
	arch := opts.Architecture
	if arch == "" {
		arch = plat.Arch
	}
	buildType := opts.BuildType
	if buildType == "" {
		buildType = "debug"
	}
	runner := opts.Runner
	if runner == nil {
		runner = execx.Default()
	}

	ctx := &Context{
		RootDir:              root,
		ChromiumSrc:          opts.ChromiumSrc,
		Architecture:         arch,
		BuildType:            buildType,
		BrowserOSAppBaseName: "BrowserOS",
		SparkleVersion:       "2.7.0",
		Platform:             plat,
		Runner:               runner,
		StartTime:            time.Now(),
		SparkleSignatures:    map[string]SparkleSig{},
		artifacts:            map[string]string{},
	}

	// Platform-specific app names (context.py __post_init__).
	switch {
	case plat.IsWindows():
		ctx.ChromiumAppName = "chrome" + plat.ExeExt()
		ctx.BrowserOSAppName = ctx.BrowserOSAppBaseName + plat.ExeExt()
	case plat.IsMacOS():
		ctx.ChromiumAppName = "Chromium.app"
		ctx.BrowserOSAppName = ctx.BrowserOSAppBaseName + ".app"
	default:
		ctx.ChromiumAppName = "chrome"
		ctx.BrowserOSAppName = strings.ToLower(ctx.BrowserOSAppBaseName)
	}

	// Architecture-specific out dir with platform separator.
	if plat.IsWindows() {
		ctx.OutDir = `out\Default_` + arch
	} else {
		ctx.OutDir = "out/Default_" + arch
	}

	// Version files.
	var versionParts map[string]string
	ctx.ChromiumVersion, versionParts = loadChromiumVersion(root)
	ctx.BrowserOSBuildOffset = loadBuildOffset(root)
	ctx.SemanticVersion = loadSemanticVersion(root)

	if ctx.ChromiumVersion != "" && ctx.BrowserOSBuildOffset != "" && len(versionParts) > 0 {
		build, err1 := strconv.Atoi(versionParts["BUILD"])
		offset, err2 := strconv.Atoi(ctx.BrowserOSBuildOffset)
		if err1 == nil && err2 == nil {
			ctx.BrowserOSChromiumVersion = fmt.Sprintf("%s.%s.%d.%s",
				versionParts["MAJOR"], versionParts["MINOR"], build+offset, versionParts["PATCH"])
		}
	}
	return ctx, nil
}

func loadChromiumVersion(root string) (string, map[string]string) {
	parts := map[string]string{}
	content, err := os.ReadFile(filepath.Join(root, "CHROMIUM_VERSION"))
	if err != nil {
		return "", parts
	}
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			parts[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if parts["MAJOR"] == "" {
		return "", parts
	}
	return fmt.Sprintf("%s.%s.%s.%s", parts["MAJOR"], parts["MINOR"], parts["BUILD"], parts["PATCH"]), parts
}

func loadBuildOffset(root string) string {
	content, err := os.ReadFile(filepath.Join(root, "build", "config", "BROWSEROS_BUILD_OFFSET"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

func loadSemanticVersion(root string) string {
	content, err := os.ReadFile(filepath.Join(root, "resources", "BROWSEROS_VERSION"))
	if err != nil {
		return ""
	}
	parts := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		parts[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	major := orDefault(parts["BROWSEROS_MAJOR"], "0")
	minor := orDefault(parts["BROWSEROS_MINOR"], "0")
	build := orDefault(parts["BROWSEROS_BUILD"], "0")
	patch := orDefault(parts["BROWSEROS_PATCH"], "0")
	switch {
	case patch != "0":
		return fmt.Sprintf("%s.%s.%s.%s", major, minor, build, patch)
	case build != "0":
		return fmt.Sprintf("%s.%s.%s", major, minor, build)
	default:
		return fmt.Sprintf("%s.%s.0", major, minor)
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// === Path getters (context.py "Path getter methods") ===

func (c *Context) ConfigDir() string   { return filepath.Join(c.RootDir, "build", "config") }
func (c *Context) GNConfigDir() string { return filepath.Join(c.ConfigDir(), "gn") }

// GNFlagsFile returns build/config/gn/flags.{platform}.{build_type}.gn.
func (c *Context) GNFlagsFile() string {
	return filepath.Join(c.GNConfigDir(), fmt.Sprintf("flags.%s.%s.gn", c.Platform.OS, c.BuildType))
}

func (c *Context) CopyResourcesConfig() string {
	return filepath.Join(c.ConfigDir(), "copy_resources.yaml")
}

func (c *Context) DownloadResourcesConfig() string {
	return filepath.Join(c.ConfigDir(), "download_resources.yaml")
}

func (c *Context) SparkleDir() string {
	return filepath.Join(c.ChromiumSrc, "third_party", "sparkle")
}

func (c *Context) SparkleURL() string {
	return fmt.Sprintf("https://github.com/sparkle-project/Sparkle/releases/download/%s/Sparkle-%s.tar.xz",
		c.SparkleVersion, c.SparkleVersion)
}

func (c *Context) ExtensionsManifestURL() string {
	return "https://cdn.browseros.com/extensions/update-manifest.alpha.xml"
}

func (c *Context) EntitlementsDir() string {
	return filepath.Join(c.RootDir, "resources", "entitlements")
}

func (c *Context) PkgDmgPath() string {
	return filepath.Join(c.ChromiumSrc, "chrome", "installer", "mac", "pkg-dmg")
}

// OutDirAbs is the absolute out dir (ChromiumSrc/OutDir).
func (c *Context) OutDirAbs() string {
	return filepath.Join(c.ChromiumSrc, filepath.FromSlash(strings.ReplaceAll(c.OutDir, `\`, "/")))
}

// AppPath resolves the built app path (context.py get_app_path): FixedAppPath
// wins; macOS debug builds prefer "<base> Dev.app" when present.
func (c *Context) AppPath() string {
	if c.FixedAppPath != "" {
		return c.FixedAppPath
	}
	if c.BuildType == "debug" && c.Platform.IsMacOS() {
		devApp := filepath.Join(c.OutDirAbs(), c.BrowserOSAppBaseName+" Dev.app")
		if _, err := os.Stat(devApp); err == nil {
			return devApp
		}
	}
	return filepath.Join(c.OutDirAbs(), c.BrowserOSAppName)
}

func (c *Context) ChromiumAppPath() string {
	return filepath.Join(c.OutDirAbs(), c.ChromiumAppName)
}

func (c *Context) GNArgsFile() string {
	return filepath.Join(c.OutDirAbs(), "args.gn")
}

func (c *Context) NotarizationZip() string {
	return filepath.Join(c.OutDirAbs(), "notarize.zip")
}

// ArtifactName returns the standardized artifact filename
// (context.py get_artifact_name), e.g. BrowserOS_v0.31.0_arm64.dmg.
func (c *Context) ArtifactName(artifactType string) (string, error) {
	if c.SemanticVersion == "" {
		return "", fmt.Errorf("semantic_version is not set to generate artifact name")
	}
	version, base, arch := c.SemanticVersion, c.BrowserOSAppBaseName, c.Architecture
	switch artifactType {
	case "dmg":
		return fmt.Sprintf("%s_v%s_%s.dmg", base, version, arch), nil
	case "appimage":
		return fmt.Sprintf("%s_v%s_%s.AppImage", base, version, arch), nil
	case "deb":
		debArch := arch
		if arch == "x64" {
			debArch = "amd64"
		}
		return fmt.Sprintf("%s_v%s_%s.deb", base, version, debArch), nil
	case "installer":
		return fmt.Sprintf("%s_v%s_%s_installer.exe", base, version, arch), nil
	case "installer_zip":
		return fmt.Sprintf("%s_v%s_%s_installer.zip", base, version, arch), nil
	}
	return "", fmt.Errorf("unknown artifact type: %s", artifactType)
}

// SparkleBuildVersion is BUILD.PATCH of BrowserOSChromiumVersion
// (context.py get_sparkle_version), e.g. "7940.97".
func (c *Context) SparkleBuildVersion() (string, error) {
	if c.BrowserOSChromiumVersion == "" {
		return "", fmt.Errorf("browseros_chromium_version is not set")
	}
	parts := strings.Split(c.BrowserOSChromiumVersion, ".")
	if len(parts) < 4 {
		return "", fmt.Errorf("invalid browseros_chromium_version format: %s", c.BrowserOSChromiumVersion)
	}
	return parts[2] + "." + parts[3], nil
}

// ReleasePath is the R2 prefix for release artifacts, e.g.
// releases/0.31.0/macos/. Like context.py get_release_path it always uses
// the semantic version (release ops pass versions explicitly instead).
func (c *Context) ReleasePath(plat string) string {
	return fmt.Sprintf("releases/%s/%s/", c.SemanticVersion, plat)
}

// DistDir is <root>/releases/<semantic_version>.
func (c *Context) DistDir() string {
	return filepath.Join(c.RootDir, "releases", c.SemanticVersion)
}

// === Dev CLI paths ===

func (c *Context) PatchesDir() string {
	return filepath.Join(c.RootDir, "chromium_patches")
}

func (c *Context) ChromiumReplaceFilesDir() string {
	return filepath.Join(c.RootDir, "chromium_files")
}

func (c *Context) FeaturesYAMLPath() string {
	return filepath.Join(c.RootDir, "build", "features.yaml")
}

func (c *Context) PatchPathForFile(filePath string) string {
	return filepath.Join(c.PatchesDir(), filepath.FromSlash(filePath))
}

func (c *Context) SeriesPatchesDir() string {
	return filepath.Join(c.RootDir, "series_patches")
}

// BaseCommitFile is <root>/BASE_COMMIT (used by dev extract/apply).
func (c *Context) BaseCommitFile() string {
	return filepath.Join(c.RootDir, "BASE_COMMIT")
}

// === Artifact registry (context.py ArtifactRegistry) ===

func (c *Context) AddArtifact(name, path string) { c.artifacts[name] = path }

func (c *Context) Artifact(name string) (string, bool) {
	p, ok := c.artifacts[name]
	return p, ok
}

func (c *Context) Artifacts() map[string]string {
	out := make(map[string]string, len(c.artifacts))
	for k, v := range c.artifacts {
		out[k] = v
	}
	return out
}
