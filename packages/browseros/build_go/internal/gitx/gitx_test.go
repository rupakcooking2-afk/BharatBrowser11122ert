package gitx

import (
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
)

func TestHelpersBuildExpectedArgv(t *testing.T) {
	rec := &execx.RecordingRunner{
		Results: []execx.Result{
			{Stdout: "abc123\n"},
			{Stdout: "main\n"},
			{Stdout: " M file.cc\n?? new.cc\n"},
		},
	}

	sha, err := RevParse(rec, "/src", "HEAD")
	if err != nil || sha != "abc123" {
		t.Errorf("RevParse = (%q, %v)", sha, err)
	}
	branch, err := CurrentBranch(rec, "/src")
	if err != nil || branch != "main" {
		t.Errorf("CurrentBranch = (%q, %v)", branch, err)
	}
	lines, err := StatusPorcelain(rec, "/src", "chrome/")
	if err != nil || len(lines) != 2 {
		t.Errorf("StatusPorcelain = (%v, %v)", lines, err)
	}

	want := []string{
		"git rev-parse HEAD",
		"git branch --show-current",
		"git status --porcelain chrome/",
	}
	got := rec.Argv()
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, c := range rec.Cmds {
		if c.Dir != "/src" {
			t.Errorf("dir = %q, want /src", c.Dir)
		}
	}
}

func TestStatusPorcelainEmptyTreeReturnsNil(t *testing.T) {
	rec := &execx.RecordingRunner{Results: []execx.Result{{Stdout: "\n"}}}
	lines, err := StatusPorcelain(rec, "/src")
	if err != nil || lines != nil {
		t.Errorf("StatusPorcelain = (%v, %v), want (nil, nil)", lines, err)
	}
}
