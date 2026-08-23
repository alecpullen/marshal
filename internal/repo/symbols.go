package repo

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"

	"marshal/internal/db"
)

// supportedLanguages is the set ExtractSymbols can parse. DetectLanguage
// (language.go) maps .tsx files to "typescript"; grammar selection
// special-cases the .tsx suffix inside languageFor.
var supportedLanguages = map[string]bool{
	"go": true, "javascript": true, "typescript": true, "python": true, "rust": true,
}

// SupportedLanguage reports whether tree-sitter symbol extraction is
// available for lang. The index gate uses it to skip unsupported files.
func SupportedLanguage(lang string) bool {
	return supportedLanguages[lang]
}

// languageFor returns the grammar for lang, or nil when unsupported. A
// "typescript" file whose path ends in .tsx gets the tsx grammar, which
// tolerates JSX syntax.
func languageFor(lang, path string) *sitter.Language {
	switch lang {
	case "go":
		return golang.GetLanguage()
	case "typescript":
		if strings.HasSuffix(path, ".tsx") {
			return tsx.GetLanguage()
		}
		return typescript.GetLanguage()
	case "javascript":
		return javascript.GetLanguage()
	case "python":
		return python.GetLanguage()
	case "rust":
		return rust.GetLanguage()
	default:
		return nil
	}
}

// extractorFor returns the per-language declaration walker.
func extractorFor(lang string) func(path string, node *sitter.Node, source []byte) []db.Symbol {
	switch lang {
	case "go":
		return extractDeclaration
	case "typescript", "javascript":
		return extractTSDeclaration
	case "python":
		return extractPyDeclaration
	case "rust":
		return extractRustDeclaration
	default:
		return nil
	}
}

// ExtractSymbols parses source with the tree-sitter grammar for lang and
// returns the functions, methods, types, and imports it finds. Unsupported
// languages return nil, nil. Tree-sitter produces a partial tree around
// syntax errors, so a malformed region of the file does not prevent
// extraction of symbols from the rest of it.
func ExtractSymbols(ctx context.Context, lang, path string, source []byte) ([]db.Symbol, error) {
	language := languageFor(lang, path)
	extract := extractorFor(lang)
	if language == nil || extract == nil {
		return nil, nil
	}
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(language)

	tree, err := parser.ParseCtx(ctx, nil, source)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	root := tree.RootNode()
	var symbols []db.Symbol
	for i := 0; i < int(root.NamedChildCount()); i++ {
		symbols = append(symbols, extract(path, root.NamedChild(i), source)...)
	}
	return symbols, nil
}

// Stub replaced by symbols_rust.go in the follow-up task.
func extractRustDeclaration(path string, node *sitter.Node, source []byte) []db.Symbol {
	return nil
}

func extractDeclaration(path string, node *sitter.Node, source []byte) []db.Symbol {
	switch node.Type() {
	case "function_declaration":
		return []db.Symbol{funcSymbol(path, node, source, "function", "")}
	case "method_declaration":
		return []db.Symbol{funcSymbol(path, node, source, "method", receiverType(node, source))}
	case "type_declaration":
		return typeSymbols(path, node, source)
	case "import_declaration":
		return importSymbols(path, node, source)
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
// its body, or the full declaration text if it has no body. The Go grammar
// fields the body; the vendored TS/Python/Rust grammars leave body
// containers unlabeled, so fall back to the first named child of a known
// body-container type.
func headerSignature(node *sitter.Node, source []byte) string {
	end := node.EndByte()
	if body := node.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	} else if body := firstNamedChildOfTypes(node, bodyContainerTypes); body != nil {
		end = body.StartByte()
	}
	return strings.TrimSpace(string(source[node.StartByte():end]))
}

// bodyContainerTypes are the unlabeled node types that hold a declaration's
// body across the vendored grammars (TS/JS statement_block/class_body/
// interface_body/enum_body, Python block, Rust block/declaration_list/
// field_declaration_list/enum_variant_list).
var bodyContainerTypes = []string{
	"statement_block", "block", "class_body", "interface_body", "enum_body",
	"declaration_list", "field_declaration_list", "enum_variant_list",
}

func firstNamedChildOfTypes(node *sitter.Node, types []string) *sitter.Node {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		for _, t := range types {
			if child.Type() == t {
				return child
			}
		}
	}
	return nil
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
		if spec.Type() != "type_spec" && spec.Type() != "type_alias" {
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

func importSymbols(path string, node *sitter.Node, source []byte) []db.Symbol {
	var specs []*sitter.Node
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "import_spec":
			specs = append(specs, child)
		case "import_spec_list":
			for j := 0; j < int(child.NamedChildCount()); j++ {
				if sub := child.NamedChild(j); sub.Type() == "import_spec" {
					specs = append(specs, sub)
				}
			}
		}
	}

	var symbols []db.Symbol
	for _, spec := range specs {
		pathNode := spec.ChildByFieldName("path")
		if pathNode == nil {
			continue
		}
		importPath := strings.Trim(pathNode.Content(source), `"`)
		signature := pathNode.Content(source)
		if aliasNode := spec.ChildByFieldName("name"); aliasNode != nil {
			signature = aliasNode.Content(source) + " " + signature
		}
		symbols = append(symbols, db.Symbol{
			FilePath:  path,
			Kind:      "import",
			Name:      importPath,
			Signature: signature,
			LineStart: int(spec.StartPoint().Row) + 1,
			LineEnd:   int(spec.EndPoint().Row) + 1,
		})
	}
	return symbols
}
