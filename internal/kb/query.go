package kb

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// SymbolResult represents a symbol lookup result with relevance scoring.
type SymbolResult struct {
	Symbol   Symbol
	FilePath string
	Score    float64 // Higher is better (exact match = 1.0)
}

// ProjectStructure provides a high-level view of the project.
type ProjectStructure struct {
	TotalFiles   int
	TotalSymbols int
	Languages    map[string]int // language -> symbol count
	Packages     []PackageInfo
}

// PackageInfo describes a package/directory.
type PackageInfo struct {
	Path           string
	FileCount      int
	SymbolCount    int
	ExportedCount  int
	MainLanguage   string
}

// Query provides lookup methods for the symbol index.
type Query struct {
	store *IndexStore
}

// NewQuery creates a new query instance.
func NewQuery(store *IndexStore) *Query {
	return &Query{store: store}
}

// Lookup finds symbols by name, optionally scoped to a file or directory.
// Returns results ranked by relevance (exact > prefix > substring > fuzzy).
func (q *Query) Lookup(name string, pathHint string) ([]SymbolResult, error) {
	// First, try exact matches in the current scope
	results, err := q.exactLookup(name, pathHint)
	if err != nil {
		return nil, err
	}

	// If we have exact matches, return them
	if len(results) > 0 {
		return results, nil
	}

	// Fall back to FTS5 fuzzy search
	return q.fuzzyLookup(name, pathHint)
}

// exactLookup searches for exact symbol name matches.
func (q *Query) exactLookup(name string, pathHint string) ([]SymbolResult, error) {
	// Build query
	var query string
	var args []interface{}
	
	if pathHint != "" {
		// Scope to path hint (file or directory)
		if strings.HasSuffix(pathHint, "/") || pathHint == "." {
			// Directory hint
			query = `
				SELECT i.file_path, i.symbols_json 
				FROM kb_index i 
				WHERE i.file_path LIKE ? AND (i.superseded_by = '' OR i.superseded_by IS NULL)
			`
			args = append(args, pathHint+"%")
		} else if filepath.Ext(pathHint) == "" {
			// No file extension - treat as prefix match (e.g., "api" matches "api.go", "api_test.go")
			query = `
				SELECT i.file_path, i.symbols_json 
				FROM kb_index i 
				WHERE i.file_path LIKE ? AND (i.superseded_by = '' OR i.superseded_by IS NULL)
			`
			args = append(args, pathHint+".%")
		} else {
			// Specific file hint with extension
			query = `
				SELECT i.file_path, i.symbols_json 
				FROM kb_index i 
				WHERE i.file_path = ? AND (i.superseded_by = '' OR i.superseded_by IS NULL)
			`
			args = append(args, pathHint)
		}
	} else {
		// Global search
		query = `
			SELECT i.file_path, i.symbols_json 
			FROM kb_index i 
			WHERE i.superseded_by = '' OR i.superseded_by IS NULL
		`
	}

	rows, err := q.store.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying index: %w", err)
	}
	defer rows.Close()

	var results []SymbolResult
	for rows.Next() {
		var filePath string
		var symbolsJSON string
		if err := rows.Scan(&filePath, &symbolsJSON); err != nil {
			continue
		}

		var symbols []Symbol
		if err := jsonUnmarshal([]byte(symbolsJSON), &symbols); err != nil {
			continue
		}

		for _, sym := range symbols {
			// Check for exact match
			if sym.Name == name {
				score := 1.0
				// Boost exported symbols
				if sym.Exported {
					score += 0.1
				}
				// Boost if in hinted path
				if pathHint != "" && strings.Contains(filePath, pathHint) {
					score += 0.2
				}

				results = append(results, SymbolResult{
					Symbol:   sym,
					FilePath: filePath,
					Score:    score,
				})
			}
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results, rows.Err()
}

// fuzzyLookup uses FTS5 for fuzzy symbol name matching.
func (q *Query) fuzzyLookup(name string, pathHint string) ([]SymbolResult, error) {
	// Build FTS5 query (supports prefix matching with *)
	ftsQuery := name + "*"
	
	var sqlQuery string
	var args []interface{}
	
	if pathHint != "" {
		// Scoped fuzzy search
		sqlQuery = `
			SELECT s.symbol_name, s.file_path 
			FROM kb_symbols_fts s 
			JOIN kb_index i ON s.file_path = i.file_path
			WHERE s.symbol_name MATCH ? AND i.file_path LIKE ?
			  AND (i.superseded_by = '' OR i.superseded_by IS NULL)
			ORDER BY rank
			LIMIT 20
		`
		args = append(args, ftsQuery, pathHint+"%")
	} else {
		// Global fuzzy search
		sqlQuery = `
			SELECT s.symbol_name, s.file_path 
			FROM kb_symbols_fts s 
			JOIN kb_index i ON s.file_path = i.file_path
			WHERE s.symbol_name MATCH ?
			  AND (i.superseded_by = '' OR i.superseded_by IS NULL)
			ORDER BY rank
			LIMIT 20
		`
		args = append(args, ftsQuery)
	}

	rows, err := q.store.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("fuzzy search: %w", err)
	}
	defer rows.Close()

	var results []SymbolResult
	seen := make(map[string]bool) // Deduplicate
	
	for rows.Next() {
		var symbolName, filePath string
		if err := rows.Scan(&symbolName, &filePath); err != nil {
			continue
		}

		// Deduplication key
		key := filePath + ":" + symbolName
		if seen[key] {
			continue
		}
		seen[key] = true

		// Get the full symbol details
		entry, err := q.store.Get(filePath)
		if err != nil || entry == nil {
			continue
		}

		for _, sym := range entry.Symbols {
			if sym.Name == symbolName {
				// Calculate fuzzy score
				score := q.calculateFuzzyScore(name, symbolName)
				
				results = append(results, SymbolResult{
					Symbol:   sym,
					FilePath: filePath,
					Score:    score,
				})
				break
			}
		}
	}

	// Sort by score
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results, rows.Err()
}

// calculateFuzzyScore returns a score from 0-1 based on name similarity.
func (q *Query) calculateFuzzyScore(query, match string) float64 {
	query = strings.ToLower(query)
	match = strings.ToLower(match)

	// Exact match
	if query == match {
		return 1.0
	}

	// Prefix match
	if strings.HasPrefix(match, query) {
		return 0.8
	}

	// Contains
	if strings.Contains(match, query) {
		return 0.6
	}

	// Substring match (both directions)
	for i := 0; i < len(query); i++ {
		for j := i + 1; j <= len(query); j++ {
			substr := query[i:j]
			if len(substr) >= 3 && strings.Contains(match, substr) {
				return 0.4 + float64(len(substr))/float64(len(query))*0.2
			}
		}
	}

	return 0.1
}

// References finds all references to a symbol.
// Best-effort: uses the cross-file reference index.
func (q *Query) References(symbolName string, defFile string) ([]Reference, error) {
	// First, get the definition
	var references []Reference

	if defFile != "" {
		// Get references from the reference table
		rows, err := q.store.db.Query(
			`SELECT ref_file, ref_line FROM kb_references 
			 WHERE symbol_name = ? AND def_file = ?
			 ORDER BY ref_file, ref_line`,
			symbolName, defFile,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var refFile string
			var refLine int
			if err := rows.Scan(&refFile, &refLine); err != nil {
				continue
			}

			references = append(references, Reference{
				FilePath:     refFile,
				Range:        LineRange{StartLine: refLine},
				IsDefinition: false,
			})
		}
		rows.Close()

		// Add the definition itself
		entry, err := q.store.Get(defFile)
		if err == nil && entry != nil {
			for _, sym := range entry.Symbols {
				if sym.Name == symbolName && sym.Metadata["is_definition"] == true {
					references = append([]Reference{{
						FilePath:     defFile,
						Range:        sym.Range,
						IsDefinition: true,
					}}, references...)
					break
				}
			}
		}
	}

	return references, nil
}

// FileSymbols returns all symbols defined in a file.
func (q *Query) FileSymbols(filePath string) ([]Symbol, error) {
	entry, err := q.store.Get(filePath)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("file not indexed: %s", filePath)
	}

	return entry.Symbols, nil
}

// PackageExports returns exported symbols from a package directory.
func (q *Query) PackageExports(pkgPath string) ([]Symbol, error) {
	// Ensure path ends with /
	if !strings.HasSuffix(pkgPath, "/") {
		pkgPath += "/"
	}

	rows, err := q.store.db.Query(
		`SELECT file_path, symbols_json FROM kb_index 
		 WHERE file_path LIKE ? 
		   AND (superseded_by = '' OR superseded_by IS NULL)`,
		pkgPath+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var exports []Symbol
	for rows.Next() {
		var filePath string
		var symbolsJSON string
		if err := rows.Scan(&filePath, &symbolsJSON); err != nil {
			continue
		}

		var symbols []Symbol
		if err := jsonUnmarshal([]byte(symbolsJSON), &symbols); err != nil {
			continue
		}

		for _, sym := range symbols {
			if sym.Exported {
				exports = append(exports, sym)
			}
		}
	}

	return exports, rows.Err()
}

// ProjectMap returns a high-level structure of the project.
func (q *Query) ProjectMap() (*ProjectStructure, error) {
	// Get all indexed files
	rows, err := q.store.db.Query(
		`SELECT file_path, symbols_json, parser FROM kb_index 
		 WHERE superseded_by = '' OR superserseded_by IS NULL`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	proj := &ProjectStructure{
		Languages: make(map[string]int),
		Packages:  make([]PackageInfo, 0),
	}

	// Collect per-directory stats
	pkgStats := make(map[string]*PackageInfo)

	for rows.Next() {
		var filePath string
		var symbolsJSON string
		var parser string
		if err := rows.Scan(&filePath, &symbolsJSON, &parser); err != nil {
			continue
		}

		proj.TotalFiles++
		proj.Languages[parser]++

		var symbols []Symbol
		if err := jsonUnmarshal([]byte(symbolsJSON), &symbols); err != nil {
			continue
		}

		proj.TotalSymbols += len(symbols)

		// Get package directory
		dir := filepath.Dir(filePath)
		if dir == "." {
			dir = "/"
		}

		if pkgStats[dir] == nil {
			pkgStats[dir] = &PackageInfo{Path: dir}
		}

		pkgStats[dir].FileCount++
		pkgStats[dir].SymbolCount += len(symbols)
		
		for _, sym := range symbols {
			if sym.Exported {
				pkgStats[dir].ExportedCount++
			}
		}
		
		// Track main language
		if pkgStats[dir].MainLanguage == "" {
			pkgStats[dir].MainLanguage = parser
		}
	}

	// Convert package stats to slice
	for _, info := range pkgStats {
		proj.Packages = append(proj.Packages, *info)
	}

	// Sort packages by path
	sort.Slice(proj.Packages, func(i, j int) bool {
		return proj.Packages[i].Path < proj.Packages[j].Path
	})

	return proj, rows.Err()
}

// AllSymbols returns all symbols matching a pattern (for exploration).
func (q *Query) AllSymbols(pattern string) ([]SymbolResult, error) {
	// Use FTS5 if pattern contains wildcards
	if strings.Contains(pattern, "*") || strings.Contains(pattern, "?") {
		return q.fuzzyLookup(strings.TrimSuffix(pattern, "*"), "")
	}

	// Otherwise use substring search
	rows, err := q.store.db.Query(
		`SELECT file_path, symbols_json FROM kb_index 
		 WHERE superseded_by = '' OR superseded_by IS NULL`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	patternLower := strings.ToLower(pattern)
	var results []SymbolResult

	for rows.Next() {
		var filePath string
		var symbolsJSON string
		if err := rows.Scan(&filePath, &symbolsJSON); err != nil {
			continue
		}

		var symbols []Symbol
		if err := jsonUnmarshal([]byte(symbolsJSON), &symbols); err != nil {
			continue
		}

		for _, sym := range symbols {
			if strings.Contains(strings.ToLower(sym.Name), patternLower) {
				results = append(results, SymbolResult{
					Symbol:   sym,
					FilePath: filePath,
					Score:    0.5,
				})
			}
		}
	}

	return results, rows.Err()
}

// jsonUnmarshal is a helper to unmarshal JSON (in case we need custom handling).
func jsonUnmarshal(data []byte, v interface{}) error {
	// In the future, we could add custom JSON unmarshaling here
	// For now, use standard library
	return json.Unmarshal(data, v)
}