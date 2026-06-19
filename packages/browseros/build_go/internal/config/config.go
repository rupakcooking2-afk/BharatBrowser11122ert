// Package config loads the build-config YAML files (build/config/*.yaml),
// mirroring build/common/config.py — including the custom `!env VAR` tag that
// substitutes environment variables (empty string when unset).
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/logx"
	"gopkg.in/yaml.v3"
)

// StringOrList accepts a YAML scalar or sequence of scalars.
type StringOrList []string

func (s *StringOrList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var single string
		if err := node.Decode(&single); err != nil {
			return err
		}
		*s = StringOrList{single}
		return nil
	case yaml.SequenceNode:
		var many []string
		if err := node.Decode(&many); err != nil {
			return err
		}
		*s = StringOrList(many)
		return nil
	}
	return fmt.Errorf("expected string or list, got yaml kind %d", node.Kind)
}

// BuildSection is the `build:` mapping of a config file.
type BuildSection struct {
	Type        string `yaml:"type"`
	ChromiumSrc string `yaml:"chromium_src"`
	// architecture may be a scalar or a list; `arch` is an accepted alias
	// (common/resolver.py reads both). nil means "not specified".
	Architecture *StringOrList `yaml:"architecture"`
	Arch         *StringOrList `yaml:"arch"`
}

// Architectures returns the declared architectures, or nil when absent.
func (b BuildSection) Architectures() []string {
	if b.Architecture != nil {
		return []string(*b.Architecture)
	}
	if b.Arch != nil {
		return []string(*b.Arch)
	}
	return nil
}

// BuildFile is the schema of build/config/{release,debug,sign,package}*.yaml.
type BuildFile struct {
	Build   BuildSection `yaml:"build"`
	GNFlags struct {
		File string `yaml:"file"`
	} `yaml:"gn_flags"`
	Modules       []string `yaml:"modules"`
	RequiredEnvs  []string `yaml:"required_envs"`
	Notifications struct {
		Slack bool `yaml:"slack"`
	} `yaml:"notifications"`
}

// Load reads a build config YAML with !env substitution.
func Load(path string) (*BuildFile, error) {
	var cfg BuildFile
	if err := LoadInto(path, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadInto reads any YAML file with !env substitution into out.
func LoadInto(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("config file not found: %s", path)
		}
		return err
	}
	logx.Info(fmt.Sprintf("Loading config from: %s", path))
	return UnmarshalWithEnv(data, out)
}

// UnmarshalWithEnv decodes YAML, resolving `!env VAR` scalars from the
// environment first (empty string + warning when unset).
func UnmarshalWithEnv(data []byte, out any) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	if root.Kind == 0 {
		return nil // empty document
	}
	resolveEnvTags(&root)
	return root.Decode(out)
}

func resolveEnvTags(node *yaml.Node) {
	if node.Kind == yaml.ScalarNode && node.Tag == "!env" {
		name := node.Value
		value, ok := os.LookupEnv(name)
		if !ok {
			logx.Warning(fmt.Sprintf("Environment variable not set: %s (using empty string)", name))
		}
		node.Value = value
		node.Tag = "!!str"
		node.Style = 0
		return
	}
	for _, child := range node.Content {
		resolveEnvTags(child)
	}
}

// ValidateRequiredEnvs errors with ALL missing variables listed, mirroring
// common/config.py:validate_required_envs.
func ValidateRequiredEnvs(required []string) error {
	var missing []string
	for _, name := range required {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing required environment variables:\n  - %s\n\nSet these variables and try again",
		strings.Join(missing, "\n  - "))
}
