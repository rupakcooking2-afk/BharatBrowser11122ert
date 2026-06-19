// Package feature ports build/modules/feature: the feature-to-files registry
// stored in build/features.yaml. Edits go through yaml.Node so existing
// comments and ordering survive (Python's safe_dump dropped comments; we keep
// the same data semantics with better fidelity).
package feature

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/buildctx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/execx"
	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"gopkg.in/yaml.v3"
)

// ValidPrefixes are the allowed description prefixes (validation.py).
var ValidPrefixes = []string{"feat:", "fix:", "build:", "chore:", "series:"}

var featureNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidateDescription checks the conventional-commit prefix (validation.py).
func ValidateDescription(description string) error {
	description = strings.TrimSpace(description)
	if description == "" {
		return fmt.Errorf("description cannot be empty")
	}
	for _, prefix := range ValidPrefixes {
		if strings.HasPrefix(description, prefix) {
			return nil
		}
	}
	return fmt.Errorf("description must start with one of: %s", strings.Join(ValidPrefixes, ", "))
}

// ValidateFeatureName checks lowercase kebab-case naming (validation.py).
func ValidateFeatureName(name string) error {
	if name == "" {
		return fmt.Errorf("feature name cannot be empty")
	}
	if strings.Contains(name, " ") {
		return fmt.Errorf("feature name cannot contain spaces (use hyphens instead)")
	}
	if strings.Contains(name, ":") {
		return fmt.Errorf("feature name cannot contain ':' (did you pass a description as the name?)")
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("feature name must be lowercase (got '%s', use '%s')", name, strings.ToLower(name))
	}
	if !featureNameRe.MatchString(name) {
		return fmt.Errorf("feature name must start with a letter/number and contain only lowercase letters, numbers, hyphens, and underscores")
	}
	return nil
}

// Feature is one entry of features.yaml.
type Feature struct {
	Name        string
	Description string
	Files       []string
}

// File wraps the parsed features.yaml document, keeping the raw node tree so
// saves preserve comments and order.
type File struct {
	path string
	doc  *yaml.Node
}

// LoadFile parses features.yaml (missing file → empty registry).
func LoadFile(path string) (*File, error) {
	f := &File{path: path}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	f.doc = &doc
	return f, nil
}

func (f *File) root() *yaml.Node {
	if f.doc == nil || len(f.doc.Content) == 0 {
		mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapping.Content = []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "version"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "1.0", Style: yaml.DoubleQuotedStyle},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "features"},
			{Kind: yaml.MappingNode, Tag: "!!map"},
		}
		f.doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{mapping}}
	}
	return f.doc.Content[0]
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func (f *File) featuresNode() *yaml.Node {
	root := f.root()
	features := mappingValue(root, "features")
	if features == nil {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "features"},
			&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
		features = root.Content[len(root.Content)-1]
	}
	return features
}

// Features returns all entries in file order.
func (f *File) Features() []Feature {
	node := f.featuresNode()
	var out []Feature
	for i := 0; i+1 < len(node.Content); i += 2 {
		out = append(out, nodeToFeature(node.Content[i].Value, node.Content[i+1]))
	}
	return out
}

// Get returns one feature by name.
func (f *File) Get(name string) (Feature, bool) {
	node := mappingValue(f.featuresNode(), name)
	if node == nil {
		return Feature{}, false
	}
	return nodeToFeature(name, node), true
}

func nodeToFeature(name string, node *yaml.Node) Feature {
	feature := Feature{Name: name}
	if desc := mappingValue(node, "description"); desc != nil {
		feature.Description = desc.Value
	}
	if files := mappingValue(node, "files"); files != nil && files.Kind == yaml.SequenceNode {
		for _, item := range files.Content {
			feature.Files = append(feature.Files, item.Value)
		}
	}
	return feature
}

// Upsert merges files into a feature (creating it if needed), sets the
// description, and keeps the file list sorted+deduped (feature.py
// add_or_update_feature semantics).
func (f *File) Upsert(name, description string, files []string) (added, alreadyPresent int) {
	featuresNode := f.featuresNode()
	node := mappingValue(featuresNode, name)
	if node == nil {
		node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		featuresNode.Content = append(featuresNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, node)
	}

	existing := map[string]bool{}
	if filesNode := mappingValue(node, "files"); filesNode != nil {
		for _, item := range filesNode.Content {
			existing[item.Value] = true
		}
	}
	merged := map[string]bool{}
	for path := range existing {
		merged[path] = true
	}
	for _, path := range files {
		if existing[path] {
			alreadyPresent++
		} else if !merged[path] {
			added++
		}
		merged[path] = true
	}
	var sortedFiles []string
	for path := range merged {
		sortedFiles = append(sortedFiles, path)
	}
	sort.Strings(sortedFiles)

	setMappingScalar(node, "description", description)
	filesSeq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, path := range sortedFiles {
		filesSeq.Content = append(filesSeq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path})
	}
	setMappingNode(node, "files", filesSeq)
	return added, alreadyPresent
}

func setMappingScalar(mapping *yaml.Node, key, value string) {
	setMappingNode(mapping, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func setMappingNode(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			// Preserve the existing value node's comments where sensible.
			value.HeadComment = mapping.Content[i+1].HeadComment
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// Save writes the document back.
func (f *File) Save() error {
	out, err := yaml.Marshal(f.doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f.path, out, 0o644)
}

// === high-level operations (feature.py) ===

// CommitChangedFiles lists files touched by a commit via diff-tree.
func CommitChangedFiles(ctx *buildctx.Context, commit string) []string {
	res, _ := ctx.Runner.Run(execx.Cmd{
		Args: []string{"git", "diff-tree", "--no-commit-id", "--name-status", "-r", commit},
		Dir:  ctx.ChromiumSrc,
	})
	if res.Code != 0 {
		return nil
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			files = append(files, parts[len(parts)-1])
		}
	}
	return files
}

// AddOrUpdate merges a commit's files into a feature
// (feature.py add_or_update_feature).
func AddOrUpdate(ctx *buildctx.Context, name, commit, description string) error {
	if err := ValidateFeatureName(name); err != nil {
		return err
	}
	if err := ValidateDescription(description); err != nil {
		return err
	}

	changed := CommitChangedFiles(ctx, commit)
	if len(changed) == 0 {
		return fmt.Errorf("no changed files found in commit %s", commit)
	}

	return AddFiles(ctx, name, description, changed)
}

// AddFiles merges an explicit file list into a feature (select.py
// add_files_to_feature).
func AddFiles(ctx *buildctx.Context, name, description string, files []string) error {
	registry, err := LoadFile(ctx.FeaturesYAMLPath())
	if err != nil {
		return err
	}
	_, existed := registry.Get(name)
	if existed {
		logx.Info(fmt.Sprintf("Updating existing feature '%s'", name))
	} else {
		logx.Info(fmt.Sprintf("Creating new feature '%s'", name))
	}
	added, already := registry.Upsert(name, description, files)
	if added > 0 {
		logx.Success(fmt.Sprintf("  Adding %d new file(s)", added))
	}
	if already > 0 {
		logx.Warning(fmt.Sprintf("  Skipping %d file(s) already in feature", already))
	}
	if err := registry.Save(); err != nil {
		return err
	}
	feature, _ := registry.Get(name)
	if existed {
		logx.Success(fmt.Sprintf("✓ Updated feature '%s' - now has %d files", name, len(feature.Files)))
	} else {
		logx.Success(fmt.Sprintf("✓ Created feature '%s' with %d files", name, len(feature.Files)))
	}
	return nil
}

// List prints all features (feature.py list_features).
func List(ctx *buildctx.Context) error {
	registry, err := LoadFile(ctx.FeaturesYAMLPath())
	if err != nil {
		return err
	}
	features := registry.Features()
	if len(features) == 0 {
		logx.Warning("No features defined")
		return nil
	}
	logx.Info(fmt.Sprintf("Features (%d):", len(features)))
	logx.Info(strings.Repeat("-", 60))
	for _, feature := range features {
		logx.Info(fmt.Sprintf("  %s: %d files - %s", feature.Name, len(feature.Files), feature.Description))
	}
	return nil
}

// Show prints one feature's details (feature.py show_feature).
func Show(ctx *buildctx.Context, name string) error {
	registry, err := LoadFile(ctx.FeaturesYAMLPath())
	if err != nil {
		return err
	}
	feature, ok := registry.Get(name)
	if !ok {
		logx.Error(fmt.Sprintf("Feature '%s' not found", name))
		logx.Info("Available features:")
		for _, f := range registry.Features() {
			logx.Info("  - " + f.Name)
		}
		return fmt.Errorf("feature '%s' not found", name)
	}
	logx.Info("Feature: " + feature.Name)
	logx.Info(strings.Repeat("-", 60))
	logx.Info("Description: " + feature.Description)
	logx.Info(fmt.Sprintf("Files (%d):", len(feature.Files)))
	for _, path := range feature.Files {
		logx.Info("  - " + path)
	}
	return nil
}
