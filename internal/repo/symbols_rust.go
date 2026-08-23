package repo

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"marshal/internal/db"
)

// extractRustDeclaration walks one top-level Rust node. impl_item bodies are
// descended for methods; trait signatures (function_signature_item) and
// mod_item are skipped.
func extractRustDeclaration(path string, node *sitter.Node, source []byte) []db.Symbol {
	switch node.Type() {
	case "function_item":
		return []db.Symbol{funcSymbol(path, node, source, "function", "")}
	case "struct_item", "enum_item", "trait_item", "type_item":
		return []db.Symbol{funcSymbol(path, node, source, "type", "")}
	case "impl_item":
		return rustImplMethods(path, node, source)
	case "use_declaration":
		return []db.Symbol{rustUseSymbol(path, node, source)}
	default:
		return nil
	}
}

// rustImplMethods extracts function_items from an impl body, stamped with
// the impl's type text as Receiver.
func rustImplMethods(path string, node *sitter.Node, source []byte) []db.Symbol {
	receiver := ""
	if t := node.ChildByFieldName("type"); t != nil {
		receiver = t.Content(source)
	}
	var out []db.Symbol
	for i := 0; i < int(node.NamedChildCount()); i++ {
		body := node.NamedChild(i)
		if body.Type() != "declaration_list" {
			continue
		}
		for j := 0; j < int(body.NamedChildCount()); j++ {
			fn := body.NamedChild(j)
			if fn.Type() == "function_item" {
				out = append(out, funcSymbol(path, fn, source, "method", receiver))
			}
		}
	}
	return out
}

// rustUseSymbol extracts a use declaration: the name is the used path (the
// declaration text minus the `use ` prefix and `;` suffix); the signature is
// the full text.
func rustUseSymbol(path string, node *sitter.Node, source []byte) db.Symbol {
	text := strings.TrimSpace(node.Content(source))
	name := strings.TrimSuffix(strings.TrimPrefix(text, "use "), ";")
	return db.Symbol{
		FilePath:  path,
		Kind:      "import",
		Name:      name,
		Signature: text,
		LineStart: int(node.StartPoint().Row) + 1,
		LineEnd:   int(node.EndPoint().Row) + 1,
	}
}
