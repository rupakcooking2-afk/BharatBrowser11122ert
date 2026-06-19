package patchwork

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// ParseSeries parses a GNU Quilt series file (series_patches.py
// parse_series): one path per line, # comments, inline " #" stripped.
func ParseSeries(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var patches []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, " #"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line != "" {
			patches = append(patches, line)
		}
	}
	return patches, nil
}

// SeriesFiles returns the applicable series files in order: common `series`
// then `series.{platform}` (series_patches.py get_series_files).
func SeriesFiles(seriesDir, platformOS string) []string {
	var files []string
	common := filepath.Join(seriesDir, "series")
	if _, err := os.Stat(common); err == nil {
		files = append(files, common)
	}
	platformFile := filepath.Join(seriesDir, "series."+platformOS)
	if _, err := os.Stat(platformFile); err == nil {
		files = append(files, platformFile)
	}
	return files
}

func applySeriesPatch(ctx *buildctx.Context, patchPath string) (bool, string) {
	res := git(ctx, "apply", "--ignore-whitespace", "--whitespace=nowarn", "-p1", patchPath)
	if res.Code == 0 {
		return true, ""
	}
	// NB: series fallback puts --3way first (series_patches.py).
	res = git(ctx, "apply", "--3way", "--ignore-whitespace", "--whitespace=nowarn", "-p1", patchPath)
	if res.Code == 0 {
		return true, ""
	}
	msg := res.Stderr
	if msg == "" {
		msg = res.Stdout
	}
	return false, msg
}

// ApplySeriesPatches applies all patches listed in the series files
// (series_patches.py apply_series_patches_impl).
func ApplySeriesPatches(ctx *buildctx.Context, dryRun bool) (applied []string, failed []string) {
	seriesDir := ctx.SeriesPatchesDir()
	seriesFiles := SeriesFiles(seriesDir, ctx.Platform.OS)
	if len(seriesFiles) == 0 {
		logx.Info("  No series files found")
		return nil, nil
	}

	type entry struct{ rel, seriesFile string }
	var all []entry
	for _, seriesFile := range seriesFiles {
		patches, err := ParseSeries(seriesFile)
		if err != nil {
			logx.Error(fmt.Sprintf("  Failed to read series file %s: %v", seriesFile, err))
			continue
		}
		for _, rel := range patches {
			all = append(all, entry{rel, seriesFile})
		}
	}
	if len(all) == 0 {
		logx.Info("  No patches listed in series files")
		return nil, nil
	}
	logx.Info(fmt.Sprintf("  Found %d patches for platform '%s' across %d series file(s)",
		len(all), ctx.Platform.OS, len(seriesFiles)))

	for i, e := range all {
		patchPath := filepath.Join(seriesDir, filepath.FromSlash(e.rel))
		if _, err := os.Stat(patchPath); err != nil {
			logx.Error(fmt.Sprintf("  [%d/%d] ✗ Patch file not found: %s", i+1, len(all), e.rel))
			failed = append(failed, patchPath)
			continue
		}
		if dryRun {
			res := git(ctx, "apply", "--check", "--ignore-whitespace", "-p1", patchPath)
			if res.Code == 0 {
				logx.Info(fmt.Sprintf("  [%d/%d] ✓ Would apply: %s", i+1, len(all), e.rel))
				applied = append(applied, patchPath)
			} else {
				logx.Error(fmt.Sprintf("  [%d/%d] ✗ Would fail: %s", i+1, len(all), e.rel))
				failed = append(failed, patchPath)
			}
			continue
		}
		ok, errMsg := applySeriesPatch(ctx, patchPath)
		if ok {
			logx.Info(fmt.Sprintf("  [%d/%d] ✓ Applied: %s", i+1, len(all), e.rel))
			applied = append(applied, patchPath)
		} else {
			logx.Error(fmt.Sprintf("  [%d/%d] ✗ Failed: %s", i+1, len(all), e.rel))
			if errMsg != "" {
				logx.Error("      " + strings.TrimSpace(errMsg))
			}
			failed = append(failed, patchPath)
		}
	}
	return applied, failed
}
