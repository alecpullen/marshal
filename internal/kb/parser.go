package kb

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// ParsedFile contains the extracted symbols and imports from a file.
type ParsedFile struct {
	Symbols []Symbol
	Imports []Import
}

// LanguageParser is implemented for each supported language.
// New languages can be added by implementing this interface.
type LanguageParser interface {
	// Name returns the parser identifier (e.g., "go", "typescript").
	Name() string
	
	// FileExtensions returns the file extensions this parser handles.
	// Include the dot, e.g., [".go"], [".ts", ".tsx"]
	FileExtensions() []string
	
	// Parse extracts symbols and imports from the given content.
	// filename is provided for error reporting and context.
	Parse(content []byte, filename string) (*ParsedFile, error)
}

// ParserRegistry holds all registered language parsers.
// Thread-safe for reads after initialization.
type ParserRegistry struct {
	parsers   map[string]LanguageParser  // extension -> parser
	extToLang map[string]string            // extension -> language name
}

// NewParserRegistry creates a registry with all supported languages registered.
func NewParserRegistry() *ParserRegistry {
	r := &ParserRegistry{
		parsers:   make(map[string]LanguageParser),
		extToLang: make(map[string]string),
	}

	// Register supported languages
	// Extensible: add new languages here
	r.Register(&GoParser{})
	r.Register(&TypeScriptParser{})
	r.Register(&JavaScriptParser{})
	r.Register(&PythonParser{})
	// Future: r.Register(&RustParser{})
	// Future: r.Register(&JavaParser{})
	// Future: r.Register(&RubyParser{})

	return r
}

// Register adds a parser for its file extensions.
func (r *ParserRegistry) Register(p LanguageParser) {
	for _, ext := range p.FileExtensions() {
		ext := strings.ToLower(ext)
		r.parsers[ext] = p
		r.extToLang[ext] = p.Name()
	}
}

// GetParser returns the appropriate parser for a filename.
// Returns nil if no parser is registered for the file extension.
func (r *ParserRegistry) GetParser(filename string) LanguageParser {
	ext := strings.ToLower(filepath.Ext(filename))
	return r.parsers[ext]
}

// GetLanguage returns the language name for a filename.
func (r *ParserRegistry) GetLanguage(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	return r.extToLang[ext]
}

// SupportedExtensions returns all registered file extensions.
func (r *ParserRegistry) SupportedExtensions() []string {
	extensions := make([]string, 0, len(r.parsers))
	for ext := range r.parsers {
		extensions = append(extensions, ext)
	}
	return extensions
}

// GoParser implements LanguageParser for Go files.
type GoParser struct {
	parser *sitter.Parser
	query  *sitter.Query
}

// Name returns "go".
func (p *GoParser) Name() string { return "go" }

// FileExtensions returns [".go"].
func (p *GoParser) FileExtensions() []string { return []string{".go"} }

// lazyInit initializes the parser on first use.
func (p *GoParser) lazyInit() error {
	if p.parser != nil {
		return nil
	}
	
	parser := sitter.NewParser()
	parser.SetLanguage(golang.GetLanguage())
	
	query, err := sitter.NewQuery([]byte(goQuery), golang.GetLanguage())
	if err != nil {
		return fmt.Errorf("compiling Go query: %w", err)
	}
	
	p.parser = parser
	p.query = query
	return nil
}

// Parse extracts Go symbols and imports.
func (p *GoParser) Parse(content []byte, filename string) (*ParsedFile, error) {
	if err := p.lazyInit(); err != nil {
		return nil, err
	}
	
	tree, err := p.parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	defer tree.Close()

	root := tree.RootNode()
	
	var symbols []Symbol
	var imports []Import

	// Extract imports
	imports = p.extractImports(content, root)

	// Query for symbols
	cursor := sitter.NewQueryCursor()
	cursor.Exec(p.query, root)

	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		for _, capture := range match.Captures {
			capName := p.query.CaptureNameForId(capture.Index)
			node := capture.Node
			
			symbol := p.extractSymbol(node, capName, content, filename)
			if symbol != nil {
				symbols = append(symbols, *symbol)
			}
		}
	}

	return &ParsedFile{
		Symbols: symbols,
		Imports: imports,
	}, nil
}

// extractSymbol converts a tree-sitter capture to a Symbol.
func (p *GoParser) extractSymbol(node *sitter.Node, capName string, content []byte, filename string) *Symbol {
	// Skip if not a name capture
	if !strings.HasPrefix(capName, "name.") {
		return nil
	}

	name := node.Content(content)
	if name == "" {
		return nil
	}

	startPoint := node.StartPoint()
	endPoint := node.EndPoint()

	var kind SymbolKind
	isDefinition := strings.Contains(capName, ".definition.")
	
	// Determine kind from capture name
	switch {
	case strings.Contains(capName, ".function"):
		kind = SymbolFunction
	case strings.Contains(capName, ".method"):
		kind = SymbolMethod
	case strings.Contains(capName, ".type"):
		kind = SymbolType
	case strings.Contains(capName, ".interface"):
		kind = SymbolInterface
	case strings.Contains(capName, ".struct"):
		kind = SymbolStruct
	case strings.Contains(capName, ".variable"):
		kind = SymbolVariable
	case strings.Contains(capName, ".constant"):
		kind = SymbolConstant
	case strings.Contains(capName, ".field"):
		kind = SymbolField
	default:
		return nil
	}

	return &Symbol{
		Name:     name,
		Kind:     kind,
		Language: "go",
		Range: LineRange{
			StartLine: int(startPoint.Row) + 1,
			StartCol:  int(startPoint.Column),
			EndLine:   int(endPoint.Row) + 1,
			EndCol:    int(endPoint.Column),
		},
		Exported: isDefinition && isExportedGo(name),
		Metadata: map[string]interface{}{
			"is_definition": isDefinition,
		},
	}
}

// extractImports extracts Go import statements.
func (p *GoParser) extractImports(content []byte, root *sitter.Node) []Import {
	importQueryStr := `(import_spec path: (interpreted_string_literal) @import_path) (import_spec path: (raw_string_literal) @import_path)`
	
	importQuery, err := sitter.NewQuery([]byte(importQueryStr), golang.GetLanguage())
	if err != nil {
		return nil
	}

	cursor := sitter.NewQueryCursor()
	cursor.Exec(importQuery, root)

	var imports []Import
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}
		for _, capture := range match.Captures {
			path := strings.Trim(capture.Node.Content(content), "\"'`")
			line := int(capture.Node.StartPoint().Row) + 1
			
			imports = append(imports, Import{
				Path:    path,
				Line:    line,
				IsLocal: strings.HasPrefix(path, "."),
			})
		}
	}

	return imports
}

// isExportedGo checks if a Go identifier is exported (starts with uppercase).
func isExportedGo(name string) bool {
	if len(name) == 0 {
		return false
	}
	return name[0] >= 'A' && name[0] <= 'Z'
}

// TypeScriptParser implements LanguageParser for TypeScript files.
type TypeScriptParser struct {
	parser *sitter.Parser
	query  *sitter.Query
}

// Name returns "typescript".
func (p *TypeScriptParser) Name() string { return "typescript" }

// FileExtensions returns [".ts", ".tsx"].
func (p *TypeScriptParser) FileExtensions() []string { return []string{".ts", ".tsx"} }

// lazyInit initializes the parser on first use.
func (p *TypeScriptParser) lazyInit() error {
	if p.parser != nil {
		return nil
	}
	
	parser := sitter.NewParser()
	parser.SetLanguage(typescript.GetLanguage())
	
	query, err := sitter.NewQuery([]byte(tsQuery), typescript.GetLanguage())
	if err != nil {
		return fmt.Errorf("compiling TypeScript query: %w", err)
	}
	
	p.parser = parser
	p.query = query
	return nil
}

// Parse extracts TypeScript symbols and imports.
func (p *TypeScriptParser) Parse(content []byte, filename string) (*ParsedFile, error) {
	if err := p.lazyInit(); err != nil {
		return nil, err
	}
	
	tree, err := p.parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	defer tree.Close()

	root := tree.RootNode()
	
	var symbols []Symbol
	var imports []Import

	// Extract imports
	imports = p.extractImports(content, root)

	// Query for symbols
	cursor := sitter.NewQueryCursor()
	cursor.Exec(p.query, root)

	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		for _, capture := range match.Captures {
			capName := p.query.CaptureNameForId(capture.Index)
			node := capture.Node
			
			symbol := p.extractSymbol(node, capName, content, filename)
			if symbol != nil {
				symbols = append(symbols, *symbol)
			}
		}
	}

	return &ParsedFile{
		Symbols: symbols,
		Imports: imports,
	}, nil
}

// extractSymbol converts a tree-sitter capture to a Symbol.
func (p *TypeScriptParser) extractSymbol(node *sitter.Node, capName string, content []byte, filename string) *Symbol {
	if !strings.HasPrefix(capName, "name.") {
		return nil
	}

	name := node.Content(content)
	if name == "" {
		return nil
	}

	startPoint := node.StartPoint()
	endPoint := node.EndPoint()

	var kind SymbolKind
	isDefinition := strings.Contains(capName, ".definition.")
	
	switch {
	case strings.Contains(capName, ".function"):
		kind = SymbolFunction
	case strings.Contains(capName, ".method"):
		kind = SymbolMethod
	case strings.Contains(capName, ".class"):
		kind = SymbolType
	case strings.Contains(capName, ".interface"):
		kind = SymbolInterface
	case strings.Contains(capName, ".type"):
		kind = SymbolType
	case strings.Contains(capName, ".variable"):
		kind = SymbolVariable
	default:
		return nil
	}

	return &Symbol{
		Name:     name,
		Kind:     kind,
		Language: "typescript",
		Range: LineRange{
			StartLine: int(startPoint.Row) + 1,
			StartCol:  int(startPoint.Column),
			EndLine:   int(endPoint.Row) + 1,
			EndCol:    int(endPoint.Column),
		},
		Exported: isDefinition, // Assume exported if defined
		Metadata: map[string]interface{}{
			"is_definition": isDefinition,
		},
	}
}

// extractImports extracts TS import statements.
func (p *TypeScriptParser) extractImports(content []byte, root *sitter.Node) []Import {
	importQueryStr := `(import_statement source: (string) @import_path) (import_require_clause source: (string) @import_path)`
	
	importQuery, err := sitter.NewQuery([]byte(importQueryStr), typescript.GetLanguage())
	if err != nil {
		return nil
	}

	cursor := sitter.NewQueryCursor()
	cursor.Exec(importQuery, root)

	var imports []Import
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}
		for _, capture := range match.Captures {
			path := strings.Trim(capture.Node.Content(content), "\"'`")
			line := int(capture.Node.StartPoint().Row) + 1
			
			imports = append(imports, Import{
				Path:    path,
				Line:    line,
				IsLocal: strings.HasPrefix(path, ".") || strings.HasPrefix(path, "/"),
			})
		}
	}

	return imports
}

// JavaScriptParser implements LanguageParser for JavaScript files.
type JavaScriptParser struct {
	parser *sitter.Parser
	query  *sitter.Query
}

// Name returns "javascript".
func (p *JavaScriptParser) Name() string { return "javascript" }

// FileExtensions returns [".js", ".jsx"].
func (p *JavaScriptParser) FileExtensions() []string { return []string{".js", ".jsx"} }

// lazyInit initializes the parser on first use.
func (p *JavaScriptParser) lazyInit() error {
	if p.parser != nil {
		return nil
	}
	
	parser := sitter.NewParser()
	parser.SetLanguage(javascript.GetLanguage())
	
	query, err := sitter.NewQuery([]byte(jsQuery), javascript.GetLanguage())
	if err != nil {
		return fmt.Errorf("compiling JavaScript query: %w", err)
	}
	
	p.parser = parser
	p.query = query
	return nil
}

// Parse extracts JavaScript symbols and imports.
func (p *JavaScriptParser) Parse(content []byte, filename string) (*ParsedFile, error) {
	if err := p.lazyInit(); err != nil {
		return nil, err
	}
	
	tree, err := p.parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	defer tree.Close()

	root := tree.RootNode()
	
	var symbols []Symbol
	var imports []Import

	// Extract imports
	imports = p.extractImports(content, root)

	// Query for symbols
	cursor := sitter.NewQueryCursor()
	cursor.Exec(p.query, root)

	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		for _, capture := range match.Captures {
			capName := p.query.CaptureNameForId(capture.Index)
			node := capture.Node
			
			symbol := p.extractSymbol(node, capName, content, filename)
			if symbol != nil {
				symbols = append(symbols, *symbol)
			}
		}
	}

	return &ParsedFile{
		Symbols: symbols,
		Imports: imports,
	}, nil
}

// extractSymbol converts a tree-sitter capture to a Symbol.
func (p *JavaScriptParser) extractSymbol(node *sitter.Node, capName string, content []byte, filename string) *Symbol {
	if !strings.HasPrefix(capName, "name.") {
		return nil
	}

	name := node.Content(content)
	if name == "" {
		return nil
	}

	startPoint := node.StartPoint()
	endPoint := node.EndPoint()

	var kind SymbolKind
	isDefinition := strings.Contains(capName, ".definition.")
	
	switch {
	case strings.Contains(capName, ".function"):
		kind = SymbolFunction
	case strings.Contains(capName, ".method"):
		kind = SymbolMethod
	case strings.Contains(capName, ".class"):
		kind = SymbolType
	case strings.Contains(capName, ".variable"):
		kind = SymbolVariable
	default:
		return nil
	}

	return &Symbol{
		Name:     name,
		Kind:     kind,
		Language: "javascript",
		Range: LineRange{
			StartLine: int(startPoint.Row) + 1,
			StartCol:  int(startPoint.Column),
			EndLine:   int(endPoint.Row) + 1,
			EndCol:    int(endPoint.Column),
		},
		Exported: isDefinition,
		Metadata: map[string]interface{}{
			"is_definition": isDefinition,
		},
	}
}

// extractImports extracts JS import statements.
func (p *JavaScriptParser) extractImports(content []byte, root *sitter.Node) []Import {
	importQueryStr := `(import_statement source: (string) @import_path) (import_require_clause source: (string) @import_path)`
	
	importQuery, err := sitter.NewQuery([]byte(importQueryStr), javascript.GetLanguage())
	if err != nil {
		return nil
	}

	cursor := sitter.NewQueryCursor()
	cursor.Exec(importQuery, root)

	var imports []Import
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}
		for _, capture := range match.Captures {
			path := strings.Trim(capture.Node.Content(content), "\"'`")
			line := int(capture.Node.StartPoint().Row) + 1
			
			imports = append(imports, Import{
				Path:    path,
				Line:    line,
				IsLocal: strings.HasPrefix(path, ".") || strings.HasPrefix(path, "/"),
			})
		}
	}

	return imports
}

// PythonParser implements LanguageParser for Python files.
type PythonParser struct {
	parser *sitter.Parser
	query  *sitter.Query
}

// Name returns "python".
func (p *PythonParser) Name() string { return "python" }

// FileExtensions returns [".py"].
func (p *PythonParser) FileExtensions() []string { return []string{".py"} }

// lazyInit initializes the parser on first use.
func (p *PythonParser) lazyInit() error {
	if p.parser != nil {
		return nil
	}
	
	parser := sitter.NewParser()
	parser.SetLanguage(python.GetLanguage())
	
	query, err := sitter.NewQuery([]byte(pythonQuery), python.GetLanguage())
	if err != nil {
		return fmt.Errorf("compiling Python query: %w", err)
	}
	
	p.parser = parser
	p.query = query
	return nil
}

// Parse extracts Python symbols and imports.
func (p *PythonParser) Parse(content []byte, filename string) (*ParsedFile, error) {
	if err := p.lazyInit(); err != nil {
		return nil, err
	}
	
	tree, err := p.parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	defer tree.Close()

	root := tree.RootNode()
	
	var symbols []Symbol
	var imports []Import

	// Extract imports
	imports = p.extractImports(content, root)

	// Query for symbols
	cursor := sitter.NewQueryCursor()
	cursor.Exec(p.query, root)

	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		for _, capture := range match.Captures {
			capName := p.query.CaptureNameForId(capture.Index)
			node := capture.Node
			
			symbol := p.extractSymbol(node, capName, content, filename)
			if symbol != nil {
				symbols = append(symbols, *symbol)
			}
		}
	}

	return &ParsedFile{
		Symbols: symbols,
		Imports: imports,
	}, nil
}

// extractSymbol converts a tree-sitter capture to a Symbol.
func (p *PythonParser) extractSymbol(node *sitter.Node, capName string, content []byte, filename string) *Symbol {
	if !strings.HasPrefix(capName, "name.") {
		return nil
	}

	name := node.Content(content)
	if name == "" {
		return nil
	}

	startPoint := node.StartPoint()
	endPoint := node.EndPoint()

	var kind SymbolKind
	isDefinition := strings.Contains(capName, ".definition.")
	
	switch {
	case strings.Contains(capName, ".function"):
		kind = SymbolFunction
	case strings.Contains(capName, ".class"):
		kind = SymbolType
	case strings.Contains(capName, ".variable"):
		kind = SymbolVariable
	default:
		return nil
	}

	return &Symbol{
		Name:     name,
		Kind:     kind,
		Language: "python",
		Range: LineRange{
			StartLine: int(startPoint.Row) + 1,
			StartCol:  int(startPoint.Column),
			EndLine:   int(endPoint.Row) + 1,
			EndCol:    int(endPoint.Column),
		},
		Exported: !strings.HasPrefix(name, "_"), // Python convention
		Metadata: map[string]interface{}{
			"is_definition": isDefinition,
		},
	}
}

// extractImports extracts Python import statements.
func (p *PythonParser) extractImports(content []byte, root *sitter.Node) []Import {
	importQueryStr := `(import_statement name: (dotted_name) @import_name) (import_from_statement module_name: (dotted_name) @import_name)`
	
	importQuery, err := sitter.NewQuery([]byte(importQueryStr), python.GetLanguage())
	if err != nil {
		return nil
	}

	cursor := sitter.NewQueryCursor()
	cursor.Exec(importQuery, root)

	var imports []Import
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}
		for _, capture := range match.Captures {
			path := capture.Node.Content(content)
			line := int(capture.Node.StartPoint().Row) + 1
			
			imports = append(imports, Import{
				Path:    path,
				Line:    line,
				IsLocal: false, // Python imports are typically not local paths
			})
		}
	}

	return imports
}

// Tree-sitter query sources
// These extract definitions and references with capture names following the pattern:
//   name.definition.<kind> for definitions
//   name.reference.<kind> for references

const goQuery = `
(function_declaration name: (identifier) @name.definition.function) @definition.function
(method_declaration name: (field_identifier) @name.definition.method) @definition.method
(type_declaration (type_spec name: (type_identifier) @name.definition.type)) @definition.type
(type_declaration (type_spec name: (type_identifier) @name.definition.interface type: (interface_type))) @definition.interface
(type_declaration (type_spec name: (type_identifier) @name.definition.struct type: (struct_type))) @definition.struct
(var_spec name: (identifier) @name.definition.variable) @definition.variable
(const_spec name: (identifier) @name.definition.constant) @definition.constant
(field_declaration name: (field_identifier) @name.definition.field) @definition.field
(call_expression function: (identifier) @name.reference.call) @reference.call
(call_expression function: (selector_expression field: (field_identifier) @name.reference.call)) @reference.call
`

const tsQuery = `
(function_declaration name: (identifier) @name.definition.function) @definition.function
(method_definition name: (property_identifier) @name.definition.method) @definition.method
(class_declaration name: (type_identifier) @name.definition.class) @definition.class
(interface_declaration name: (type_identifier) @name.definition.interface) @definition.interface
(type_alias_declaration name: (type_identifier) @name.definition.type) @definition.type
(variable_declarator name: (identifier) @name.definition.variable) @definition.variable
(call_expression function: (identifier) @name.reference.call) @reference.call
(call_expression function: (member_expression property: (property_identifier) @name.reference.call)) @reference.call
`

const jsQuery = `
(function_declaration name: (identifier) @name.definition.function) @definition.function
(method_definition name: (property_identifier) @name.definition.method) @definition.method
(class_declaration name: (identifier) @name.definition.class) @definition.class
(variable_declarator name: (identifier) @name.definition.variable) @definition.variable
(call_expression function: (identifier) @name.reference.call) @reference.call
(call_expression function: (member_expression property: (property_identifier) @name.reference.call)) @reference.call
`

const pythonQuery = `
(function_definition name: (identifier) @name.definition.function) @definition.function
(class_definition name: (identifier) @name.definition.class) @definition.class
(assignment left: (identifier) @name.definition.variable) @definition.variable
(call function: (identifier) @name.reference.call) @reference.call
(call function: (attribute attribute: (identifier) @name.reference.call)) @reference.call
`