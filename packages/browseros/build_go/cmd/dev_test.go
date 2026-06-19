package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

func execDev(t *testing.T, args ...string) (string, error) {
	t.Helper()
	prevSrc, prevVerbose, prevQuiet := devChromiumSrc, devVerbose, devQuiet
	prevOut := logx.Out
	var logBuf bytes.Buffer
	logx.Out = &logBuf
	devChromiumSrc, devVerbose, devQuiet = "", false, false
	t.Cleanup(func() {
		devChromiumSrc, devVerbose, devQuiet = prevSrc, prevVerbose, prevQuiet
		logx.Out = prevOut
	})

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(append([]string{"dev"}, args...))
	err := rootCmd.Execute()
	rootCmd.SetArgs(nil)
	return logBuf.String() + out.String(), err
}

func TestDevCommandTreeMatchesPython(t *testing.T) {
	out, err := execDev(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"extract", "apply", "feature", "annotate"} {
		if !strings.Contains(out, want) {
			t.Errorf("dev help missing %q", want)
		}
	}

	out, _ = execDev(t, "extract", "--help")
	for _, want := range []string{"commit", "patch", "range"} {
		if !strings.Contains(out, want) {
			t.Errorf("extract help missing %q", want)
		}
	}

	out, _ = execDev(t, "apply", "--help")
	for _, want := range []string{"all", "feature", "patch", "force", "changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("apply help missing %q", want)
		}
	}

	out, _ = execDev(t, "feature", "--help")
	for _, want := range []string{"list", "show", "add-update", "classify"} {
		if !strings.Contains(out, want) {
			t.Errorf("feature help missing %q", want)
		}
	}
}

func TestDevRequiresChromiumSrc(t *testing.T) {
	_, err := execDev(t, "apply", "all", "--no-interactive")
	if err == nil || !strings.Contains(err.Error(), "--chromium-src is required") {
		t.Errorf("err = %v", err)
	}
}

func TestDevApplyChangedRequiresResetTo(t *testing.T) {
	_, err := execDev(t, "apply", "changed", "--commit", "abc123")
	if err == nil || !strings.Contains(err.Error(), "--reset-to is required") {
		t.Errorf("err = %v", err)
	}
}

func TestDevExtractCommitFlagSurface(t *testing.T) {
	out, _ := execDev(t, "extract", "commit", "--help")
	for _, want := range []string{"--base", "--force", "--include-binary", "--feature", "--interactive", "--no-interactive", "--output"} {
		if !strings.Contains(out, want) {
			t.Errorf("extract commit help missing %q:\n%s", want, out)
		}
	}
}

func TestDevApplyAllFlagSurface(t *testing.T) {
	out, _ := execDev(t, "apply", "all", "--help")
	for _, want := range []string{"--interactive", "--no-interactive", "--reset-to", "--annotate"} {
		if !strings.Contains(out, want) {
			t.Errorf("apply all help missing %q:\n%s", want, out)
		}
	}
}
