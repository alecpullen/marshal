package bridge

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readVerifyCommands reads the build and test keys of [sdd.verify] from
// root's .marshal/config.toml. The bridge is stdlib-only and has no TOML
// parser, so scanTOMLStrings walks the file with the lexical rules that
// matter here: tables, dotted keys, comments, and basic, literal and
// multi-line strings. It reads only string values. An inline table
// (`verify = { build = "x" }`) or any other form reads as unset.
//
// Callers must only report the result for a trusted project: the engine
// ignores an untrusted project's config.
func readVerifyCommands(root string) VerifyCommands {
	data, err := os.ReadFile(filepath.Join(root, ".marshal", "config.toml"))
	if err != nil {
		return VerifyCommands{}
	}
	kv := scanTOMLStrings(string(data))
	return VerifyCommands{Build: kv["sdd.verify.build"], Test: kv["sdd.verify.test"]}
}

// scanTOMLStrings returns every string-valued key of a TOML document,
// keyed by its full dotted path. Values of other types are skipped.
func scanTOMLStrings(src string) map[string]string {
	out := map[string]string{}
	table := ""
	pos := 0
	for pos < len(src) {
		eol := strings.IndexByte(src[pos:], '\n')
		lineEnd := len(src)
		if eol >= 0 {
			lineEnd = pos + eol
		}
		line := strings.TrimSpace(src[pos:lineEnd])
		next := lineEnd + 1
		switch {
		case line == "" || line[0] == '#':
		case strings.HasPrefix(line, "[["):
			table = "[[array]]"
		case line[0] == '[':
			end := strings.IndexByte(line, ']')
			if end < 0 {
				table = "[invalid]"
				break
			}
			table = normalizeTOMLKey(line[1:end])
		default:
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				break
			}
			key := normalizeTOMLKey(line[:eq])
			// vs is where the value starts in src.
			vs := pos + strings.Index(src[pos:lineEnd], "=") + 1
			for vs < len(src) && (src[vs] == ' ' || src[vs] == '\t') {
				vs++
			}
			if vs < len(src) && (src[vs] == '"' || src[vs] == '\'') {
				val, end, ok := scanTOMLString(src, vs)
				if ok && restIsBlank(src, end) {
					path := key
					if table != "" {
						path = table + "." + key
					}
					out[path] = val
				}
				next = skipLine(src, end)
			} else {
				next = skipTOMLValue(src, vs)
			}
		}
		pos = next
	}
	return out
}

// normalizeTOMLKey drops the spaces around the dots of a bare key path.
func normalizeTOMLKey(k string) string {
	parts := strings.Split(k, ".")
	for i := range parts {
		p := strings.TrimSpace(parts[i])
		if len(p) >= 2 && (p[0] == '"' || p[0] == '\'') && p[len(p)-1] == p[0] {
			p = p[1 : len(p)-1]
		}
		parts[i] = p
	}
	return strings.Join(parts, ".")
}

// restIsBlank reports whether the rest of src's line from i is blank or a
// comment.
func restIsBlank(src string, i int) bool {
	for ; i < len(src) && src[i] != '\n'; i++ {
		switch src[i] {
		case ' ', '\t', '\r':
		case '#':
			return true
		default:
			return false
		}
	}
	return true
}

// skipLine returns the index after the end of the line containing i.
func skipLine(src string, i int) int {
	if n := strings.IndexByte(src[i:], '\n'); n >= 0 {
		return i + n + 1
	}
	return len(src)
}

// skipTOMLValue returns the index after a non-string value starting at i:
// the end of its line, or past the closing bracket of a multi-line array
// or inline table. Strings and comments inside are skipped whole.
func skipTOMLValue(src string, i int) int {
	depth := 0
	for i < len(src) {
		switch c := src[i]; c {
		case '"', '\'':
			_, end, ok := scanTOMLString(src, i)
			if !ok {
				return skipLine(src, i)
			}
			i = end
			continue
		case '#':
			i = skipLine(src, i)
			if depth <= 0 {
				return i
			}
			continue
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		case '\n':
			if depth <= 0 {
				return i + 1
			}
		}
		i++
	}
	return len(src)
}

// scanTOMLString reads the string starting at src[i] (a quote). It returns
// the value and the index after the closing quote. Escapes in basic
// strings other than those Go also accepts make it fail.
func scanTOMLString(src string, i int) (val string, end int, ok bool) {
	q := src[i]
	if strings.HasPrefix(src[i:], strings.Repeat(string(q), 3)) {
		delim := strings.Repeat(string(q), 3)
		body := i + 3
		j := body
		for j < len(src) {
			if q == '"' && src[j] == '\\' {
				j += 2
				continue
			}
			if strings.HasPrefix(src[j:], delim) {
				// Up to two extra quotes may precede the closing delimiter.
				k := j
				for k < len(src) && src[k] == q && k-j < 5 {
					k++
				}
				if k-j >= 3 {
					j = k - 3
				}
				raw := src[body:j]
				raw = strings.TrimPrefix(strings.TrimPrefix(raw, "\r"), "\n")
				if q == '\'' {
					return raw, j + 3, true
				}
				raw = trimLineEndingBackslashes(raw)
				v, err := strconv.Unquote(`"` + escapeForUnquote(raw) + `"`)
				if err != nil {
					return "", j + 3, false
				}
				return v, j + 3, true
			}
			j++
		}
		return "", len(src), false
	}
	j := i + 1
	for j < len(src) && src[j] != '\n' {
		if q == '"' && src[j] == '\\' {
			j += 2
			continue
		}
		if src[j] == q {
			raw := src[i+1 : j]
			if q == '\'' {
				return raw, j + 1, true
			}
			v, err := strconv.Unquote(`"` + raw + `"`)
			if err != nil {
				return "", j + 1, false
			}
			return v, j + 1, true
		}
		j++
	}
	return "", j, false
}

// trimLineEndingBackslashes applies the TOML rule that a backslash at the
// end of a line in a multi-line basic string removes the newline and the
// whitespace that follows.
func trimLineEndingBackslashes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r') {
				j++
			}
			if j < len(s) && s[j] == '\n' {
				for j < len(s) && strings.ContainsRune(" \t\r\n", rune(s[j])) {
					j++
				}
				i = j - 1
				continue
			}
			b.WriteByte(s[i])
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// escapeForUnquote makes a multi-line body safe to place between double
// quotes for strconv.Unquote: raw newlines and unescaped quotes are
// escaped, existing escapes are kept.
func escapeForUnquote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteByte(s[i])
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
