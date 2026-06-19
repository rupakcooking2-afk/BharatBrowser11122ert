// Package gitx provides thin git helpers over execx.
package gitx

import (
	"fmt"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
)

// Run executes git with args in dir, capturing output.
func Run(r execx.Runner, dir string, args ...string) (execx.Result, error) {
	return r.Run(execx.Cmd{Args: append([]string{"git"}, args...), Dir: dir})
}

// RunChecked is Run with non-zero exit converted to an error.
func RunChecked(r execx.Runner, dir string, args ...string) (execx.Result, error) {
	return execx.Checked(r, execx.Cmd{Args: append([]string{"git"}, args...), Dir: dir})
}

// RevParse resolves a ref to a full sha.
func RevParse(r execx.Runner, dir, ref string) (string, error) {
	res, err := RunChecked(r, dir, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("could not resolve %q: %w", ref, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// CommitExists verifies a commit-ish resolves.
func CommitExists(r execx.Runner, dir, ref string) bool {
	res, err := Run(r, dir, "rev-parse", "--verify", ref+"^{commit}")
	return err == nil && res.Code == 0
}

// CurrentBranch returns the checked-out branch name (empty when detached).
func CurrentBranch(r execx.Runner, dir string) (string, error) {
	res, err := RunChecked(r, dir, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// StatusPorcelain returns `git status --porcelain` lines for the given paths.
func StatusPorcelain(r execx.Runner, dir string, pathspecs ...string) ([]string, error) {
	args := append([]string{"status", "--porcelain"}, pathspecs...)
	res, err := RunChecked(r, dir, args...)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(res.Stdout)
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// IsDirty reports whether the worktree has uncommitted changes.
func IsDirty(r execx.Runner, dir string) (bool, error) {
	lines, err := StatusPorcelain(r, dir)
	if err != nil {
		return false, err
	}
	return len(lines) > 0, nil
}
