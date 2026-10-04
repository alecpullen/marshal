package workspacecfg

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

var (
	toolchainRe = regexp.MustCompile(`^(go|node|python|rust)@[0-9][0-9A-Za-z.\-]*$`)
	sizeRe      = regexp.MustCompile(`^\d+[kmg]$`)
	headerRe    = regexp.MustCompile(`^\s*\[\[?\s*([^\[\]]+?)\s*\]\]?\s*(#.*)?$`)

	networkModes = map[string]bool{"open": true, "allowlist": true, "off": true}
	policyModes  = map[string]bool{"plan": true, "default": true, "edit": true, "copilot": true, "auto": true}
)

// layerKeys maps a section key to the layers it serves.
var layerKeys = map[string][]int{
	"workspace":      {1, 2},
	"packages":       {3},
	"mounts":         {4},
	"files":          {5},
	"secrets":        {6},
	"secrets.inject": {6},
	"network":        {7},
	"resources":      {8},
	"setup":          {9},
	"policy":         {0},
	"preview":        {0},
}

// rawDoc mirrors Doc for decoding, with [secrets] kept as a raw map so
// its two value forms can be split by type.
type rawDoc struct {
	Workspace Workspace            `toml:"workspace"`
	Packages  Packages             `toml:"packages"`
	Mounts    []Mount              `toml:"mounts"`
	Files     map[string]FileMount `toml:"files"`
	Secrets   map[string]any       `toml:"secrets"`
	Network   Network              `toml:"network"`
	Resources Resources            `toml:"resources"`
	Policy    Policy               `toml:"policy"`
	Preview   Preview              `toml:"preview"`
	Setup     Setup                `toml:"setup"`
}

// Parse decodes a workspace file. Unknown keys become warnings and the
// rest still decodes; a syntax error yields one error diagnostic and an
// empty Doc.
func Parse(src []byte) (Doc, []Section, []Diagnostic) {
	diags := []Diagnostic{}
	sections, headerLines := scanSections(src)

	var raw rawDoc
	dec := toml.NewDecoder(bytes.NewReader(src)).DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		var strict *toml.StrictMissingError
		var derr *toml.DecodeError
		switch {
		case errors.As(err, &strict):
			for _, e := range strict.Errors {
				row, _ := e.Position()
				diags = append(diags, Diagnostic{
					Line:     row,
					Message:  "unknown key " + strings.Join(e.Key(), "."),
					Severity: SeverityWarning,
				})
			}
		case errors.As(err, &derr):
			row, _ := derr.Position()
			return emptyDoc(), sections, []Diagnostic{{Line: row, Message: derr.Error(), Severity: SeverityError}}
		default:
			return emptyDoc(), sections, []Diagnostic{{Line: 1, Message: err.Error(), Severity: SeverityError}}
		}
	}

	d := Doc{
		Workspace:  raw.Workspace,
		Packages:   raw.Packages,
		Mounts:     raw.Mounts,
		Files:      raw.Files,
		SecretsEnv: map[string]string{},
		Inject:     map[string]Inject{},
		Network:    raw.Network,
		Resources:  raw.Resources,
		Policy:     raw.Policy,
		Preview:    raw.Preview,
		Setup:      raw.Setup,
	}
	if d.Files == nil {
		d.Files = map[string]FileMount{}
	}
	if d.Mounts == nil {
		d.Mounts = []Mount{}
	}

	secretsLine := headerLines["secrets"]
	if secretsLine == 0 {
		secretsLine = 1
	}
	keys := make([]string, 0, len(raw.Secrets))
	for k := range raw.Secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch v := raw.Secrets[k].(type) {
		case string:
			d.SecretsEnv[k] = v
		case map[string]any:
			if k != "inject" {
				diags = append(diags, errAt(secretsLine, "[secrets] has unsupported table %q", k))
				continue
			}
			hosts := make([]string, 0, len(v))
			for h := range v {
				hosts = append(hosts, h)
			}
			sort.Strings(hosts)
			for _, h := range hosts {
				m, ok := v[h].(map[string]any)
				if !ok {
					diags = append(diags, errAt(secretsLine, "secrets.inject.%q must be a table", h))
					continue
				}
				var inj Inject
				inj.Ref, _ = m["ref"].(string)
				inj.Header, _ = m["header"].(string)
				inj.Format, _ = m["format"].(string)
				d.Inject[h] = inj
			}
		default:
			diags = append(diags, errAt(secretsLine, "[secrets] value for %q must be a string", k))
		}
	}

	diags = append(diags, validate(d, headerLines)...)
	sort.SliceStable(diags, func(i, j int) bool { return diags[i].Line < diags[j].Line })
	return d, sections, diags
}

func emptyDoc() Doc {
	return Doc{
		Mounts:     []Mount{},
		Files:      map[string]FileMount{},
		SecretsEnv: map[string]string{},
		Inject:     map[string]Inject{},
	}
}

func errAt(line int, format string, args ...any) Diagnostic {
	return Diagnostic{Line: line, Message: fmt.Sprintf(format, args...), Severity: SeverityError}
}

func lineOr1(headerLines map[string]int, key string) int {
	if l := headerLines[key]; l > 0 {
		return l
	}
	return 1
}

func validate(d Doc, hl map[string]int) []Diagnostic {
	var out []Diagnostic
	wl := lineOr1(hl, "workspace")
	for _, t := range d.Workspace.Toolchains {
		if !toolchainRe.MatchString(t) {
			out = append(out, errAt(wl, "invalid toolchain %q: want <go|node|python|rust>@<version>", t))
		}
	}
	nl := lineOr1(hl, "network")
	if d.Network.Mode != "" && !networkModes[d.Network.Mode] {
		out = append(out, errAt(nl, "invalid network mode %q: want open, allowlist or off", d.Network.Mode))
	}
	sl := lineOr1(hl, "secrets")
	for _, k := range sortedKeys(d.SecretsEnv) {
		if !strings.HasPrefix(d.SecretsEnv[k], "vault:") {
			out = append(out, errAt(sl, "secret %q must reference vault:<ref>", k))
		}
	}
	il := hl["secrets.inject"]
	if il == 0 {
		il = sl
	}
	for _, h := range sortedKeys(d.Inject) {
		if !strings.HasPrefix(d.Inject[h].Ref, "vault:") {
			out = append(out, errAt(il, "inject %q ref must start with vault:", h))
		}
	}
	ml := lineOr1(hl, "mounts")
	for i, m := range d.Mounts {
		if (m.Repo == "") == (m.Volume == "") {
			out = append(out, errAt(ml, "mount %d needs exactly one of repo or volume", i+1))
		}
		if strings.TrimSpace(m.Target) == "" {
			out = append(out, errAt(ml, "mount %d needs a target", i+1))
		}
	}
	pl := lineOr1(hl, "policy")
	if d.Policy.Mode != "" && !policyModes[d.Policy.Mode] {
		out = append(out, errAt(pl, "invalid policy mode %q", d.Policy.Mode))
	}
	vl := lineOr1(hl, "preview")
	seenPort := map[int]bool{}
	for _, port := range d.Preview.Ports {
		switch {
		case port < 1 || port > 65535:
			out = append(out, errAt(vl, "preview port %d out of range: want 1-65535", port))
		case seenPort[port]:
			out = append(out, errAt(vl, "preview port %d listed twice", port))
		}
		seenPort[port] = true
	}
	rl := lineOr1(hl, "resources")
	if d.Resources.Memory != "" && !sizeRe.MatchString(d.Resources.Memory) {
		out = append(out, errAt(rl, "resources.memory %q must look like 512m or 4g", d.Resources.Memory))
	}
	if d.Resources.Disk != "" && !sizeRe.MatchString(d.Resources.Disk) {
		out = append(out, errAt(rl, "resources.disk %q must look like 512m or 4g", d.Resources.Disk))
	}
	if d.Resources.Timeout != "" {
		if _, err := time.ParseDuration(d.Resources.Timeout); err != nil {
			out = append(out, errAt(rl, "resources.timeout %q is not a duration", d.Resources.Timeout))
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// scanSections finds table headers and returns the layer sections plus
// the first header line (1-based) of each key.
func scanSections(src []byte) ([]Section, map[string]int) {
	lines := strings.Split(string(src), "\n")
	type hdr struct {
		key  string
		line int
	}
	// Headers come from the TOML parser, not from matching lines, so text
	// inside a multi-line string or array that looks like a header is not
	// one. On a syntax error the headers found before it are kept.
	var hdrs []hdr
	first := map[string]int{}
	var p unstable.Parser
	p.Reset(src)
	for p.NextExpression() {
		n := p.Expression()
		if n.Kind != unstable.Table && n.Kind != unstable.ArrayTable {
			continue
		}
		var parts []string
		it := n.Key()
		offset := -1
		for it.Next() {
			kn := it.Node()
			if offset < 0 {
				offset = int(kn.Raw.Offset)
			}
			parts = append(parts, string(kn.Data))
		}
		if offset < 0 {
			continue
		}
		line := 1 + bytes.Count(src[:offset], []byte("\n"))
		key := canonicalKey(strings.Join(parts, "."))
		hdrs = append(hdrs, hdr{key, line})
		if _, ok := first[key]; !ok {
			first[key] = line
		}
	}
	// Unknown headers still end the preceding section.
	type rng struct{ start, end int }
	byKey := map[string]*rng{}
	var order []string
	for i, h := range hdrs {
		end := len(lines)
		if i+1 < len(hdrs) {
			end = hdrs[i+1].line - 1
		}
		// Blank lines and comments just above the next header belong to
		// that header, not to this section.
		for end > h.line && isBlankOrComment(lines[end-1]) {
			end--
		}
		if _, ok := layerKeys[h.key]; !ok {
			continue
		}
		if r, ok := byKey[h.key]; ok {
			r.end = end
			continue
		}
		byKey[h.key] = &rng{h.line, end}
		order = append(order, h.key)
	}
	var secs []Section
	for _, k := range order {
		r := byKey[k]
		for _, layer := range layerKeys[k] {
			secs = append(secs, Section{Layer: layer, Key: k, StartLine: r.start, EndLine: r.end})
		}
	}
	sort.SliceStable(secs, func(i, j int) bool { return secs[i].StartLine < secs[j].StartLine })
	return secs, first
}

// canonicalKey folds a table header onto the section that owns it:
// [files."a/b"] belongs to files and [secrets.inject."h"] to
// secrets.inject. Unknown headers are returned unchanged.
func canonicalKey(k string) string {
	for _, owner := range []string{"secrets.inject", "secrets", "files", "workspace", "packages", "mounts", "network", "resources", "policy", "preview", "setup"} {
		if k == owner || strings.HasPrefix(k, owner+".") {
			return owner
		}
	}
	return k
}

func isBlankOrComment(ln string) bool {
	t := strings.TrimSpace(ln)
	return t == "" || strings.HasPrefix(t, "#")
}

func errorsOf(d []Diagnostic) []Diagnostic {
	var out []Diagnostic
	for _, x := range d {
		if x.Severity == SeverityError {
			out = append(out, x)
		}
	}
	return out
}
