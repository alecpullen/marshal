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
	st      *state
	order   []string
	byID    map[string]*field
	section map[string]string
}

// BuildRegistry indexes every addressable root-frame setting.
func BuildRegistry(cfg config.Config) *Registry {
	st := newState(cfg)
	registry := &Registry{
		st:      st,
		byID:    map[string]*field{},
		section: map[string]string{},
	}
	for _, section := range sectionList() {
		for _, field := range section.root(st).list.Rows() {
			if field.id == "" {
				continue
			}
			if _, duplicate := registry.byID[field.id]; duplicate {
				continue
			}
			registry.order = append(registry.order, field.id)
			registry.byID[field.id] = field
			registry.section[field.id] = section.title
		}
	}
	return registry
}

// Config returns the registry's mutable working config.
func (r *Registry) Config() config.Config { return r.st.cfg }

// Lookup returns a field by dotted key.
func (r *Registry) Lookup(key string) (*field, bool) {
	field, ok := r.byID[key]
	return field, ok
}

// Keys returns sorted dotted setting keys.
func (r *Registry) Keys() []string {
	keys := append([]string(nil), r.order...)
	sort.Strings(keys)
	return keys
}

// Match returns fields matching a query, ranked by section, key, title, and keywords.
func (r *Registry) Match(query string) []*field {
	haystacks := make([]string, len(r.order))
	for index, key := range r.order {
		field := r.byID[key]
		haystacks[index] = r.section[key] + " " + key + " " + field.title + " " + strings.Join(field.keywords, " ")
	}

	matches := fuzzy.Rank(query, haystacks)
	fields := make([]*field, 0, len(matches))
	for _, index := range matches {
		fields = append(fields, r.byID[r.order[index]])
	}
	return fields
}

// Describe reports a field's type, display value, and available choices.
func (r *Registry) Describe(key string) (kind, current string, options []string, err error) {
	field, ok := r.byID[key]
	if !ok {
		return "", "", nil, fmt.Errorf("unknown setting %q", key)
	}

	switch field.kind {
	case kindToggle:
		return "toggle", onOff(field.getBool()), []string{"on", "off"}, nil
	case kindEnum:
		return "enum", field.getStr(), field.options(), nil
	case kindScalar:
		value := field.getStr()
		if field.masked {
			value = maskKey(value)
		}
		return "scalar", value, nil, nil
	default:
		return "", "", nil, fmt.Errorf("%s is edited in /settings (collection or action)", key)
	}
}

// Apply validates and applies a value without persisting it.
func (r *Registry) Apply(key, value string) (oldValue, newValue string, err error) {
	field, ok := r.byID[key]
	if !ok {
		return "", "", fmt.Errorf("unknown setting %q", key)
	}

	switch field.kind {
	case kindToggle:
		parsed, parseErr := parseOnOff(value)
		if parseErr != nil {
			return "", "", parseErr
		}
		oldValue = onOff(field.getBool())
		field.setBool(parsed)
		return oldValue, onOff(parsed), nil
	case kindScalar, kindEnum:
		if field.setStr == nil {
			return "", "", fmt.Errorf("%s is read-only", key)
		}
		if field.kind == kindEnum {
			found := false
			for _, option := range field.options() {
				if option == value {
					found = true
					break
				}
			}
			if !found {
				return "", "", fmt.Errorf("%s must be one of: %s", key, strings.Join(field.options(), ", "))
			}
		}
		oldValue = field.getStr()
		if err := field.setStr(value); err != nil {
			return "", "", err
		}
		newValue = field.getStr()
		if field.masked {
			oldValue, newValue = maskKey(oldValue), maskKey(newValue)
		}
		return oldValue, newValue, nil
	default:
		return "", "", fmt.Errorf("%s is edited in /settings (collection or action)", key)
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
