package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var ErrInvalidArgs = errors.New("invalid tool arguments")

const (
	// maxReportedCauses bounds how many individual violations are named
	// before the message is summarized. The message is model-facing; a wall
	// of causes costs context without helping it correct anything.
	maxReportedCauses = 5
	// maxErrorChars bounds the whole message for the same reason.
	maxErrorChars = 500
)

// errPrinter renders the library's error kinds into English. It holds no
// mutable state and is safe for concurrent use.
var errPrinter = message.NewPrinter(language.English)

// Validator wraps a compiled JSON Schema. A nil *Validator means the tool
// declared no schema and accepts any JSON object — methods are nil-safe.
type Validator struct {
	schema *jsonschema.Schema
}

// CompileSchema compiles a tool's declared schema. An empty raw schema is not
// an error: it means the tool is unconstrained, and (nil, nil) is returned.
func CompileSchema(toolName string, raw json.RawMessage) (*Validator, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("schema for %q is not valid JSON: %w", toolName, err)
	}
	// Tool schemas carry no $id, so they are registered under a synthetic
	// URI and compiled from it.
	uri := "tool:///" + toolName
	c := jsonschema.NewCompiler()
	if err := c.AddResource(uri, doc); err != nil {
		return nil, fmt.Errorf("schema for %q could not be loaded: %w", toolName, err)
	}
	compiled, err := c.Compile(uri)
	if err != nil {
		return nil, fmt.Errorf("schema for %q is not a valid JSON Schema: %w", toolName, err)
	}
	return &Validator{schema: compiled}, nil
}

// Validate reports whether args satisfy the schema. Errors wrap ErrInvalidArgs
// and name the offending fields so the model can correct the call.
func (v *Validator) Validate(toolName string, args json.RawMessage) error {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		// An absent argument object is an empty one. Normalizing here rather
		// than short-circuiting is what lets a schema with required fields
		// reject a no-argument call.
		trimmed = []byte(`{}`)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(trimmed))
	if err != nil {
		return fmt.Errorf("%w for %q: arguments are not valid JSON: %v", ErrInvalidArgs, toolName, err)
	}
	if v == nil || v.schema == nil {
		// Unconstrained, but still required to be a JSON object: every tool
		// handler decodes into a struct.
		if _, ok := instance.(map[string]any); !ok {
			return fmt.Errorf("%w for %q: expected a JSON object", ErrInvalidArgs, toolName)
		}
		return nil
	}
	if err := v.schema.Validate(instance); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return fmt.Errorf("%w for %q: %s", ErrInvalidArgs, toolName, renderCauses(ve))
		}
		return fmt.Errorf("%w for %q: %v", ErrInvalidArgs, toolName, err)
	}
	return nil
}

// renderCauses flattens a validation error tree into a stable, bounded,
// model-readable sentence.
func renderCauses(ve *jsonschema.ValidationError) string {
	leaves := flattenCauses(ve, nil)
	parts := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		parts = append(parts, formatLocation(leaf.InstanceLocation)+": "+leaf.ErrorKind.LocalizedString(errPrinter))
	}
	// Sorting makes the message identical for identical input regardless of
	// the order the library happened to walk the schema.
	sort.Strings(parts)
	parts = dedupe(parts)

	extra := 0
	if len(parts) > maxReportedCauses {
		extra = len(parts) - maxReportedCauses
		parts = parts[:maxReportedCauses]
	}
	out := strings.Join(parts, "; ")
	if extra > 0 {
		out += fmt.Sprintf(" (+%d more)", extra)
	}
	if len(out) > maxErrorChars {
		out = out[:maxErrorChars] + "…"
	}
	return out
}

func flattenCauses(ve *jsonschema.ValidationError, acc []*jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(ve.Causes) == 0 {
		return append(acc, ve)
	}
	for _, c := range ve.Causes {
		acc = flattenCauses(c, acc)
	}
	return acc
}

// formatLocation turns the library's path segments into the dotted/bracket
// form a model is used to reading: ["todos","0","status"] -> todos[0].status.
func formatLocation(loc []string) string {
	if len(loc) == 0 {
		return "(root)"
	}
	var b strings.Builder
	for _, seg := range loc {
		if _, err := strconv.Atoi(seg); err == nil {
			b.WriteString("[" + seg + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(seg)
	}
	return b.String()
}

func dedupe(sorted []string) []string {
	if len(sorted) < 2 {
		return sorted
	}
	out := sorted[:1]
	for _, s := range sorted[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// ValidateArgs validates a call's arguments against the tool's schema. The
// validator is compiled once at Register; a Tool value that never went
// through Register (hand-built in a test, say) falls back to compiling on
// the fly so it still validates.
func ValidateArgs(tool Tool, args json.RawMessage) error {
	v := tool.validator
	if v == nil && len(bytes.TrimSpace(tool.Schema)) > 0 {
		compiled, err := CompileSchema(tool.Name, tool.Schema)
		if err != nil {
			return fmt.Errorf("%w for %q: %v", ErrInvalidArgs, tool.Name, err)
		}
		v = compiled
	}
	return v.Validate(tool.Name, args)
}
