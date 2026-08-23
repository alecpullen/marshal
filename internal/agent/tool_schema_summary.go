package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// toolSchemaProps is the subset of JSON Schema that registry tools declare.
type toolSchemaProps struct {
	Properties map[string]struct {
		Type string `json:"type"`
		Enum []any  `json:"enum"`
	} `json:"properties"`
	Required []string `json:"required"`
}

// summarizeToolSchema renders a compact one-line argument synopsis from a
// tool's JSON Schema: required args first ("name:type"), then optional args
// alphabetically ("name?:type"). Short enum value lists replace the type
// inline. Returns "" when the schema is absent, unparseable, or declares no
// properties, so callers fall back to the bare name/risk/description line.
func summarizeToolSchema(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s toolSchemaProps
	if err := json.Unmarshal(raw, &s); err != nil || len(s.Properties) == 0 {
		return ""
	}
	required := make(map[string]bool, len(s.Required))
	for _, name := range s.Required {
		required[name] = true
	}
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		ri, rj := required[names[i]], required[names[j]]
		if ri != rj {
			return ri
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for _, name := range names {
		prop := s.Properties[name]
		typ := prop.Type
		if len(prop.Enum) > 0 {
			vals := make([]string, 0, len(prop.Enum))
			for _, v := range prop.Enum {
				vals = append(vals, fmt.Sprint(v))
			}
			if joined := strings.Join(vals, "|"); len(joined) <= 24 {
				typ = joined
			}
		}
		if typ == "" {
			typ = "any"
		}
		if required[name] {
			parts = append(parts, name+":"+typ)
		} else {
			parts = append(parts, name+"?:"+typ)
		}
	}
	return "args: " + strings.Join(parts, ", ")
}
