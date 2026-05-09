package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/alecpullen/marshal/internal/agent/tools"
)

// SymbolLookupTool implements the kb_symbol_lookup tool.
// Finds symbol definitions by name with optional path scoping.
type SymbolLookupTool struct {
	query *Query
}

// NewSymbolLookupTool creates a new symbol lookup tool.
func NewSymbolLookupTool(query *Query) *SymbolLookupTool {
	return &SymbolLookupTool{query: query}
}

// Name returns the tool name.
func (t *SymbolLookupTool) Name() string {
	return "kb_symbol_lookup"
}

// Description returns the tool description.
func (t *SymbolLookupTool) Description() string {
	return "Find symbol definition by name. Returns file path, line number, and symbol details. " +
		"Optional path_hint scopes search to a specific file or directory."
}

// IsReadOperation returns true (this is a read-only operation).
func (t *SymbolLookupTool) IsReadOperation() bool {
	return true
}

// IsMutating returns false (does not modify state).
func (t *SymbolLookupTool) IsMutating() bool {
	return false
}

// RequiresReadBeforeEdit returns false.
func (t *SymbolLookupTool) RequiresReadBeforeEdit() bool {
	return false
}

// Schema returns the JSON schema for the tool.
func (t *SymbolLookupTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "Symbol name to look up (e.g., 'Middleware', 'parseFile', 'MyClass')"
			},
			"path_hint": {
				"type": "string",
				"description": "Optional file or directory path to scope the search (e.g., './internal/kb' or 'parser.go')"
			}
		},
		"required": ["name"]
	}`)
}

// Invoke executes the tool.
func (t *SymbolLookupTool) Invoke(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	var params struct {
		Name     string `json:"name"`
		PathHint string `json:"path_hint"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	results, err := t.query.Lookup(params.Name, params.PathHint)
	if err != nil {
		return nil, fmt.Errorf("lookup failed: %w", err)
	}

	if len(results) == 0 {
		return &tools.Result{
			Content: fmt.Sprintf("No symbol named '%s' found", params.Name),
		}, nil
	}

	// Format results
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d symbol(s) named '%s':\n\n", len(results), params.Name))

	for i, result := range results {
		sym := result.Symbol
		sb.WriteString(fmt.Sprintf("%d. **%s** (%s)\n", i+1, sym.Name, sym.Kind))
		sb.WriteString(fmt.Sprintf("   File: `%s`\n", result.FilePath))
		sb.WriteString(fmt.Sprintf("   Line: %d\n", sym.Range.StartLine))
		if sym.Signature != "" {
			sb.WriteString(fmt.Sprintf("   Signature: `%s`\n", sym.Signature))
		}
		if sym.Parent != "" {
			sb.WriteString(fmt.Sprintf("   Parent: %s\n", sym.Parent))
		}
		if sym.Exported {
			sb.WriteString("   Exported: yes\n")
		}
		if result.Score < 1.0 {
			sb.WriteString(fmt.Sprintf("   Relevance: %.0f%%\n", result.Score*100))
		}
		sb.WriteString("\n")
	}

	return &tools.Result{
		Content: sb.String(),
	}, nil
}

// SymbolReferencesTool implements the kb_symbol_references tool.
// Finds all references to a symbol (best-effort).
type SymbolReferencesTool struct {
	query *Query
}

// NewSymbolReferencesTool creates a new symbol references tool.
func NewSymbolReferencesTool(query *Query) *SymbolReferencesTool {
	return &SymbolReferencesTool{query: query}
}

// Name returns the tool name.
func (t *SymbolReferencesTool) Name() string {
	return "kb_symbol_references"
}

// Description returns the tool description.
func (t *SymbolReferencesTool) Description() string {
	return "Find all references to a symbol. Best-effort: may miss dynamic references. " +
		"Requires the file where the symbol is defined."
}

// IsReadOperation returns true.
func (t *SymbolReferencesTool) IsReadOperation() bool {
	return true
}

// IsMutating returns false.
func (t *SymbolReferencesTool) IsMutating() bool {
	return false
}

// RequiresReadBeforeEdit returns false.
func (t *SymbolReferencesTool) RequiresReadBeforeEdit() bool {
	return false
}

// Schema returns the JSON schema.
func (t *SymbolReferencesTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "Symbol name to find references for"
			},
			"def_file": {
				"type": "string",
				"description": "File path where the symbol is defined"
			}
		},
		"required": ["name", "def_file"]
	}`)
}

// Invoke executes the tool.
func (t *SymbolReferencesTool) Invoke(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	var params struct {
		Name    string `json:"name"`
		DefFile string `json:"def_file"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	references, err := t.query.References(params.Name, params.DefFile)
	if err != nil {
		return nil, fmt.Errorf("reference search failed: %w", err)
	}

	if len(references) == 0 {
		return &tools.Result{
			Content: fmt.Sprintf("No references found for '%s' in %s", params.Name, params.DefFile),
		}, nil
	}

	// Count definitions vs references
	var defs, refs int
	for _, r := range references {
		if r.IsDefinition {
			defs++
		} else {
			refs++
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d reference(s) to '%s':\n", refs, params.Name))
	sb.WriteString(fmt.Sprintf("(includes %d definition occurrence%s)\n\n", defs, pluralize(defs, "s", "")))

	for i, ref := range references {
		if ref.IsDefinition {
			sb.WriteString(fmt.Sprintf("%d. **Definition** in `%s` line %d\n",
				i+1, ref.FilePath, ref.Range.StartLine))
		} else {
			sb.WriteString(fmt.Sprintf("%d. Reference in `%s` line %d\n",
				i+1, ref.FilePath, ref.Range.StartLine))
		}
	}

	return &tools.Result{
		Content: sb.String(),
	}, nil
}

// FileSymbolsTool implements the kb_file_symbols tool.
// Lists all symbols defined in a file.
type FileSymbolsTool struct {
	query *Query
}

// NewFileSymbolsTool creates a new file symbols tool.
func NewFileSymbolsTool(query *Query) *FileSymbolsTool {
	return &FileSymbolsTool{query: query}
}

// Name returns the tool name.
func (t *FileSymbolsTool) Name() string {
	return "kb_file_symbols"
}

// Description returns the tool description.
func (t *FileSymbolsTool) Description() string {
	return "List all symbols defined in a specific file. Returns functions, types, variables, etc."
}

// IsReadOperation returns true.
func (t *FileSymbolsTool) IsReadOperation() bool {
	return true
}

// IsMutating returns false.
func (t *FileSymbolsTool) IsMutating() bool {
	return false
}

// RequiresReadBeforeEdit returns false.
func (t *FileSymbolsTool) RequiresReadBeforeEdit() bool {
	return false
}

// Schema returns the JSON schema.
func (t *FileSymbolsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "File path to list symbols from"
			}
		},
		"required": ["path"]
	}`)
}

// Invoke executes the tool.
func (t *FileSymbolsTool) Invoke(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	symbols, err := t.query.FileSymbols(params.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get symbols: %w", err)
	}

	if len(symbols) == 0 {
		return &tools.Result{
			Content: fmt.Sprintf("No symbols found in %s (or file not indexed)", params.Path),
		}, nil
	}

	// Group by kind
	byKind := make(map[SymbolKind][]Symbol)
	for _, sym := range symbols {
		byKind[sym.Kind] = append(byKind[sym.Kind], sym)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**%d symbols** in `%s`:\n\n", len(symbols), params.Path))

	// Print in order: types, functions, methods, variables, others
	order := []SymbolKind{SymbolType, SymbolInterface, SymbolStruct, SymbolFunction, SymbolMethod, SymbolVariable, SymbolConstant, SymbolField, SymbolImport}
	
	for _, kind := range order {
		syms := byKind[kind]
		if len(syms) == 0 {
			continue
		}

		sb.WriteString(fmt.Sprintf("**%s** (%d):\n", pluralizeKind(kind), len(syms)))
		for _, sym := range syms {
			marker := "  "
			if sym.Exported {
				marker = "* "
			}
			sig := ""
			if sym.Signature != "" {
				sig = " " + sym.Signature
			}
			sb.WriteString(fmt.Sprintf("%s%s (line %d)%s\n", marker, sym.Name, sym.Range.StartLine, sig))
		}
		sb.WriteString("\n")
	}

	return &tools.Result{
		Content: sb.String(),
	}, nil
}

// PackageExportsTool implements the kb_package_exports tool.
// Lists exported symbols from a package/directory.
type PackageExportsTool struct {
	query *Query
}

// NewPackageExportsTool creates a new package exports tool.
func NewPackageExportsTool(query *Query) *PackageExportsTool {
	return &PackageExportsTool{query: query}
}

// Name returns the tool name.
func (t *PackageExportsTool) Name() string {
	return "kb_package_exports"
}

// Description returns the tool description.
func (t *PackageExportsTool) Description() string {
	return "List exported/public symbols from a package or directory. " +
		"Useful for understanding a package's public API."
}

// IsReadOperation returns true.
func (t *PackageExportsTool) IsReadOperation() bool {
	return true
}

// IsMutating returns false.
func (t *PackageExportsTool) IsMutating() bool {
	return false
}

// RequiresReadBeforeEdit returns false.
func (t *PackageExportsTool) RequiresReadBeforeEdit() bool {
	return false
}

// Schema returns the JSON schema.
func (t *PackageExportsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Package directory path (e.g., './internal/kb' or 'pkg/api')"
			}
		},
		"required": ["path"]
	}`)
}

// Invoke executes the tool.
func (t *PackageExportsTool) Invoke(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	exports, err := t.query.PackageExports(params.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get exports: %w", err)
	}

	if len(exports) == 0 {
		return &tools.Result{
			Content: fmt.Sprintf("No exported symbols found in %s (or package not indexed)", params.Path),
		}, nil
	}

	// Group by file
	byFile := make(map[string][]Symbol)
	for _, sym := range exports {
		byFile[sym.Name] = append(byFile[sym.Name], sym)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**%d exported symbol(s)** in package `%s`:\n\n", len(exports), params.Path))

	for _, sym := range exports {
		sig := ""
		if sym.Signature != "" {
			sig = sym.Signature
		}
		sb.WriteString(fmt.Sprintf("- **%s** (%s)%s\n", sym.Name, sym.Kind, sig))
	}

	return &tools.Result{
		Content: sb.String(),
	}, nil
}

// ProjectMapTool implements the kb_project_map tool.
// Returns a high-level project structure overview.
type ProjectMapTool struct {
	query *Query
}

// NewProjectMapTool creates a new project map tool.
func NewProjectMapTool(query *Query) *ProjectMapTool {
	return &ProjectMapTool{query: query}
}

// Name returns the tool name.
func (t *ProjectMapTool) Name() string {
	return "kb_project_map"
}

// Description returns the tool description.
func (t *ProjectMapTool) Description() string {
	return "Get a high-level overview of the project structure: file counts, symbol counts, " +
		"languages used, and major packages."
}

// IsReadOperation returns true.
func (t *ProjectMapTool) IsReadOperation() bool {
	return true
}

// IsMutating returns false.
func (t *ProjectMapTool) IsMutating() bool {
	return false
}

// RequiresReadBeforeEdit returns false.
func (t *ProjectMapTool) RequiresReadBeforeEdit() bool {
	return false
}

// Schema returns the JSON schema.
func (t *ProjectMapTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"required": []
	}`)
}

// Invoke executes the tool.
func (t *ProjectMapTool) Invoke(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	structure, err := t.query.ProjectMap()
	if err != nil {
		return nil, fmt.Errorf("failed to get project map: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("**Project Overview**\n\n")
	sb.WriteString(fmt.Sprintf("- **Total Files**: %d\n", structure.TotalFiles))
	sb.WriteString(fmt.Sprintf("- **Total Symbols**: %d\n", structure.TotalSymbols))
	sb.WriteString(fmt.Sprintf("- **Packages**: %d\n\n", len(structure.Packages)))

	// Languages
	if len(structure.Languages) > 0 {
		sb.WriteString("**Languages**:\n")
		for lang, count := range structure.Languages {
			sb.WriteString(fmt.Sprintf("- %s: %d symbols\n", lang, count))
		}
		sb.WriteString("\n")
	}

	// Major packages (top 10 by file count)
	if len(structure.Packages) > 0 {
		sb.WriteString("**Top Packages**:\n")
		
		// Sort by file count
		topPackages := make([]PackageInfo, len(structure.Packages))
		copy(topPackages, structure.Packages)
		
		for i := 0; i < len(topPackages) && i < 10; i++ {
			pkg := topPackages[i]
			sb.WriteString(fmt.Sprintf("- `%s`: %d files, %d symbols (%d exported)\n",
				pkg.Path, pkg.FileCount, pkg.SymbolCount, pkg.ExportedCount))
		}
	}

	return &tools.Result{
		Content: sb.String(),
	}, nil
}

// AllTools returns all KB tools as a slice.
func AllTools(query *Query) []tools.Tool {
	return []tools.Tool{
		NewSymbolLookupTool(query),
		NewSymbolReferencesTool(query),
		NewFileSymbolsTool(query),
		NewPackageExportsTool(query),
		NewProjectMapTool(query),
	}
}

// Helper functions

func pluralize(n int, plural, singular string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func pluralizeKind(kind SymbolKind) string {
	switch kind {
	case SymbolFunction:
		return "Functions"
	case SymbolMethod:
		return "Methods"
	case SymbolType:
		return "Types"
	case SymbolInterface:
		return "Interfaces"
	case SymbolStruct:
		return "Structs"
	case SymbolVariable:
		return "Variables"
	case SymbolConstant:
		return "Constants"
	case SymbolField:
		return "Fields"
	case SymbolImport:
		return "Imports"
	case SymbolEnum:
		return "Enums"
	case SymbolModule:
		return "Modules"
	default:
		return string(kind)
	}
}

// Phase 3.8 Summary Tools

// FileSummaryTool implements the kb_file_summary tool.
// Retrieves or generates an LLM-backed summary of a file.
type FileSummaryTool struct {
	summariser *Summariser
}

// NewFileSummaryTool creates a new file summary tool.
func NewFileSummaryTool(summariser *Summariser) *FileSummaryTool {
	return &FileSummaryTool{summariser: summariser}
}

// Name returns the tool name.
func (t *FileSummaryTool) Name() string {
	return "kb_file_summary"
}

// Description returns the tool description.
func (t *FileSummaryTool) Description() string {
	return "Get an LLM-generated summary of a file. Includes purpose, public API, dependencies, and implementation notes. May trigger on-demand generation if not cached."
}

// IsReadOperation returns true.
func (t *FileSummaryTool) IsReadOperation() bool {
	return true
}

// IsMutating returns false.
func (t *FileSummaryTool) IsMutating() bool {
	return false
}

// RequiresReadBeforeEdit returns false.
func (t *FileSummaryTool) RequiresReadBeforeEdit() bool {
	return false
}

// Schema returns the JSON schema.
func (t *FileSummaryTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "File path to summarize"
			},
			"allow_generation": {
				"type": "boolean",
				"default": true,
				"description": "Whether to generate summary if not cached (costs ~1 cent)"
			}
		},
		"required": ["path"]
	}`)
}

// Invoke executes the tool.
func (t *FileSummaryTool) Invoke(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	var params struct {
		Path            string `json:"path"`
		AllowGeneration bool   `json:"allow_generation"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	if params.AllowGeneration == false {
		// Only check cache
		summary, err := t.summariser.GetSummary(params.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to get summary: %w", err)
		}
		if summary == nil {
			return &tools.Result{
				Content: fmt.Sprintf("No cached summary for %s. Set allow_generation=true to generate (costs ~1 cent).", params.Path),
			}, nil
		}
		return &tools.Result{Content: formatFileSummary(summary)}, nil
	}

	// Generate or retrieve summary
	summary, err := t.summariser.SummariseFile(params.Path)
	if err != nil {
		if err == ErrBudgetExceeded {
			return &tools.Result{
				Content: fmt.Sprintf("Budget exceeded. Cannot generate summary for %s.", params.Path),
			}, nil
		}
		return nil, fmt.Errorf("summarisation failed: %w", err)
	}

	return &tools.Result{Content: formatFileSummary(summary)}, nil
}

// formatFileSummary formats a FileSummary for display.
func formatFileSummary(summary *FileSummary) string {
	var sb strings.Builder

	staleness := summary.StalenessLabel()
	if staleness == "fresh" {
		sb.WriteString(fmt.Sprintf("📄 **%s** (fresh)\n\n", filepath.Base(summary.Path)))
	} else {
		sb.WriteString(fmt.Sprintf("📄 **%s** (%s)\n\n", filepath.Base(summary.Path), staleness))
	}

	sb.WriteString(fmt.Sprintf("**Purpose:**\n%s\n\n", summary.Purpose))

	if len(summary.PublicSurface) > 0 {
		sb.WriteString("**Public API:**\n")
		for _, pub := range summary.PublicSurface {
			sb.WriteString(fmt.Sprintf("- `%s`\n", pub))
		}
		sb.WriteString("\n")
	}

	if len(summary.DependsOn) > 0 {
		sb.WriteString("**Dependencies:**\n")
		for _, dep := range summary.DependsOn {
			sb.WriteString(fmt.Sprintf("- %s\n", dep))
		}
		sb.WriteString("\n")
	}

	if summary.Notes != "" {
		sb.WriteString(fmt.Sprintf("**Notes:**\n%s\n\n", summary.Notes))
	}

	sb.WriteString(fmt.Sprintf("*Generated: %s by %s*\n", 
		summary.GeneratedAt.Format("2006-01-02 15:04"), summary.GeneratedBy))

	return sb.String()
}

// PackageSummaryTool implements the kb_package_summary tool.
// Retrieves or generates a summary of a package/directory.
type PackageSummaryTool struct {
	summariser *Summariser
}

// NewPackageSummaryTool creates a new package summary tool.
func NewPackageSummaryTool(summariser *Summariser) *PackageSummaryTool {
	return &PackageSummaryTool{summariser: summariser}
}

// Name returns the tool name.
func (t *PackageSummaryTool) Name() string {
	return "kb_package_summary"
}

// Description returns the tool description.
func (t *PackageSummaryTool) Description() string {
	return "Get an LLM-generated summary of a package/directory. Aggregates file summaries and describes package architecture."
}

// IsReadOperation returns true.
func (t *PackageSummaryTool) IsReadOperation() bool {
	return true
}

// IsMutating returns false.
func (t *PackageSummaryTool) IsMutating() bool {
	return false
}

// RequiresReadBeforeEdit returns false.
func (t *PackageSummaryTool) RequiresReadBeforeEdit() bool {
	return false
}

// Schema returns the JSON schema.
func (t *PackageSummaryTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Package/directory path to summarize"
			}
		},
		"required": ["path"]
	}`)
}

// Invoke executes the tool.
func (t *PackageSummaryTool) Invoke(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	// Generate or retrieve package summary
	ps, err := t.summariser.SummarisePackage(params.Path)
	if err != nil {
		if err == ErrBudgetExceeded {
			return &tools.Result{
				Content: fmt.Sprintf("Budget exceeded. Cannot generate package summary for %s.", params.Path),
			}, nil
		}
		return nil, fmt.Errorf("package summarisation failed: %w", err)
	}

	return &tools.Result{Content: formatPackageSummary(ps)}, nil
}

// formatPackageSummary formats a PackageSummary for display.
func formatPackageSummary(ps *PackageSummary) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("📦 **%s**\n\n", ps.Path))
	sb.WriteString(fmt.Sprintf("**Purpose:**\n%s\n\n", ps.Purpose))

	if len(ps.PublicAPI) > 0 {
		sb.WriteString("**Public API:**\n")
		for _, api := range ps.PublicAPI {
			sb.WriteString(fmt.Sprintf("- `%s`\n", api))
		}
		sb.WriteString("\n")
	}

	if len(ps.EntryPoints) > 0 {
		sb.WriteString("**Entry Points:**\n")
		for _, ep := range ps.EntryPoints {
			sb.WriteString(fmt.Sprintf("- `%s`\n", ep))
		}
		sb.WriteString("\n")
	}

	if ps.Architecture != "" {
		sb.WriteString(fmt.Sprintf("**Architecture:**\n%s\n\n", ps.Architecture))
	}

	if len(ps.Subpackages) > 0 {
		sb.WriteString("**Subpackages:**\n")
		for _, sub := range ps.Subpackages {
			sb.WriteString(fmt.Sprintf("- %s\n", sub))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("*Files: %d | Generated: %s*\n", 
		len(ps.Files), ps.GeneratedAt.Format("2006-01-02 15:04")))

	return sb.String()
}

// ConventionsTool implements the kb_conventions tool.
// Lists approved codebase conventions for agents to follow.
type ConventionsTool struct {
	extractor *ConventionExtractor
}

// NewConventionsTool creates a new conventions tool.
func NewConventionsTool(extractor *ConventionExtractor) *ConventionsTool {
	return &ConventionsTool{extractor: extractor}
}

// Name returns the tool name.
func (t *ConventionsTool) Name() string {
	return "kb_conventions"
}

// Description returns the tool description.
func (t *ConventionsTool) Description() string {
	return "List approved codebase conventions and patterns. Only user-approved conventions are returned. Use these to maintain consistency with existing code."
}

// IsReadOperation returns true.
func (t *ConventionsTool) IsReadOperation() bool {
	return true
}

// IsMutating returns false.
func (t *ConventionsTool) IsMutating() bool {
	return false
}

// RequiresReadBeforeEdit returns false.
func (t *ConventionsTool) RequiresReadBeforeEdit() bool {
	return false
}

// Schema returns the JSON schema.
func (t *ConventionsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"topic": {
				"type": "string",
				"description": "Optional topic filter (e.g., 'error handling', 'naming')"
			}
		}
	}`)
}

// Invoke executes the tool.
func (t *ConventionsTool) Invoke(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	var params struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	// Get only approved conventions
	conventions, err := t.extractor.ListApprovedConventions()
	if err != nil {
		return nil, fmt.Errorf("failed to list conventions: %w", err)
	}

	// Filter by topic if specified
	if params.Topic != "" {
		var filtered []*ExtractedConvention
		for _, conv := range conventions {
			if strings.Contains(strings.ToLower(conv.Topic), strings.ToLower(params.Topic)) {
				filtered = append(filtered, conv)
			}
		}
		conventions = filtered
	}

	if len(conventions) == 0 {
		return &tools.Result{
			Content: "No approved conventions found. Conventions are extracted and require manual approval before being available to agents.",
		}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 **%d Approved Convention(s)**\n\n", len(conventions)))

	for i, conv := range conventions {
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, conv.Topic))
		sb.WriteString(fmt.Sprintf("   %s\n", conv.Description))
		sb.WriteString(fmt.Sprintf("   *Confidence: %.0f%% | Evidence: %d samples*\n\n", 
			conv.Confidence*100, len(conv.Evidence)))
	}

	return &tools.Result{Content: sb.String()}, nil
}

// AllSummaryTools returns all Phase 3.8 summary tools.
func AllSummaryTools(summariser *Summariser, extractor *ConventionExtractor) []tools.Tool {
	return []tools.Tool{
		NewFileSummaryTool(summariser),
		NewPackageSummaryTool(summariser),
		NewConventionsTool(extractor),
	}
}