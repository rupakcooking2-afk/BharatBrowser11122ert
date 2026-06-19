// Package resources ports build/modules/resources (copy resources,
// chromium_replace, string_replaces) and the download_resources module from
// build/modules/storage/download.py.
package resources

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/config"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
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
	return os.Chmod(dst, info.Mode())
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
		return copyFile(path, target)
	})
}

// === resources (resources/resources.py) ===

// CopyOperation is one entry of copy_resources.yaml copy_operations.
type CopyOperation struct {
	Name        string   `yaml:"name"`
	Source      string   `yaml:"source"`
	Destination string   `yaml:"destination"`
	Type        string   `yaml:"type"`
	BuildType   string   `yaml:"build_type"`
	OS          []string `yaml:"os"`
	Arch        []string `yaml:"arch"`
}

type copyConfig struct {
	CopyOperations []CopyOperation `yaml:"copy_operations"`
}

type Copy struct{}

func NewCopy() *Copy { return &Copy{} }

func (Copy) Name() string        { return "resources" }
func (Copy) Description() string { return "Copy resources (icons, extensions) to Chromium" }

func (Copy) Validate(ctx *buildctx.Context) error {
	if _, err := os.Stat(ctx.CopyResourcesConfig()); err != nil {
		return fmt.Errorf("copy configuration file not found: %s", ctx.CopyResourcesConfig())
	}
	return nil
}

func (Copy) Execute(ctx *buildctx.Context) error {
	logx.Info("\n📦 Copying resources...")
	return CopyResources(ctx)
}

// shouldRunOperation applies the build_type/os/arch gates shared by copy and
// download operations.
func operationSkipReason(ctx *buildctx.Context, buildType string, osCond, archCond []string) string {
	if buildType != "" && buildType != ctx.BuildType {
		return fmt.Sprintf("build_type: %s, current: %s", buildType, ctx.BuildType)
	}
	if len(osCond) > 0 && !slicesContains(osCond, ctx.Platform.OS) {
		return fmt.Sprintf("os: %v, current: %s", osCond, ctx.Platform.OS)
	}
	if len(archCond) > 0 && !slicesContains(archCond, ctx.Architecture) {
		return fmt.Sprintf("arch: %v, current: %s", archCond, ctx.Architecture)
	}
	return ""
}

func slicesContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// CopyResources executes copy_resources.yaml (resources.py copy_resources_impl).
func CopyResources(ctx *buildctx.Context) error {
	var cfg copyConfig
	if err := config.LoadInto(ctx.CopyResourcesConfig(), &cfg); err != nil {
		return err
	}
	if len(cfg.CopyOperations) == 0 {
		logx.Info("⚠️  No copy_operations defined in configuration")
		return nil
	}

	for _, op := range cfg.CopyOperations {
		name := op.Name
		if name == "" {
			name = "Unnamed operation"
		}
		if reason := operationSkipReason(ctx, op.BuildType, op.OS, op.Arch); reason != "" {
			logx.Info(fmt.Sprintf("  ⏭️  Skipping %s (%s)", name, reason))
			continue
		}

		srcPath := filepath.Join(ctx.RootDir, filepath.FromSlash(op.Source))
		dstBase := filepath.Join(ctx.ChromiumSrc, filepath.FromSlash(op.Destination))
		opType := op.Type
		if opType == "" {
			opType = "directory"
		}

		logx.Info(fmt.Sprintf("  • %s", name))

		// Per-operation failures are logged, not fatal (resources.py
		// catches each operation's exception).
		switch opType {
		case "directory":
			info, err := os.Stat(srcPath)
			if err != nil || !info.IsDir() {
				logx.Warning(fmt.Sprintf("    Source directory not found: %s", op.Source))
				continue
			}
			if err := copyTree(srcPath, dstBase); err != nil {
				logx.Error(fmt.Sprintf("    Error: %v", err))
				continue
			}
			logx.Info(fmt.Sprintf("    ✓ Copied directory: %s → %s", op.Source, op.Destination))

		case "files":
			matches, err := filepath.Glob(srcPath)
			if err != nil || len(matches) == 0 {
				logx.Warning(fmt.Sprintf("    No files found matching: %s", op.Source))
				continue
			}
			if err := os.MkdirAll(dstBase, 0o755); err != nil {
				logx.Error(fmt.Sprintf("    Error: %v", err))
				continue
			}
			copied := 0
			for _, match := range matches {
				if info, err := os.Stat(match); err == nil && !info.IsDir() {
					if err := copyFile(match, filepath.Join(dstBase, filepath.Base(match))); err != nil {
						logx.Error(fmt.Sprintf("    Error: %v", err))
						continue
					}
					copied++
				}
			}
			logx.Info(fmt.Sprintf("    ✓ Copied %d files: %s → %s", len(matches), op.Source, op.Destination))
			_ = copied

		case "file":
			info, err := os.Stat(srcPath)
			if err != nil || info.IsDir() {
				logx.Warning(fmt.Sprintf("    Source file not found: %s", op.Source))
				continue
			}
			if err := copyFile(srcPath, dstBase); err != nil {
				logx.Error(fmt.Sprintf("    Error: %v", err))
				continue
			}
			logx.Info(fmt.Sprintf("    ✓ Copied file: %s → %s", op.Source, op.Destination))
		}
	}

	logx.Success("Resources copied")
	return nil
}

// === chromium_replace (resources/chromium_replace.py) ===

type ChromiumReplace struct{}

func NewChromiumReplace() *ChromiumReplace { return &ChromiumReplace{} }

func (ChromiumReplace) Name() string { return "chromium_replace" }
func (ChromiumReplace) Description() string {
	return "Replace Chromium source files with custom versions"
}

func (ChromiumReplace) Validate(ctx *buildctx.Context) error {
	if _, err := os.Stat(ctx.ChromiumSrc); err != nil {
		return fmt.Errorf("chromium source not found: %s", ctx.ChromiumSrc)
	}
	return nil
}

func (ChromiumReplace) Execute(ctx *buildctx.Context) error {
	logx.Info("\n🔄 Replacing chromium files...")
	return ReplaceChromiumFiles(ctx)
}

// ReplaceChromiumFiles ports replace_chromium_files_impl: copies files from
// <root>/chromium_files into chromium_src, honoring .debug/.release variant
// suffixes.
func ReplaceChromiumFiles(ctx *buildctx.Context) error {
	logx.Info(fmt.Sprintf("  Build type: %s", ctx.BuildType))
	replacementDir := ctx.ChromiumReplaceFilesDir()
	if _, err := os.Stat(replacementDir); err != nil {
		logx.Info(fmt.Sprintf("⚠️  No chromium_files directory found at: %s", replacementDir))
		return nil
	}

	replaced, skipped := 0, 0
	err := filepath.WalkDir(replacementDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(replacementDir, path)
		if err != nil {
			return err
		}

		var destRel string
		ext := filepath.Ext(path)
		if ext == ".debug" || ext == ".release" {
			if (ctx.BuildType == "debug" && ext != ".debug") || (ctx.BuildType == "release" && ext != ".release") {
				skipped++
				return nil
			}
			destRel = strings.TrimSuffix(rel, ext)
		} else {
			destRel = rel
			variant := path + "." + ctx.BuildType
			if _, err := os.Stat(variant); err == nil && (ctx.BuildType == "debug" || ctx.BuildType == "release") {
				logx.Info(fmt.Sprintf("    ⏭️  Skipping %s (using %s variant instead)", rel, ctx.BuildType))
				skipped++
				return nil
			}
		}

		dstFile := filepath.Join(ctx.ChromiumSrc, destRel)
		if _, err := os.Stat(dstFile); err != nil {
			logx.Error(fmt.Sprintf("    Destination file not found in chromium_src: %s", destRel))
			return fmt.Errorf("destination file not found in chromium_src: %s", destRel)
		}
		if err := copyFile(path, dstFile); err != nil {
			logx.Error(fmt.Sprintf("    Error replacing file %s: %v", rel, err))
			return err
		}
		logx.Info(fmt.Sprintf("    ✓ Replaced: %s → %s", rel, destRel))
		replaced++
		return nil
	})
	if err != nil {
		return err
	}
	logx.Success(fmt.Sprintf("Replaced %d files (skipped %d non-matching files)", replaced, skipped))
	return nil
}

// === string_replaces (resources/string_replaces.py) ===

// brandingReplacements mirror string_replaces.py exactly (order matters).
var brandingReplacements = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`The Chromium Authors. All rights reserved.`), "The BrowserOS Authors. All rights reserved."},
	{regexp.MustCompile(`Google LLC. All rights reserved.`), "The BrowserOS Authors. All rights reserved."},
	{regexp.MustCompile(`The Chromium Authors`), "BrowserOS Software Inc"},
	{regexp.MustCompile(`Google Chrome`), "BrowserOS"},
	{regexp.MustCompile(`Google( Play)?`), ""}, // placeholder; handled specially below
	{regexp.MustCompile(`Chromium`), "BrowserOS"},
	{regexp.MustCompile(`Chrome`), "BrowserOS"},
}

// googleNotPlay implements Python's `(Google)(?! Play)` (Go's regexp has no
// lookahead): replace "Google" only when not followed by " Play".
func googleNotPlay(content string) (string, int) {
	re := regexp.MustCompile(`Google( Play)?`)
	count := 0
	out := re.ReplaceAllStringFunc(content, func(m string) string {
		if strings.HasSuffix(m, " Play") {
			return m
		}
		count++
		return "BrowserOS"
	})
	return out, count
}

var stringReplaceTargets = []string{
	"chrome/app/chromium_strings.grd",
	"chrome/app/settings_chromium_strings.grdp",
}

type StringReplaces struct{}

func NewStringReplaces() *StringReplaces { return &StringReplaces{} }

func (StringReplaces) Name() string { return "string_replaces" }
func (StringReplaces) Description() string {
	return "Apply branding string replacements in Chromium"
}

func (StringReplaces) Validate(ctx *buildctx.Context) error {
	if _, err := os.Stat(ctx.ChromiumSrc); err != nil {
		return fmt.Errorf("chromium source not found: %s", ctx.ChromiumSrc)
	}
	return nil
}

func (StringReplaces) Execute(ctx *buildctx.Context) error {
	logx.Info("\n🔤 Applying string replacements...")
	return ApplyStringReplacements(ctx)
}

// ApplyStringReplacements ports apply_string_replacements_impl: every target
// file is attempted; failures are collected and reported at the end (Python
// continues past a failed file rather than failing fast).
func ApplyStringReplacements(ctx *buildctx.Context) error {
	var failed error
	for _, target := range stringReplaceTargets {
		fullPath := filepath.Join(ctx.ChromiumSrc, filepath.FromSlash(target))
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			logx.Warning(fmt.Sprintf("  ⚠️  File not found: %s", target))
			continue
		}
		logx.Info(fmt.Sprintf("  • Processing: %s", target))

		content := string(raw)
		original := content
		total := 0
		for _, r := range brandingReplacements {
			if r.replacement == "" { // the (Google)(?! Play) special case
				var n int
				content, n = googleNotPlay(content)
				if n > 0 {
					total += n
					logx.Info(fmt.Sprintf("    ✓ Replaced %d occurrences of '(Google)(?! Play)'", n))
				}
				continue
			}
			matches := r.pattern.FindAllStringIndex(content, -1)
			if len(matches) > 0 {
				content = r.pattern.ReplaceAllString(content, r.replacement)
				total += len(matches)
				logx.Info(fmt.Sprintf("    ✓ Replaced %d occurrences of '%s'", len(matches), r.pattern.String()))
			}
		}

		if content != original {
			if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
				logx.Error(fmt.Sprintf("    Error processing %s: %v", target, err))
				failed = fmt.Errorf("string replacements failed: %w", err)
				continue
			}
			logx.Success(fmt.Sprintf("    Updated with %d total replacements", total))
		} else {
			logx.Info("    No replacements needed")
		}
	}
	if failed != nil {
		logx.Error("String replacements failed")
		return failed
	}
	logx.Success("String replacements completed")
	return nil
}
