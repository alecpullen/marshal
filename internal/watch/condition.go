package watch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Sample is the normalized output of a single source evaluation. It is the
// input to condition.Eval. Stdout is the tail-capped command/file output;
// ExitCode is the command's exit code (0 for file sources); Exists and Hash
// describe a file source's current state.
type Sample struct {
	Stdout   string
	ExitCode int
	Exists   bool
	Hash     string
}

// condition is a parsed, stateless predicate over a Sample. The watch struct
// holds the previous Sample so the change condition can compare across
// evaluations.
type condition interface {
	// Eval reports whether the sample satisfies the condition. prev is the
	// previous sample (zero value on the first evaluation) and is only used
	// by the change condition.
	Eval(sample Sample, prev Sample) bool
}

// parseCondition parses a raw condition string into a condition. The empty
// string means the default "change" condition. A malformed condition is a
// registration error.
func parseCondition(raw string) (condition, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return changeCondition{}, nil
	}

	fields := strings.Fields(raw)
	switch fields[0] {
	case "change":
		if len(fields) != 1 {
			return nil, fmt.Errorf("condition %q: change takes no arguments", raw)
		}
		return changeCondition{}, nil
	case "exit_code":
		if len(fields) != 2 {
			return nil, fmt.Errorf("condition %q: exit_code requires exactly one integer argument", raw)
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("condition %q: exit_code argument must be an integer: %v", raw, err)
		}
		return exitCodeCondition{n: n}, nil
	case "regex":
		if len(fields) != 2 {
			return nil, fmt.Errorf("condition %q: regex requires exactly one pattern argument", raw)
		}
		re, err := regexp.Compile(fields[1])
		if err != nil {
			return nil, fmt.Errorf("condition %q: invalid regex: %v", raw, err)
		}
		return regexCondition{re: re}, nil
	case "json":
		if len(fields) != 4 {
			return nil, fmt.Errorf("condition %q: json requires <path> <op> <value>", raw)
		}
		op := fields[2]
		switch op {
		case "=", "!=", "<", ">":
		default:
			return nil, fmt.Errorf("condition %q: unsupported json operator %q (want =, !=, <, >)", raw, op)
		}
		return jsonCondition{path: fields[1], op: op, value: fields[3]}, nil
	default:
		return nil, fmt.Errorf("condition %q: unknown condition type %q", raw, fields[0])
	}
}

// changeCondition fires when the sample differs from the previous sample.
type changeCondition struct{}

func (changeCondition) Eval(sample Sample, prev Sample) bool {
	return sample != prev
}

// exitCodeCondition fires when the sample's exit code equals n.
type exitCodeCondition struct {
	n int
}

func (c exitCodeCondition) Eval(sample Sample, _ Sample) bool {
	return sample.ExitCode == c.n
}

// regexCondition fires when the pattern matches the sample's stdout tail.
type regexCondition struct {
	re *regexp.Regexp
}

func (c regexCondition) Eval(sample Sample, _ Sample) bool {
	return c.re.MatchString(sample.Stdout)
}

// jsonCondition extracts a field from the sample's stdout (parsed as JSON)
// and compares it against a literal value. Ops: =, !=, <, >. Numeric compare
// when both sides parse as float, string compare otherwise. A non-JSON sample
// evaluates to false (not an error).
type jsonCondition struct {
	path  string
	op    string
	value string
}

func (c jsonCondition) Eval(sample Sample, _ Sample) bool {
	var doc any
	if err := json.Unmarshal([]byte(sample.Stdout), &doc); err != nil {
		return false
	}
	got, ok := walkPath(doc, c.path)
	if !ok {
		return false
	}
	return compareValues(got, c.value, c.op)
}

// walkPath walks a dot-path (a.b[0].c) over an unmarshalled JSON document.
// It returns the value at the path and whether it was found.
func walkPath(doc any, path string) (any, bool) {
	cur := doc
	segments := splitPath(path)
	for _, seg := range segments {
		name, idx, hasIdx := parseSegment(seg)
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[name]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			if !hasIdx {
				return nil, false
			}
			if idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
		if hasIdx {
			arr, ok := cur.([]any)
			if !ok {
				return nil, false
			}
			if idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
		}
	}
	return cur, true
}

// splitPath splits a dot-path into segments, keeping bracket indices attached
// to their preceding segment (e.g. "a.b[0].c" -> ["a", "b[0]", "c"]).
func splitPath(path string) []string {
	var segs []string
	var cur strings.Builder
	for _, r := range path {
		if r == '.' {
			if cur.Len() > 0 {
				segs = append(segs, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		segs = append(segs, cur.String())
	}
	return segs
}

// parseSegment splits a segment like "b[0]" into its name ("b") and index
// (0, hasIdx=true). A bare "[0]" yields name "" and hasIdx=true.
func parseSegment(seg string) (name string, idx int, hasIdx bool) {
	if i := strings.Index(seg, "["); i >= 0 {
		name = seg[:i]
		rest := seg[i+1:]
		if j := strings.Index(rest, "]"); j >= 0 {
			n, err := strconv.Atoi(rest[:j])
			if err == nil {
				return name, n, true
			}
		}
		return name, 0, false
	}
	return seg, 0, false
}

// compareValues compares a JSON-extracted value against a literal string using
// the given operator. Numeric compare when both sides parse as float, string
// compare otherwise.
func compareValues(got any, want string, op string) bool {
	// Try numeric comparison first.
	gotF, gotNum := toFloat(got)
	wantF, wantNum := toFloat(want)
	if gotNum && wantNum {
		switch op {
		case "=":
			return gotF == wantF
		case "!=":
			return gotF != wantF
		case "<":
			return gotF < wantF
		case ">":
			return gotF > wantF
		}
	}

	gotS := fmt.Sprintf("%v", got)
	switch op {
	case "=":
		return gotS == want
	case "!=":
		return gotS != want
	case "<":
		return gotS < want
	case ">":
		return gotS > want
	}
	return false
}

// toFloat attempts to coerce a JSON value or string to a float64.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}
