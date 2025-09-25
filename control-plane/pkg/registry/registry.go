package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Registry represents the mapping between CLI commands and composite resources.
type Registry struct {
	Commands []CommandEntry `yaml:"commands"`
}

// CommandEntry describes a single CLI command mapping.
type CommandEntry struct {
	Name          string           `yaml:"name"`
	Group         string           `yaml:"group"`
	Version       string           `yaml:"version"`
	CompositeKind string           `yaml:"compositeKind"`
	ClaimKind     string           `yaml:"claimKind"`
	Status        string           `yaml:"status"`
	Compositions  []CompositionRef `yaml:"compositions"`
	Description   string           `yaml:"description"`
}

// CompositionRef references a composition file implementing a command.
type CompositionRef struct {
	Name      string `yaml:"name"`
	File      string `yaml:"file"`
	Readiness string `yaml:"readiness"`
}

var (
	errEmptyRegistry          = errors.New("registry has no commands")
	allowedStatuses           = map[string]struct{}{"pending": {}, "in-progress": {}, "complete": {}}
	allowedReadiness          = map[string]struct{}{"alpha": {}, "beta": {}, "ga": {}, "": {}}
	errCompositionMissingFile = errors.New("composition file missing")
)

// Load reads a registry YAML file from disk.
func Load(path string) (*Registry, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}

	var reg Registry
	if err := yaml.Unmarshal(content, &reg); err != nil {
		return nil, fmt.Errorf("unmarshal registry: %w", err)
	}
	return &reg, nil
}

// Validate ensures the registry is well-formed and referenced files exist.
func (r *Registry) Validate(baseDir string) error {
	if r == nil || len(r.Commands) == 0 {
		return errEmptyRegistry
	}

	seen := map[string]struct{}{}
	baseDir = filepath.Clean(baseDir)

	for _, cmd := range r.Commands {
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			return fmt.Errorf("command entry missing name")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate command entry: %s", name)
		}
		seen[name] = struct{}{}

		status := strings.ToLower(strings.TrimSpace(cmd.Status))
		if _, ok := allowedStatuses[status]; !ok {
			return fmt.Errorf("command %s has invalid status %q", name, cmd.Status)
		}

		if status != "pending" && len(cmd.Compositions) == 0 {
			return fmt.Errorf("command %s is %s but no compositions defined", name, status)
		}

		for _, comp := range cmd.Compositions {
			if strings.TrimSpace(comp.Name) == "" {
				return fmt.Errorf("command %s has composition without name", name)
			}

			readiness := strings.ToLower(strings.TrimSpace(comp.Readiness))
			if _, ok := allowedReadiness[readiness]; !ok {
				return fmt.Errorf("command %s composition %s has invalid readiness %q", name, comp.Name, comp.Readiness)
			}

			if strings.TrimSpace(comp.File) == "" {
				return fmt.Errorf("command %s composition %s missing file reference", name, comp.Name)
			}

			abs := filepath.Join(baseDir, comp.File)
			info, err := os.Stat(abs)
			if err != nil {
				return fmt.Errorf("%w: %s: %v", errCompositionMissingFile, abs, err)
			}
			if info.IsDir() {
				return fmt.Errorf("composition path %s is a directory", abs)
			}
		}
	}

	return nil
}

// Command returns the registry entry for the given command name.
func (r *Registry) Command(name string) (*CommandEntry, bool) {
	if r == nil {
		return nil, false
	}
	for i := range r.Commands {
		if r.Commands[i].Name == name {
			return &r.Commands[i], true
		}
	}
	return nil, false
}
