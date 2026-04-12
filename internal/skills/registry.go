package skills

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Built-in skill files embedded at build time.
var (
	//go:embed builtin/schema-migration.toml
	schemaMigrationData []byte
	//go:embed builtin/security-audit.toml
	securityAuditData []byte
	//go:embed builtin/test-generation.toml
	testGenerationData []byte
)

// Registry holds all loaded skills indexed by trigger.
type Registry struct {
	all       []*Skill
	byTrigger map[string]*Skill
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{byTrigger: make(map[string]*Skill)}
}

// Load reads all *.toml files from dir and returns a populated Registry.
// Missing directories are silently ignored. Malformed files return an error.
func Load(dir string) (*Registry, error) {
	r := New()

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading skills dir %s: %w", dir, err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		var s Skill
		if _, err := toml.DecodeFile(path, &s); err != nil {
			return nil, fmt.Errorf("parsing skill %s: %w", path, err)
		}
		if err := r.Register(&s); err != nil {
			return nil, fmt.Errorf("registering skill from %s: %w", path, err)
		}
	}

	return r, nil
}

// Register adds a skill to the registry.
// Returns an error if the trigger is already registered or is invalid.
func (r *Registry) Register(s *Skill) error {
	if s.Trigger == "" {
		return fmt.Errorf("skill %q has no trigger", s.Name)
	}
	if !strings.HasPrefix(s.Trigger, "/") {
		return fmt.Errorf("skill %q trigger %q must start with '/'", s.Name, s.Trigger)
	}
	if _, exists := r.byTrigger[s.Trigger]; exists {
		return fmt.Errorf("duplicate trigger %q", s.Trigger)
	}
	r.all = append(r.all, s)
	r.byTrigger[s.Trigger] = s
	return nil
}

// Find returns the skill for the given trigger, or (nil, false) if not found.
func (r *Registry) Find(trigger string) (*Skill, bool) {
	if r == nil {
		return nil, false
	}
	s, ok := r.byTrigger[trigger]
	return s, ok
}

// All returns all registered skills in registration order.
func (r *Registry) All() []*Skill {
	if r == nil {
		return nil
	}
	return r.all
}

// LoadBuiltins registers the three built-in skills that ship with Marshal.
// This is typically called after creating a new registry before loading user skills.
func LoadBuiltins(r *Registry) error {
	builtins := []struct {
		name string
		data []byte
	}{
		{"schema-migration", schemaMigrationData},
		{"security-audit", securityAuditData},
		{"test-generation", testGenerationData},
	}

	for _, b := range builtins {
		var s Skill
		if err := toml.Unmarshal(b.data, &s); err != nil {
			return fmt.Errorf("parsing built-in skill %s: %w", b.name, err)
		}
		if err := r.Register(&s); err != nil {
			// Duplicate trigger might mean user override; skip built-in
			if strings.Contains(err.Error(), "duplicate trigger") {
				continue
			}
			return fmt.Errorf("registering built-in skill %s: %w", b.name, err)
		}
	}

	return nil
}
