// Package extensions ports build/modules/extensions/bundled_extensions.py:
// download extensions declared in the CDN update manifest and write
// bundled_extensions.json.
package extensions

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/fetch"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// ExtensionInfo is one extension parsed from the update manifest.
type ExtensionInfo struct {
	ID       string
	Version  string
	Codebase string
}

// gupdate models the Google Update protocol response. encoding/xml matches
// local names regardless of the document's default namespace, which covers
// both the namespaced and namespace-less manifests Python accepted.
type gupdate struct {
	Apps []struct {
		AppID       string `xml:"appid,attr"`
		UpdateCheck *struct {
			Version  string `xml:"version,attr"`
			Codebase string `xml:"codebase,attr"`
		} `xml:"updatecheck"`
	} `xml:"app"`
}

// ParseManifestXML extracts extension entries from the update manifest.
func ParseManifestXML(content []byte) ([]ExtensionInfo, error) {
	var doc gupdate
	if err := xml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse manifest XML: %w", err)
	}
	var extensions []ExtensionInfo
	for _, app := range doc.Apps {
		if app.AppID == "" || app.UpdateCheck == nil {
			continue
		}
		if app.UpdateCheck.Version == "" || app.UpdateCheck.Codebase == "" {
			continue
		}
		extensions = append(extensions, ExtensionInfo{
			ID:       app.AppID,
			Version:  app.UpdateCheck.Version,
			Codebase: app.UpdateCheck.Codebase,
		})
	}
	return extensions, nil
}

// Bundled is the bundled_extensions module.
type Bundled struct {
	Fetcher fetch.Fetcher
}

func NewBundled() *Bundled { return &Bundled{} }

func (Bundled) Name() string { return "bundled_extensions" }
func (Bundled) Description() string {
	return "Download and bundle extensions from CDN update manifest"
}

func (Bundled) Validate(ctx *buildctx.Context) error {
	if ctx.ChromiumSrc == "" {
		return fmt.Errorf("chromium source directory not found: %s", ctx.ChromiumSrc)
	}
	if _, err := os.Stat(ctx.ChromiumSrc); err != nil {
		return fmt.Errorf("chromium source directory not found: %s", ctx.ChromiumSrc)
	}
	return nil
}

func (m Bundled) Execute(ctx *buildctx.Context) error {
	logx.Info("\n📦 Bundling extensions from CDN manifest...")
	fetcher := m.Fetcher
	if fetcher == nil {
		fetcher = fetch.Default()
	}

	outputDir := OutputDir(ctx)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	logx.Info(fmt.Sprintf("  Output: %s", outputDir))

	manifestURL := ctx.ExtensionsManifestURL()
	logx.Info(fmt.Sprintf("  Fetching manifest: %s", manifestURL))
	manifest, err := fetch.Get(fetcher, manifestURL)
	if err != nil {
		return fmt.Errorf("failed to fetch manifest: %w", err)
	}

	extensions, err := ParseManifestXML(manifest)
	if err != nil {
		return err
	}
	if len(extensions) == 0 {
		return fmt.Errorf("no extensions found in manifest")
	}
	logx.Info(fmt.Sprintf("  Found %d extensions in manifest", len(extensions)))

	for _, ext := range extensions {
		logx.Info(fmt.Sprintf("  Downloading %s v%s...", ext.ID, ext.Version))
		dest := filepath.Join(outputDir, ext.ID+".crx")
		if err := fetcher.Download(ext.Codebase, dest); err != nil {
			return fmt.Errorf("failed to download %s: %w", ext.ID, err)
		}
	}

	if err := writeJSON(extensions, outputDir); err != nil {
		return err
	}
	logx.Success(fmt.Sprintf("Bundled %d extensions successfully", len(extensions)))
	return nil
}

// OutputDir is chrome/browser/browseros/bundled_extensions in chromium_src.
func OutputDir(ctx *buildctx.Context) string {
	return filepath.Join(ctx.ChromiumSrc, "chrome", "browser", "browseros", "bundled_extensions")
}

func writeJSON(extensions []ExtensionInfo, outputDir string) error {
	data := map[string]map[string]string{}
	for _, ext := range extensions {
		data[ext.ID] = map[string]string{
			"external_crx":     ext.ID + ".crx",
			"external_version": ext.Version,
		}
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	jsonPath := filepath.Join(outputDir, "bundled_extensions.json")
	if err := os.WriteFile(jsonPath, encoded, 0o644); err != nil {
		return err
	}
	logx.Info(fmt.Sprintf("  Generated %s", filepath.Base(jsonPath)))
	return nil
}
