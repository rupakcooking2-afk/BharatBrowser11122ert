package feature

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/platform"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureCtx(t *testing.T, runner execx.Runner) *buildctx.Context {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pyproject.toml"), "name = \"browseros\"\n")
	plat := platform.Platform{OS: "macos", Arch: "arm64"}
	ctx, err := buildctx.New(buildctx.Options{
		ChromiumSrc: t.TempDir(), Platform: &plat, RootDir: root, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestValidateFeatureName(t *testing.T) {
	for _, valid := range []string{"llm-chat", "agent_v2", "core9"} {
		if err := ValidateFeatureName(valid); err != nil {
			t.Errorf("%q should be valid: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "Has Space", "feat: oops", "UPPER", "-leading"} {
		if err := ValidateFeatureName(invalid); err == nil {
			t.Errorf("%q should be invalid", invalid)
		}
	}
}

func TestValidateDescriptionRequiresPrefix(t *testing.T) {
	for _, valid := range []string{"feat: chat", "fix: crash", "build: gn", "chore: tidy", "series: base"} {
		if err := ValidateDescription(valid); err != nil {
			t.Errorf("%q should be valid: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "chat feature", "feature: x"} {
		if err := ValidateDescription(invalid); err == nil {
			t.Errorf("%q should be invalid", invalid)
		}
	}
}

const sampleFeaturesYAML = `version: "1.0"
features:
  # core comes first
  browseros-core:
    description: "chore: browseros core infrastructure"
    files:
      - chrome/browser/browseros/BUILD.gn
      - chrome/browser/browseros/core/
  llm-chat:
    description: "feat: llm chat"
    files:
      - chrome/browser/ui/chat.cc
`

func TestUpsertMergesSortsAndPreservesOrderAndComments(t *testing.T) {
	ctx := fixtureCtx(t, &execx.RecordingRunner{})
	writeFile(t, ctx.FeaturesYAMLPath(), sampleFeaturesYAML)

	registry, err := LoadFile(ctx.FeaturesYAMLPath())
	if err != nil {
		t.Fatal(err)
	}
	added, already := registry.Upsert("llm-chat", "feat: llm chat v2",
		[]string{"chrome/browser/ui/chat.cc", "chrome/app/new_file.cc"})
	if added != 1 || already != 1 {
		t.Errorf("Upsert = (%d added, %d already), want (1, 1)", added, already)
	}
	if err := registry.Save(); err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(ctx.FeaturesYAMLPath())
	text := string(content)
	if !strings.Contains(text, "# core comes first") {
		t.Error("comments should survive a save")
	}
	if strings.Index(text, "browseros-core") > strings.Index(text, "llm-chat") {
		t.Error("feature order should be preserved")
	}
	if !strings.Contains(text, "feat: llm chat v2") {
		t.Error("description should be updated")
	}
	// Files sorted: chrome/app/... before chrome/browser/...
	reloaded, _ := LoadFile(ctx.FeaturesYAMLPath())
	feature, _ := reloaded.Get("llm-chat")
	if len(feature.Files) != 2 || feature.Files[0] != "chrome/app/new_file.cc" {
		t.Errorf("files = %v, want sorted with new file first", feature.Files)
	}
}

func TestUpsertCreatesNewFeature(t *testing.T) {
	ctx := fixtureCtx(t, &execx.RecordingRunner{})
	registry, err := LoadFile(ctx.FeaturesYAMLPath()) // missing file
	if err != nil {
		t.Fatal(err)
	}
	registry.Upsert("fresh", "feat: fresh", []string{"a.cc", "b.cc"})
	if err := registry.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := LoadFile(ctx.FeaturesYAMLPath())
	feature, ok := reloaded.Get("fresh")
	if !ok || len(feature.Files) != 2 {
		t.Errorf("fresh feature = (%+v, %v)", feature, ok)
	}
}

func TestAddOrUpdateValidatesAndUsesDiffTree(t *testing.T) {
	rec := &execx.RecordingRunner{
		Results: []execx.Result{{Stdout: "M\tchrome/a.cc\nA\tchrome/b.cc\n"}},
	}
	ctx := fixtureCtx(t, rec)

	if err := AddOrUpdate(ctx, "Bad Name", "HEAD", "feat: x"); err == nil {
		t.Error("invalid name should fail")
	}
	if err := AddOrUpdate(ctx, "ok-name", "HEAD", "no prefix"); err == nil {
		t.Error("invalid description should fail")
	}

	if err := AddOrUpdate(ctx, "ok-name", "abc123", "feat: x"); err != nil {
		t.Fatal(err)
	}
	if got := rec.Argv()[0]; got != "git diff-tree --no-commit-id --name-status -r abc123" {
		t.Errorf("diff-tree argv = %q", got)
	}
	registry, _ := LoadFile(ctx.FeaturesYAMLPath())
	feature, _ := registry.Get("ok-name")
	if len(feature.Files) != 2 {
		t.Errorf("files = %v", feature.Files)
	}
}

func TestUnclassifiedFilesHonorsDirectoryPrefixes(t *testing.T) {
	ctx := fixtureCtx(t, &execx.RecordingRunner{})
	writeFile(t, ctx.FeaturesYAMLPath(), sampleFeaturesYAML)

	// Patch files: one classified by exact path, one by dir prefix, one not.
	writeFile(t, filepath.Join(ctx.PatchesDir(), "chrome", "browser", "ui", "chat.cc"), "p")
	writeFile(t, filepath.Join(ctx.PatchesDir(), "chrome", "browser", "browseros", "core", "x.cc"), "p")
	writeFile(t, filepath.Join(ctx.PatchesDir(), "chrome", "renderer", "loose.cc"), "p")

	unclassified, err := UnclassifiedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unclassified) != 1 || unclassified[0] != "chrome/renderer/loose.cc" {
		t.Errorf("unclassified = %v", unclassified)
	}
}

func TestPromptSelectionPicksExistingAndNew(t *testing.T) {
	ctx := fixtureCtx(t, &execx.RecordingRunner{})
	writeFile(t, ctx.FeaturesYAMLPath(), sampleFeaturesYAML)

	// Pick feature #2 (llm-chat).
	name, desc, ok := PromptSelection(ctx, "", "", &AutoPrompter{Answers: []string{"2"}})
	if !ok || name != "llm-chat" || desc != "feat: llm chat" {
		t.Errorf("selection = (%q, %q, %v)", name, desc, ok)
	}

	// Pick "new feature" (#3) and define it.
	name, desc, ok = PromptSelection(ctx, "", "", &AutoPrompter{Answers: []string{"3", "new-thing", "feat: new thing"}})
	if !ok || name != "new-thing" || desc != "feat: new thing" {
		t.Errorf("new feature = (%q, %q, %v)", name, desc, ok)
	}

	// Empty answer skips.
	if _, _, ok := PromptSelection(ctx, "", "", &AutoPrompter{Answers: []string{""}}); ok {
		t.Error("empty answer should cancel")
	}
}
