package repomap

import (
	"context"
	"os"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// tagger holds tree-sitter parsers and compiled queries per language.
type tagger struct {
	parsers   map[string]*sitter.Parser
	queries   map[string]*sitter.Query
}

func newTagger() *tagger {
	t := &tagger{
		parsers: make(map[string]*sitter.Parser),
		queries: make(map[string]*sitter.Query),
	}

	langs := map[string]struct {
		lang  *sitter.Language
		query string
	}{
		"go":         {golang.GetLanguage(), goQuery},
		"python":     {python.GetLanguage(), pythonQuery},
		"javascript": {javascript.GetLanguage(), jsQuery},
		"typescript": {typescript.GetLanguage(), tsQuery},
	}

	for name, l := range langs {
		p := sitter.NewParser()
		p.SetLanguage(l.lang)
		t.parsers[name] = p

		q, err := sitter.NewQuery([]byte(l.query), l.lang)
		if err != nil {
			// Skip languages whose queries fail to compile.
			continue
		}
		t.queries[name] = q
	}

	return t
}

// extractFile parses the file at absPath and returns its tags.
// relPath is stored on each returned Tag.
func (t *tagger) extractFile(absPath, relPath string) ([]Tag, error) {
	lang := langFor(relPath)
	if lang == "" {
		return nil, nil
	}

	parser := t.parsers[lang]
	query := t.queries[lang]
	if parser == nil || query == nil {
		return nil, nil
	}

	src, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	if root.HasError() {
		// Parse errors are common; continue anyway — we get partial tags.
	}

	cursor := sitter.NewQueryCursor()
	cursor.Exec(query, root)

	var tags []Tag
	for {
		m, ok := cursor.NextMatch()
		if !ok {
			break
		}
		for _, c := range m.Captures {
			capName := query.CaptureNameForId(c.Index)
			if !strings.HasPrefix(capName, "name.") {
				continue
			}
			symName := c.Node.Content(src)
			if symName == "" {
				continue
			}
			line := int(c.Node.StartPoint().Row) + 1

			var kind TagKind
			if strings.Contains(capName, ".definition.") {
				kind = TagDef
			} else if strings.Contains(capName, ".reference.") {
				kind = TagRef
			} else {
				continue
			}

			tags = append(tags, Tag{
				RelPath: relPath,
				Name:    symName,
				Kind:    kind,
				Line:    line,
			})
		}
	}

	return tags, nil
}

// Embedded query sources (matches queries/*.scm).  These are kept as Go
// constants rather than //go:embed so the package has no dependency on the
// filesystem at runtime.
const goQuery = `
(function_declaration
  name: (identifier) @name.definition.function) @definition.function

(method_declaration
  name: (field_identifier) @name.definition.method) @definition.method

(type_declaration
  (type_spec
    name: (type_identifier) @name.definition.type)) @definition.type

(call_expression
  function: (identifier) @name.reference.call) @reference.call

(call_expression
  function: (selector_expression
    field: (field_identifier) @name.reference.call)) @reference.call
`

const pythonQuery = `
(function_definition
  name: (identifier) @name.definition.function) @definition.function

(class_definition
  name: (identifier) @name.definition.class) @definition.class

(call
  function: (identifier) @name.reference.call) @reference.call

(call
  function: (attribute
    attribute: (identifier) @name.reference.call)) @reference.call
`

const jsQuery = `
(function_declaration
  name: (identifier) @name.definition.function) @definition.function

(method_definition
  name: (property_identifier) @name.definition.method) @definition.method

(class_declaration
  name: (identifier) @name.definition.class) @definition.class

(call_expression
  function: (identifier) @name.reference.call) @reference.call

(call_expression
  function: (member_expression
    property: (property_identifier) @name.reference.call)) @reference.call
`

const tsQuery = `
(function_declaration
  name: (identifier) @name.definition.function) @definition.function

(method_definition
  name: (property_identifier) @name.definition.method) @definition.method

(class_declaration
  name: (type_identifier) @name.definition.class) @definition.class

(interface_declaration
  name: (type_identifier) @name.definition.interface) @definition.interface

(type_alias_declaration
  name: (type_identifier) @name.definition.type) @definition.type

(call_expression
  function: (identifier) @name.reference.call) @reference.call

(call_expression
  function: (member_expression
    property: (property_identifier) @name.reference.call)) @reference.call
`
