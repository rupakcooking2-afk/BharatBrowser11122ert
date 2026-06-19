// Package annotate ports build/modules/annotate: create git commits in the
// chromium tree organized by features from features.yaml.
package annotate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/feature"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

func git(ctx *buildctx.Context, args ...string) execx.Result {
	res, _ := ctx.Runner.Run(execx.Cmd{Args: append([]string{"git"}, args...), Dir: ctx.ChromiumSrc})
	return res
}

// indexLockPath resolves the git index lock (annotate.py _git_index_lock_path).
func indexLockPath(ctx *buildctx.Context) string {
	res := git(ctx, "rev-parse", "--git-path", "index.lock")
	if res.Code != 0 {
		return filepath.Join(ctx.ChromiumSrc, ".git", "index.lock")
	}
	lockPath := strings.TrimSpace(res.Stdout)
	if !filepath.IsAbs(lockPath) {
		return filepath.Join(ctx.ChromiumSrc, lockPath)
	}
	return lockPath
}

func isIndexLockError(stderr string) bool {
	normalized := strings.ToLower(stderr)
	return strings.Contains(normalized, "index.lock") && strings.Contains(normalized, "file exists")
}

// gitWithLockRetry retries once after removing a stale index.lock
// (annotate.py _run_git_with_lock_retry).
func gitWithLockRetry(ctx *buildctx.Context, args ...string) execx.Result {
	res := git(ctx, args...)
	if res.Code == 0 || !isIndexLockError(res.Stderr) {
		return res
	}
	lockPath := indexLockPath(ctx)
	if _, err := os.Stat(lockPath); err == nil {
		if err := os.Remove(lockPath); err == nil {
			logx.Warning("   Git lock existed; removed stale index.lock and retrying")
		} else {
			logx.Warning(fmt.Sprintf("   Failed to remove index.lock at %s: %v", lockPath, err))
		}
	}
	return git(ctx, args...)
}

// ModifiedFiles filters a feature's file list to those with uncommitted
// changes (annotate.py get_modified_files).
func ModifiedFiles(ctx *buildctx.Context, files []string) []string {
	var modified []string
	for _, filePath := range files {
		fullPath := filepath.Join(ctx.ChromiumSrc, filepath.FromSlash(filePath))
		if _, err := os.Stat(fullPath); err != nil {
			continue
		}
		res := git(ctx, "status", "--porcelain", filePath)
		if res.Code == 0 && strings.TrimSpace(res.Stdout) != "" {
			modified = append(modified, filePath)
		}
	}
	return modified
}

func addAndCommit(ctx *buildctx.Context, files []string, message string) bool {
	for _, filePath := range files {
		res := gitWithLockRetry(ctx, "add", filePath)
		if res.Code != 0 {
			logx.Error(fmt.Sprintf("Failed to add file %s: %s", filePath, strings.TrimSpace(res.Stderr)))
			return false
		}
	}
	res := gitWithLockRetry(ctx, "commit", "-m", message)
	if res.Code != 0 {
		combined := res.Stderr + res.Stdout
		if strings.Contains(combined, "nothing to commit") || strings.Contains(combined, "nothing added to commit") {
			return false
		}
		logx.Error(fmt.Sprintf("Failed to create commit with message `%s`: %s", message, strings.TrimSpace(res.Stderr)))
		return false
	}
	return true
}

// Features creates one commit per feature with modified files
// (annotate.py annotate_features). featureFilter limits to one feature.
func Features(ctx *buildctx.Context, featureFilter string) (int, int, error) {
	registry, err := feature.LoadFile(ctx.FeaturesYAMLPath())
	if err != nil {
		return 0, 0, err
	}
	features := registry.Features()
	if len(features) == 0 {
		return 0, 0, fmt.Errorf("no features found in features.yaml")
	}
	if featureFilter != "" {
		found := false
		for _, f := range features {
			if f.Name == featureFilter {
				features = []feature.Feature{f}
				found = true
				break
			}
		}
		if !found {
			return 0, 0, fmt.Errorf("feature '%s' not found in features.yaml", featureFilter)
		}
	}

	logx.Info(fmt.Sprintf("📋 Processing %d feature(s)", len(features)))
	logx.Info(strings.Repeat("=", 60))

	commits, skipped := 0, 0
	for _, f := range features {
		description := f.Description
		if description == "" {
			description = f.Name
		}
		logx.Info("\n🔧 " + f.Name)
		logx.Info("   " + description)

		if len(f.Files) == 0 {
			logx.Warning("   No files specified, skipping")
			skipped++
			continue
		}
		modified := ModifiedFiles(ctx, f.Files)
		if len(modified) == 0 {
			logx.Warning(fmt.Sprintf("   No modified files (%d files checked)", len(f.Files)))
			skipped++
			continue
		}
		logx.Info(fmt.Sprintf("   Found %d modified file(s)", len(modified)))

		if addAndCommit(ctx, modified, description) {
			logx.Success(fmt.Sprintf("   ✓ Committed %d file(s)", len(modified)))
			commits++
		} else {
			logx.Warning("   No changes staged, skipping commit")
			skipped++
		}
	}
	return commits, skipped, nil
}
