package skills

import (
	"fmt"
	"sort"
	"strings"

	"marshal/internal/tools/registry"

	"github.com/pelletier/go-toml/v2"
)

// DefaultSkillRisk is the risk level assigned to a skill when its
// frontmatter does not specify one. It mirrors the registry's RiskReadOnly
// value so that skills that don't opt in to a higher risk are treated as
// safe-to-invoke reference material by the policy engine.
const DefaultSkillRisk = string(registry.RiskReadOnly)

type Skill struct {
	Name        string
	Description string
	Risk        string
	Body        string
}

type Index struct {
	skills map[string]Skill
}

func NewIndex() *Index {
	return &Index{skills: make(map[string]Skill)}
}

func (idx *Index) Set(name string, skill Skill) {
	idx.skills[name] = skill
}

func (idx *Index) Load(name string) (Skill, bool) {
	skill, ok := idx.skills[name]
	return skill, ok
}

func (idx *Index) List() []Skill {
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
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Risk        string `toml:"risk"`
}

func parseFrontmatter(raw string) (Skill, error) {
	const delimiter = "+++\n"

	idx := strings.Index(raw, delimiter)
	if idx != 0 {
		return Skill{}, fmt.Errorf("skill file must start with +++ delimiter")
	}

	end := strings.Index(raw[len(delimiter):], delimiter)
	if end == -1 {
		return Skill{}, fmt.Errorf("skill file missing closing +++ delimiter")
	}

	fmRaw := raw[len(delimiter) : len(delimiter)+end]
	body := strings.TrimLeft(raw[len(delimiter)+end+len(delimiter):], "\n")

	var fm frontmatter
	if err := toml.Unmarshal([]byte(fmRaw), &fm); err != nil {
		return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
	}

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
