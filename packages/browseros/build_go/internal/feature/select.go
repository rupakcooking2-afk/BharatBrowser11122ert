package feature

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
)

// Prompter collects interactive answers (select.py prompts); tests inject
// canned responses.
type Prompter interface {
	Input(prompt, def string) string
}

// StdinPrompter reads answers from stdin.
type StdinPrompter struct{}

func (StdinPrompter) Input(prompt, def string) string {
	if def != "" {
		fmt.Fprintf(logx.Out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(logx.Out, "%s: ", prompt)
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// AutoPrompter replays scripted answers.
type AutoPrompter struct {
	Answers []string
	idx     int
}

func (p *AutoPrompter) Input(_, def string) string {
	if p.idx >= len(p.Answers) {
		return def
	}
	answer := p.Answers[p.idx]
	p.idx++
	if answer == "" {
		return def
	}
	return answer
}

// PromptSelection asks the user to pick an existing feature or create a new
// one (select.py prompt_feature_selection). Returns name, description;
// ok=false when cancelled.
func PromptSelection(ctx *buildctx.Context, commitHash, commitMessage string, prompter Prompter) (string, string, bool) {
	registry, err := LoadFile(ctx.FeaturesYAMLPath())
	if err != nil {
		logx.Error(err.Error())
		return "", "", false
	}
	features := registry.Features()

	if commitHash != "" || commitMessage != "" {
		logx.Info("")
		logx.Info(strings.Repeat("=", 60))
		if commitHash != "" {
			logx.Info(fmt.Sprintf("Commit: %.12s", commitHash))
		}
		if commitMessage != "" {
			logx.Info("Message: " + commitMessage)
		}
		logx.Info(strings.Repeat("=", 60))
	}

	logx.Info("")
	logx.Info("Select a feature to add files to:")
	logx.Info(strings.Repeat("-", 40))
	for i, feature := range features {
		logx.Info(fmt.Sprintf("  %d) %s (%d files)", i+1, feature.Description, len(feature.Files)))
	}
	newOption := len(features) + 1
	logx.Info(fmt.Sprintf("  %d) [Add new feature]", newOption))
	logx.Info("")

	answer := prompter.Input(fmt.Sprintf("Selection (1-%d, or empty to skip)", newOption), "")
	if answer == "" {
		return "", "", false
	}
	choice, err := strconv.Atoi(answer)
	if err != nil || choice < 1 || choice > newOption {
		logx.Warning("Invalid selection")
		return "", "", false
	}
	if choice == newOption {
		return promptNewFeature(commitMessage, prompter)
	}
	selected := features[choice-1]
	return selected.Name, selected.Description, true
}

// promptNewFeature collects a new feature name + description
// (select.py prompt_new_feature).
func promptNewFeature(defaultDescription string, prompter Prompter) (string, string, bool) {
	name := prompter.Input("New feature name (lowercase-kebab-case)", "")
	if name == "" {
		return "", "", false
	}
	if err := ValidateFeatureName(name); err != nil {
		logx.Error(err.Error())
		return "", "", false
	}
	description := prompter.Input(
		fmt.Sprintf("Description (must start with %s)", strings.Join(ValidPrefixes, "/")),
		defaultDescription)
	if err := ValidateDescription(description); err != nil {
		logx.Error(err.Error())
		return "", "", false
	}
	return name, description, true
}

// AllPatchFiles lists chromium-relative paths of every patch file
// (select.py get_all_patch_files).
func AllPatchFiles(ctx *buildctx.Context) []string {
	patchesDir := ctx.PatchesDir()
	var files []string
	filepath.WalkDir(patchesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			return nil
		}
		rel, err := filepath.Rel(patchesDir, path)
		if err != nil {
			return nil
		}
		// Strip marker suffixes so paths match features.yaml entries.
		relSlash := filepath.ToSlash(rel)
		for _, suffix := range []string{".deleted", ".binary", ".rename"} {
			relSlash = strings.TrimSuffix(relSlash, suffix)
		}
		files = append(files, relSlash)
		return nil
	})
	sort.Strings(files)
	return files
}

// UnclassifiedFiles returns patch files not referenced by any feature
// (select.py get_unclassified_files). Directory entries in features.yaml
// (trailing "/") classify everything below them.
func UnclassifiedFiles(ctx *buildctx.Context) ([]string, error) {
	registry, err := LoadFile(ctx.FeaturesYAMLPath())
	if err != nil {
		return nil, err
	}
	var prefixes []string
	classified := map[string]bool{}
	for _, feature := range registry.Features() {
		for _, path := range feature.Files {
			if strings.HasSuffix(path, "/") {
				prefixes = append(prefixes, path)
			} else {
				classified[path] = true
			}
		}
	}

	var unclassified []string
	for _, path := range AllPatchFiles(ctx) {
		if classified[path] {
			continue
		}
		covered := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				covered = true
				break
			}
		}
		if !covered {
			unclassified = append(unclassified, path)
		}
	}
	return unclassified, nil
}

// Classify walks unclassified files prompting for a feature per file
// (select.py classify_files). Returns (classified, skipped).
func Classify(ctx *buildctx.Context, prompter Prompter) (int, int, error) {
	unclassified, err := UnclassifiedFiles(ctx)
	if err != nil {
		return 0, 0, err
	}
	if len(unclassified) == 0 {
		logx.Success("All patch files are already classified!")
		return 0, 0, nil
	}
	logx.Info(fmt.Sprintf("Found %d unclassified patch file(s)", len(unclassified)))
	logx.Info("")

	classified, skipped := 0, 0
	for i, path := range unclassified {
		logx.Info(fmt.Sprintf("\n[%d/%d] %s", i+1, len(unclassified), path))
		name, description, ok := PromptSelection(ctx, "", "", prompter)
		if !ok {
			logx.Warning("  Skipped")
			skipped++
			continue
		}
		if err := AddFiles(ctx, name, description, []string{path}); err != nil {
			logx.Error(err.Error())
			skipped++
			continue
		}
		classified++
	}
	logx.Info("")
	logx.Success(fmt.Sprintf("Classified %d file(s), skipped %d", classified, skipped))
	return classified, skipped, nil
}
