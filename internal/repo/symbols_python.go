package repo

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"marshal/internal/db"
)

// extractPyDeclaration walks one top-level Python node. decorated_definition
// is descended so @decorator-wrapped functions and classes still yield
// symbols. Nested functions (inside a function body) are never reached —
// only root children are walked.
func extractPyDeclaration(path string, node *sitter.Node, source []byte) []db.Symbol {
	switch node.Type() {
	case "function_definition":
		return []db.Symbol{funcSymbol(path, node, source, "function", "")}
	case "class_definition":
		return append([]db.Symbol{funcSymbol(path, node, source, "type", "")}, pyClassMethods(path, node, source)...)
	case "import_statement", "import_from_statement":
		return []db.Symbol{pyImportSymbol(path, node, source)}
	case "decorated_definition":
		var out []db.Symbol
		for i := 0; i < int(node.NamedChildCount()); i++ {
			out = append(out, extractPyDeclaration(path, node.NamedChild(i), source)...)
		}
		return out
	default:
		return nil
	}
}

// pyClassMethods extracts function_definitions from a class body, stamped
// with the class name as Receiver.
func pyClassMethods(path string, node *sitter.Node, source []byte) []db.Symbol {
	className := ""
	if n := node.ChildByFieldName("name"); n != nil {
		className = n.Content(source)
	}
	var out []db.Symbol
	for i := 0; i < int(node.NamedChildCount()); i++ {
		body := node.NamedChild(i)
		if body.Type() != "block" {
			continue
		}
		for j := 0; j < int(body.NamedChildCount()); j++ {
			fn := body.NamedChild(j)
			if fn.Type() == "function_definition" {
				out = append(out, funcSymbol(path, fn, source, "method", className))
			}
			// Decorated methods unwrap one level, matching the top level.
			if fn.Type() == "decorated_definition" {
				for k := 0; k < int(fn.NamedChildCount()); k++ {
					if inner := fn.NamedChild(k); inner.Type() == "function_definition" {
						out = append(out, funcSymbol(path, inner, source, "method", className))
					}
				}
			}
		}
	}
	return out
}

// pyImportSymbol extracts an import: for `import x` the name is the first
// dotted_name child; for `from x import y` it is the module_name field. The
// signature is the full statement text.
func pyImportSymbol(path string, node *sitter.Node, source []byte) db.Symbol {
	name := ""
	if node.Type() == "import_from_statement" {
		if m := node.ChildByFieldName("module_name"); m != nil {
			name = m.Content(source)
		}
	} else {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			if child := node.NamedChild(i); child.Type() == "dotted_name" {
				name = child.Content(source)
				break
			}
		}
	}
	return db.Symbol{
		FilePath:  path,
		Kind:      "import",
		Name:      name,
		Signature: strings.TrimSpace(node.Content(source)),
		LineStart: int(node.StartPoint().Row) + 1,
		LineEnd:   int(node.EndPoint().Row) + 1,
	}
}
