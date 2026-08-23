package repo

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"marshal/internal/db"
)

// extractTSDeclaration walks one top-level TypeScript/TSX/JavaScript node.
// export_statement is descended so `export function …` yields symbols;
// lexical_declaration is searched for arrow/function-expression assignments
// (`const f = (…) => …`).
func extractTSDeclaration(path string, node *sitter.Node, source []byte) []db.Symbol {
	switch node.Type() {
	case "function_declaration", "generator_function_declaration":
		return []db.Symbol{funcSymbol(path, node, source, "function", "")}
	case "class_declaration", "abstract_class_declaration", "interface_declaration", "enum_declaration", "type_alias_declaration":
		return append([]db.Symbol{funcSymbol(path, node, source, "type", "")}, tsClassMethods(path, node, source)...)
	case "lexical_declaration":
		return tsArrowFunctions(path, node, source)
	case "import_statement":
		return []db.Symbol{tsImportSymbol(path, node, source)}
	case "export_statement":
		var out []db.Symbol
		for i := 0; i < int(node.NamedChildCount()); i++ {
			out = append(out, extractTSDeclaration(path, node.NamedChild(i), source)...)
		}
		return out
	default:
		return nil
	}
}

// tsClassMethods extracts method_definitions from a class or abstract class
// body, stamped with the class name as Receiver. (Interfaces and enums have
// no method bodies worth indexing; their members are skipped.)
func tsClassMethods(path string, node *sitter.Node, source []byte) []db.Symbol {
	if node.Type() != "class_declaration" && node.Type() != "abstract_class_declaration" {
		return nil
	}
	className := ""
	if n := node.ChildByFieldName("name"); n != nil {
		className = n.Content(source)
	}
	var out []db.Symbol
	for i := 0; i < int(node.NamedChildCount()); i++ {
		body := node.NamedChild(i)
		if body.Type() != "class_body" {
			continue
		}
		for j := 0; j < int(body.NamedChildCount()); j++ {
			m := body.NamedChild(j)
			if m.Type() == "method_definition" {
				out = append(out, funcSymbol(path, m, source, "method", className))
			}
		}
	}
	return out
}

// tsArrowFunctions extracts `const f = (…) => …` / `const f = function…`
// declarations as function symbols, named after the declarator's identifier.
// The signature is the full declarator text (it has no body container).
func tsArrowFunctions(path string, node *sitter.Node, source []byte) []db.Symbol {
	var out []db.Symbol
	for i := 0; i < int(node.NamedChildCount()); i++ {
		decl := node.NamedChild(i)
		if decl.Type() != "variable_declarator" {
			continue
		}
		nameNode := decl.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		for j := 0; j < int(decl.NamedChildCount()); j++ {
			v := decl.NamedChild(j)
			if v.Type() != "arrow_function" && v.Type() != "function_expression" {
				continue
			}
			out = append(out, db.Symbol{
				FilePath:  path,
				Kind:      "function",
				Name:      nameNode.Content(source),
				Signature: headerSignature(decl, source),
				LineStart: int(decl.StartPoint().Row) + 1,
				LineEnd:   int(decl.EndPoint().Row) + 1,
			})
		}
	}
	return out
}

// tsImportSymbol extracts an import statement: the name is the module
// string's content with quotes stripped (the vendored grammars leave the
// source string an unlabeled `string` child); the signature is the
// statement's first line.
func tsImportSymbol(path string, node *sitter.Node, source []byte) db.Symbol {
	name := ""
	for i := 0; i < int(node.NamedChildCount()); i++ {
		if child := node.NamedChild(i); child.Type() == "string" {
			name = strings.Trim(child.Content(source), `'"`)
		}
	}
	return db.Symbol{
		FilePath:  path,
		Kind:      "import",
		Name:      name,
		Signature: strings.SplitN(strings.TrimSpace(node.Content(source)), "\n", 2)[0],
		LineStart: int(node.StartPoint().Row) + 1,
		LineEnd:   int(node.EndPoint().Row) + 1,
	}
}
