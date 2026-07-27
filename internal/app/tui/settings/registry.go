package settings

import (
	"fmt"
	"sort"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/fuzzy"
)

// Registry is a flat, screen-independent index of root-frame leaf fields.
type Registry struct {
	st       *state
	order    []string
	byID     map[string]*field
	section  map[string]string
	defaults map[string]string // baseline string values at BuildRegistry time
}

// Change is the observed result of applying a setting. Changed is based on
// the field getter after its setter runs, rather than the requested value.
type Change struct {
	OldValue string
	NewValue string
	Changed  bool
}

// BuildRegistry indexes every addressable root-frame setting.
func BuildRegistry(cfg config.Config) *Registry {
	st := newState(cfg)
	registry := &Registry{
		st:       st,
		byID:     map[string]*field{},
		section:  map[string]string{},
		defaults: map[string]string{},
	}
	for _, section := range sectionList() {
		for _, field := range section.Root(st).List.Rows() {
			if field.ID == "" {
				continue
			}
			if _, duplicate := registry.byID[field.ID]; duplicate {
				continue
			}
			registry.order = append(registry.order, field.ID)
			registry.byID[field.ID] = field
			registry.section[field.ID] = section.Title
			if field.GetStr != nil {
				registry.defaults[field.ID] = field.GetStr()
			} else {
				registry.defaults[field.ID] = fieldString(field)
			}
		}
	}
	return registry
}

// Config returns the registry's mutable working config.
func (r *Registry) Config() config.Config { return r.st.cfg }

// Lookup returns a field by dotted key, matching both the stable id and the
// TOML path alias.
func (r *Registry) Lookup(key string) (*field, bool) {
	if field, ok := r.byID[key]; ok {
		return field, true
	}
	for _, f := range r.byID {
		if f.TomlPath == key {
			return f, true
		}
	}
	return nil, false
}

// fieldString returns the string representation of a field's current value,
// suitable for comparison against a baseline default.
func fieldString(f *field) string {
	switch f.Kind {
	case kindToggle:
		if f.GetBool() {
			return "on"
		}
		return "off"
	case kindScalar, kindEnum:
		if f.GetStr != nil {
			return f.GetStr()
		}
	}
	return ""
}

// Modified returns the IDs of fields whose current value differs from the
// baseline default captured at BuildRegistry time.
func (r *Registry) Modified() []string {
	var out []string
	for _, id := range r.order {
		f, ok := r.byID[id]
		if !ok {
			continue
		}
		if fieldString(f) != r.defaults[id] {
			out = append(out, id)
		}
	}
	return out
}

// OrderedKeys returns setting keys in insertion order (deterministic).
func (r *Registry) OrderedKeys() []string {
	return append([]string(nil), r.order...)
}

// Keys returns sorted dotted setting keys.
func (r *Registry) Keys() []string {
	keys := append([]string(nil), r.order...)
	sort.Strings(keys)
	return keys
}

// MatchKeys returns setting keys matching a query, ranked by section, key,
// title, and keywords.
func (r *Registry) MatchKeys(query string) []string {
	haystacks := make([]string, len(r.order))
	for index, key := range r.order {
		field := r.byID[key]
		haystacks[index] = r.section[key] + " " + key + " " + field.Title + " " + strings.Join(field.Keywords, " ")
	}

	matches := fuzzy.Rank(query, haystacks)
	keys := make([]string, 0, len(matches))
	for _, index := range matches {
		keys = append(keys, r.order[index])
	}
	return keys
}

// SectionOf returns the sidebar section title that owns a registry key,
// or "" for keys the registry does not know.
func (r *Registry) SectionOf(key string) string { return r.section[key] }

// Describe reports a field's type, display value, and available choices.
func (r *Registry) Describe(key string) (kind, current string, options []string, err error) {
	field, ok := r.Lookup(key)
	if !ok {
		return "", "", nil, fmt.Errorf("unknown setting %q", key)
	}

	switch field.Kind {
	case kindToggle:
		return "toggle", onOff(field.GetBool()), []string{"on", "off"}, nil
	case kindEnum:
		return "enum", field.GetStr(), field.Options(), nil
	case kindScalar:
		value := field.GetStr()
		if field.Masked {
			value = maskKey(value)
		}
		return "scalar", value, nil, nil
	default:
		return "", "", nil, fmt.Errorf("%s is edited in /settings (collection or action)", key)
	}
}

// Apply validates and applies a value without persisting it.
func (r *Registry) Apply(key, value string) (Change, error) {
	field, ok := r.Lookup(key)
	if !ok {
		return Change{}, fmt.Errorf("unknown setting %q", key)
	}

	switch field.Kind {
	case kindToggle:
		parsed, parseErr := parseOnOff(value)
		if parseErr != nil {
			return Change{}, parseErr
		}
		oldValue := onOff(field.GetBool())
		field.SetBool(parsed)
		newValue := onOff(field.GetBool())
		change := Change{
			OldValue: oldValue,
			NewValue: newValue,
			Changed:  oldValue != newValue,
		}
		if !change.Changed && newValue != onOff(parsed) {
			return Change{}, fmt.Errorf("is unavailable in the current configuration")
		}
		return change, nil
	case kindScalar, kindEnum:
		if field.SetStr == nil {
			return Change{}, fmt.Errorf("%s is read-only", key)
		}
		if field.Kind == kindEnum {
			found := false
			for _, option := range field.Options() {
				if option == value {
					found = true
					break
				}
			}
			if !found {
				return Change{}, fmt.Errorf("%s must be one of: %s", key, strings.Join(field.Options(), ", "))
			}
		}
		oldValue := field.GetStr()
		if err := field.SetStr(value); err != nil {
			return Change{}, err
		}
		newValue := field.GetStr()
		change := Change{
			OldValue: oldValue,
			NewValue: newValue,
			Changed:  oldValue != newValue,
		}
		if field.Masked {
			change.OldValue, change.NewValue = maskKey(oldValue), maskKey(newValue)
		}
		return change, nil
	default:
		return Change{}, fmt.Errorf("%s is edited in /settings (collection or action)", key)
	}
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not a toggle value (use on/off)", value)
	}
}
