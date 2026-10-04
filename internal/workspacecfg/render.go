package workspacecfg

import (
	"encoding/json"
	"fmt"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// canonicalOrder is the section order Format writes and Patch inserts in.
var canonicalOrder = []string{"workspace", "packages", "mounts", "files", "secrets", "network", "resources", "policy", "setup"}

// renderSection renders one section key of d as TOML, trimmed. It returns
// nil when the section is empty. Wrapper types keep the field order stable.
func renderSection(key string, d Doc) []byte {
	var v any
	switch key {
	case "workspace":
		v = struct {
			Workspace Workspace `toml:"workspace"`
		}{d.Workspace}
	case "packages":
		v = struct {
			Packages Packages `toml:"packages"`
		}{d.Packages}
	case "mounts":
		if len(d.Mounts) == 0 {
			return nil
		}
		v = struct {
			Mounts []Mount `toml:"mounts"`
		}{d.Mounts}
	case "files":
		if len(d.Files) == 0 {
			return nil
		}
		v = struct {
			Files map[string]FileMount `toml:"files"`
		}{d.Files}
	case "secrets":
		sec := map[string]any{}
		for k, val := range d.SecretsEnv {
			sec[k] = val
		}
		if len(d.Inject) > 0 {
			sec["inject"] = d.Inject
		}
		if len(sec) == 0 {
			return nil
		}
		v = map[string]any{"secrets": sec}
	case "network":
		v = struct {
			Network Network `toml:"network"`
		}{d.Network}
	case "resources":
		v = struct {
			Resources Resources `toml:"resources"`
		}{d.Resources}
	case "policy":
		v = struct {
			Policy Policy `toml:"policy"`
		}{d.Policy}
	case "setup":
		v = struct {
			Setup Setup `toml:"setup"`
		}{d.Setup}
	default:
		return nil
	}
	out, err := toml.Marshal(v)
	if err != nil {
		return nil
	}
	if !hasBody(out) {
		return nil
	}
	return []byte(strings.TrimSpace(string(out)))
}

// hasBody reports whether rendered TOML holds anything besides table
// headers and blank lines.
func hasBody(b []byte) bool {
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == "" || headerRe.MatchString(ln) {
			continue
		}
		return true
	}
	return false
}

// Format renders d canonically: every non-empty section in canonical
// order, separated by blank lines.
func Format(d Doc) []byte {
	var parts []string
	for _, key := range canonicalOrder {
		if s := renderSection(key, d); s != nil {
			parts = append(parts, string(s))
		}
	}
	if len(parts) == 0 {
		return []byte{}
	}
	return []byte(strings.Join(parts, "\n\n") + "\n")
}

// layerSectionKeys returns the section keys a layer owns.
func layerSectionKeys(layer int) []string {
	switch layer {
	case 0:
		return []string{"policy"}
	case 1, 2:
		return []string{"workspace"}
	case 3:
		return []string{"packages"}
	case 4:
		return []string{"mounts"}
	case 5:
		return []string{"files"}
	case 6:
		return []string{"secrets"}
	case 7:
		return []string{"network"}
	case 8:
		return []string{"resources"}
	case 9:
		return []string{"setup"}
	}
	return nil
}

// applyLayer decodes value into the part of d that layer owns.
func applyLayer(d *Doc, layer int, value json.RawMessage) error {
	var target any
	switch layer {
	case 0:
		target = &d.Policy
	case 1, 2:
		target = &d.Workspace
	case 3:
		target = &d.Packages
	case 4:
		d.Mounts = nil
		target = &d.Mounts
	case 5:
		d.Files = nil
		target = &d.Files
	case 6:
		part := struct {
			SecretsEnv map[string]string `json:"secretsEnv"`
			Inject     map[string]Inject `json:"inject"`
		}{}
		if err := json.Unmarshal(value, &part); err != nil {
			return fmt.Errorf("decode layer %d value: %w", layer, err)
		}
		d.SecretsEnv, d.Inject = part.SecretsEnv, part.Inject
		return nil
	case 7:
		target = &d.Network
	case 8:
		target = &d.Resources
	case 9:
		target = &d.Setup
	default:
		return fmt.Errorf("unknown layer %d", layer)
	}
	if layer != 4 && layer != 5 {
		// Replace the part wholesale rather than merging into it.
		switch t := target.(type) {
		case *Policy:
			*t = Policy{}
		case *Workspace:
			*t = Workspace{}
		case *Packages:
			*t = Packages{}
		case *Network:
			*t = Network{}
		case *Resources:
			*t = Resources{}
		case *Setup:
			*t = Setup{}
		}
	}
	if err := json.Unmarshal(value, target); err != nil {
		return fmt.Errorf("decode layer %d value: %w", layer, err)
	}
	return nil
}

// Patch re-renders the section(s) of one layer from value (JSON) and
// splices them into src at the section's line range, leaving everything
// else byte for byte. A missing section is inserted in canonical order.
func Patch(src []byte, layer int, value json.RawMessage) ([]byte, error) {
	d, secs, diags := Parse(src)
	if errs := errorsOf(diags); len(errs) > 0 {
		return nil, fmt.Errorf("source has errors: line %d: %s", errs[0].Line, errs[0].Message)
	}
	keys := layerSectionKeys(layer)
	if keys == nil {
		return nil, fmt.Errorf("unknown layer %d", layer)
	}
	if err := applyLayer(&d, layer, value); err != nil {
		return nil, err
	}
	key := keys[0]
	rendered := renderSection(key, d)

	// Layers 1 and 2 share [workspace]. Layer 6 spans [secrets] and
	// [secrets.inject], so its range covers every layer-6 section.
	var cur *Section
	for _, s := range secs {
		if s.Layer != layer {
			continue
		}
		if cur == nil {
			w := s
			cur = &w
			continue
		}
		cur.StartLine = min(cur.StartLine, s.StartLine)
		cur.EndLine = max(cur.EndLine, s.EndLine)
	}

	lines := strings.Split(string(src), "\n")
	var out []string
	switch {
	case cur != nil && rendered == nil:
		out = append(append(out, lines[:cur.StartLine-1]...), lines[cur.EndLine:]...)
	case cur != nil:
		out = append(out, lines[:cur.StartLine-1]...)
		out = append(out, strings.Split(string(rendered), "\n")...)
		out = append(out, lines[cur.EndLine:]...)
	case rendered == nil:
		return append([]byte(nil), src...), nil
	default:
		at := insertionLine(key, secs, len(lines))
		block := strings.Split(string(rendered), "\n")
		if at > 0 && strings.TrimSpace(lines[at-1]) != "" {
			block = append([]string{""}, block...)
		}
		out = append(out, lines[:at]...)
		out = append(out, block...)
		if at < len(lines) && strings.TrimSpace(lines[at]) != "" {
			out = append(out, "")
		}
		out = append(out, lines[at:]...)
	}
	res := []byte(strings.Join(out, "\n"))
	if len(res) > 0 && res[len(res)-1] != '\n' {
		res = append(res, '\n')
	}
	if _, _, ds := Parse(res); len(errorsOf(ds)) > 0 {
		return nil, fmt.Errorf("patched source no longer parses: line %d: %s", ds[0].Line, ds[0].Message)
	}
	return res, nil
}

// insertionLine returns the index in lines (0-based, i.e. "after this many
// lines") at which a missing section key goes: after the nearest earlier
// canonical section that exists, else before the first existing section,
// else at the end of the file.
func insertionLine(key string, secs []Section, total int) int {
	pos := -1
	for i, k := range canonicalOrder {
		if k == key {
			pos = i
		}
	}
	for i := pos - 1; i >= 0; i-- {
		end := 0
		for _, s := range secs {
			if s.Key == canonicalOrder[i] || (canonicalOrder[i] == "secrets" && s.Key == "secrets.inject") {
				end = max(end, s.EndLine)
			}
		}
		if end > 0 {
			return end
		}
	}
	if len(secs) > 0 {
		first := secs[0].StartLine
		for _, s := range secs {
			first = min(first, s.StartLine)
		}
		return first - 1
	}
	// No sections: append, ignoring the empty element a trailing newline
	// leaves behind.
	if total > 0 {
		return total - 1
	}
	return 0
}
