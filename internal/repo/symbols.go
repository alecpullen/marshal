package repo

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"

	"marshal/internal/db"
)

// ExtractSymbols parses Go source with tree-sitter and returns the
// functions, methods, types, and imports it finds. Tree-sitter produces a
// partial tree around syntax errors, so a malformed region of the file
// does not prevent extraction of symbols from the rest of it.
func ExtractSymbols(path string, source []byte) ([]db.Symbol, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(golang.GetLanguage())

	tree, err := parser.ParseCtx(context.Background(), nil, source)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	root := tree.RootNode()
	var symbols []db.Symbol
	for i := 0; i < int(root.NamedChildCount()); i++ {
		symbols = append(symbols, extractDeclaration(path, root.NamedChild(i), source)...)
	}
	return symbols, nil
}

func extractDeclaration(path string, node *sitter.Node, source []byte) []db.Symbol {
	switch node.Type() {
	case "function_declaration":
		return []db.Symbol{funcSymbol(path, node, source, "function", "")}
	case "method_declaration":
		return []db.Symbol{funcSymbol(path, node, source, "method", receiverType(node, source))}
	case "type_declaration":
		return typeSymbols(path, node, source)
	default:
		return nil
	}
}

func funcSymbol(path string, node *sitter.Node, source []byte, kind, receiver string) db.Symbol {
	name := ""
	if nameNode := node.ChildByFieldName("name"); nameNode != nil {
		name = nameNode.Content(source)
	}
	return db.Symbol{
		FilePath:  path,
		Kind:      kind,
		Name:      name,
		Receiver:  receiver,
		Signature: headerSignature(node, source),
		LineStart: int(node.StartPoint().Row) + 1,
		LineEnd:   int(node.EndPoint().Row) + 1,
	}
}

// headerSignature returns the declaration text up to (but not including)
// its body, or the full declaration text if it has no body.
func headerSignature(node *sitter.Node, source []byte) string {
	end := node.EndByte()
	if body := node.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	}
	return strings.TrimSpace(string(source[node.StartByte():end]))
}

// receiverType returns a method's receiver type text (e.g. "*Scanner"), or
// "" if node has no receiver field or the receiver has no type.
func receiverType(node *sitter.Node, source []byte) string {
	receiver := node.ChildByFieldName("receiver")
	if receiver == nil || receiver.NamedChildCount() == 0 {
		return ""
	}
	param := receiver.NamedChild(0)
	typeNode := param.ChildByFieldName("type")
	if typeNode == nil {
		return ""
	}
	return typeNode.Content(source)
}

func typeSymbols(path string, node *sitter.Node, source []byte) []db.Symbol {
	var symbols []db.Symbol
	for i := 0; i < int(node.NamedChildCount()); i++ {
		spec := node.NamedChild(i)
		if spec.Type() != "type_spec" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		typeNode := spec.ChildByFieldName("type")
		if nameNode == nil || typeNode == nil {
			continue
		}
		name := nameNode.Content(source)
		symbols = append(symbols, db.Symbol{
			FilePath:  path,
			Kind:      "type",
			Name:      name,
			Signature: "type " + name + " " + typeKindWord(typeNode, source),
			LineStart: int(spec.StartPoint().Row) + 1,
			LineEnd:   int(spec.EndPoint().Row) + 1,
		})
	}
	return symbols
}

// typeKindWord summarizes a type_spec's type node as a short trailing word
// for its signature: the underlying type text for simple aliases (e.g.
// "int"), or the composite keyword ("struct"/"interface"/...) with its
// opening brace stripped for struct/interface/composite types.
func typeKindWord(typeNode *sitter.Node, source []byte) string {
	text := typeNode.Content(source)
	line := strings.SplitN(text, "\n", 2)[0]
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "{"))
}
