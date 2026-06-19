package patch

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

const IgnoreFileName = ".browseros-patchignore"

// IgnoreSet filters untracked checkout files (local junk) out of patch sets.
// Tracked modifications are never filtered — a patch to a real Chromium file
// always wins over an ignore pattern.
type IgnoreSet struct {
	patterns []string
}

// DefaultIgnorePatterns covers tool state and scratch files that must never
// be extracted into the patch repo.
func DefaultIgnorePatterns() []string {
	return []string{
		".llm/",
		".browseros-patch/",
		"*.log",
		"*.rej",
		"*.orig",
		".DS_Store",
	}
}

// LoadIgnoreSet merges the built-in defaults, the optional repo-root
// .browseros-patchignore file, and per-run extra patterns.
func LoadIgnoreSet(repoRoot string, extra []string) (*IgnoreSet, error) {
	patterns := DefaultIgnorePatterns()
	data, err := os.ReadFile(filepath.Join(repoRoot, IgnoreFileName))
	switch {
	case err == nil:
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			patterns = append(patterns, line)
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	patterns = append(patterns, extra...)
	return &IgnoreSet{patterns: patterns}, nil
}

func (s *IgnoreSet) Match(rel string) bool {
	if s == nil {
		return false
	}
	candidate := NormalizeChromiumPath(rel)
	for _, pattern := range s.patterns {
		if matchIgnorePattern(pattern, candidate) {
			return true
		}
	}
	return false
}

// matchIgnorePattern implements a documented gitignore subset:
// trailing "/" matches the directory at any depth, a pattern containing "/"
// globs against the full relative path, anything else globs the basename.
func matchIgnorePattern(pattern string, rel string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if dir, isDir := strings.CutSuffix(pattern, "/"); isDir {
		return rel == dir ||
			strings.HasPrefix(rel, dir+"/") ||
			strings.Contains(rel, "/"+dir+"/")
	}
	if strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, rel)
		return err == nil && ok
	}
	ok, err := path.Match(pattern, path.Base(rel))
	return err == nil && ok
}
