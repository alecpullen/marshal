package skills

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"marshal/internal/tools/registry"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// DefaultSkillRisk is the risk level assigned to a skill when its
// frontmatter does not specify one. It mirrors the registry's RiskReadOnly
// value so that skills that don't opt in to a higher risk are treated as
// safe-to-invoke reference material by the policy engine.
const DefaultSkillRisk = string(registry.RiskReadOnly)

// BundleFileName is the file that marks a directory as a skill bundle.
const BundleFileName = "SKILL.md"

// Parse parses a skill file's TOML frontmatter and body.
func Parse(raw string) (Skill, error) {
	return parseFrontmatter(raw)
}

type Skill struct {
	Name        string
	Description string
	Risk        string
	Body        string
}

// Index is the live skill registry. It is shared by every runner in a
// session (the main loop, async subagents, swarm roles, pipeline children),
// so its map is guarded: skill.install mutates it at runtime while other
// runners concurrently read it to build their system prompts. An
// unsynchronised map read+write is a fatal runtime error, not a panic.
type Index struct {
	mu     sync.RWMutex
	skills map[string]Skill
}

func NewIndex() *Index {
	return &Index{skills: make(map[string]Skill)}
}

func (idx *Index) Set(name string, skill Skill) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.skills[name] = skill
}

func (idx *Index) Load(name string) (Skill, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	skill, ok := idx.skills[name]
	return skill, ok
}

func (idx *Index) List() []Skill {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	names := make([]string, 0, len(idx.skills))
	for name := range idx.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	skills := make([]Skill, 0, len(names))
	for _, name := range names {
		skills = append(skills, idx.skills[name])
	}
	return skills
}

type frontmatter struct {
	Name        string `toml:"name"        yaml:"name"`
	Description string `toml:"description" yaml:"description"`
	Risk        string `toml:"risk"        yaml:"risk"`
}

func parseFrontmatter(raw string) (Skill, error) {
	// Support both TOML (+++) and YAML (---) frontmatter delimiters.
	// YAML is common in third-party skill suites (e.g. github.com/obra/superpowers).

	// Normalize CRLF to LF so delimiter detection works for both line
	// endings.
	raw = strings.ReplaceAll(raw, "\r\n", "\n")

	const (
		tomlDelimiter = "+++\n"
		yamlDelimiter = "---\n"
	)

	switch {
	case strings.HasPrefix(raw, tomlDelimiter):
		return parseTOMLFrontmatter(raw, tomlDelimiter)
	case strings.HasPrefix(raw, yamlDelimiter):
		return parseYAMLFrontmatter(raw, yamlDelimiter)
	default:
		return Skill{}, fmt.Errorf("skill file must start with +++ (TOML) or --- (YAML) delimiter")
	}
}

func parseTOMLFrontmatter(raw, delimiter string) (Skill, error) {
	end := strings.Index(raw[len(delimiter):], delimiter)
	if end == -1 {
		return Skill{}, fmt.Errorf("skill file missing closing %s delimiter", delimiter)
	}

	fmRaw := raw[len(delimiter) : len(delimiter)+end]
	body := strings.TrimLeft(raw[len(delimiter)+end+len(delimiter):], "\n")

	var fm frontmatter
	if err := toml.Unmarshal([]byte(fmRaw), &fm); err != nil {
		return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	return validateSkill(fm, body)
}

func parseYAMLFrontmatter(raw, delimiter string) (Skill, error) {
	end := strings.Index(raw[len(delimiter):], delimiter)
	if end == -1 {
		return Skill{}, fmt.Errorf("skill file missing closing %s delimiter", delimiter)
	}

	fmRaw := raw[len(delimiter) : len(delimiter)+end]
	body := strings.TrimLeft(raw[len(delimiter)+end+len(delimiter):], "\n")

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(fmRaw), &fm); err != nil {
		return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	return validateSkill(fm, body)
}

func validateSkill(fm frontmatter, body string) (Skill, error) {
	if fm.Name == "" {
		return Skill{}, fmt.Errorf("skill frontmatter missing required field: name")
	}
	if fm.Description == "" {
		return Skill{}, fmt.Errorf("skill frontmatter missing required field: description")
	}
	if fm.Risk == "" {
		fm.Risk = DefaultSkillRisk
	}

	return Skill{
		Name:        fm.Name,
		Description: fm.Description,
		Risk:        fm.Risk,
		Body:        body,
	}, nil
}
