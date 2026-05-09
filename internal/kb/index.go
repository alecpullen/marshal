// Package kb implements the Knowledge Base Foundation (Phase 3.75).
// It provides structured symbol indexing with tree-sitter integration,
// deterministic lookup tools, and incremental maintenance via file watching.
package kb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SymbolKind represents the type of symbol.
// Extensible: add new kinds for additional languages (Rust, Java, etc.)
type SymbolKind string

const (
	SymbolFunction  SymbolKind = "function"
	SymbolMethod    SymbolKind = "method"
	SymbolType     SymbolKind = "type"
	SymbolInterface SymbolKind = "interface"
	SymbolStruct    SymbolKind = "struct"
	SymbolVariable  SymbolKind = "variable"
	SymbolConstant  SymbolKind = "constant"
	SymbolImport    SymbolKind = "import"
	SymbolField     SymbolKind = "field"
	SymbolEnum      SymbolKind = "enum"
	SymbolModule    SymbolKind = "module"
)

// IsValid checks if the symbol kind is valid.
func (k SymbolKind) IsValid() bool {
	switch k {
	case SymbolFunction, SymbolMethod, SymbolType, SymbolInterface,
		SymbolStruct, SymbolVariable, SymbolConstant, SymbolImport,
		SymbolField, SymbolEnum, SymbolModule:
		return true
	}
	return false
}

// LineRange represents a position range in a file.
type LineRange struct {
	StartLine int `json:"start_line"`
	StartCol  int `json:"start_col"`
	EndLine   int `json:"end_line"`
	EndCol    int `json:"end_col"`
}

// Reference represents a cross-file symbol reference.
type Reference struct {
	FilePath string    `json:"file_path"`
	Range    LineRange `json:"range"`
	// IsDefinition indicates if this is the defining occurrence.
	IsDefinition bool `json:"is_definition"`
}

// Symbol represents a code symbol extracted from a file.
type Symbol struct {
	Name       string      `json:"name"`
	Kind       SymbolKind  `json:"kind"`
	Range      LineRange   `json:"range"`
	Parent     string      `json:"parent,omitempty"`    // Parent symbol name (e.g., struct for methods)
	Signature  string      `json:"signature,omitempty"` // For functions: params + returns
	Exported   bool        `json:"exported"`            // true if publicly accessible
	Language   string      `json:"language"`            // 'go', 'typescript', 'python', etc.
	References []Reference `json:"references,omitempty"` // Cross-file references (best-effort)
	// Extensible: Language-specific fields can be added via a generic map if needed
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// Import represents an import statement.
type Import struct {
	Path    string `json:"path"`
	Alias   string `json:"alias,omitempty"`
	Line    int    `json:"line"`
	IsLocal bool   `json:"is_local"` // true for relative imports (./ or ../)
}

// IndexEntry represents the full symbol index for a single file.
type IndexEntry struct {
	FilePath    string    `json:"file_path"`
	ContentHash string    `json:"content_hash"` // BLAKE3 hash for invalidation
	Parser      string    `json:"parser"`       // Parser used (go, typescript, etc.)
	Symbols     []Symbol  `json:"symbols"`
	Imports     []Import  `json:"imports,omitempty"`
	IndexedAt   time.Time `json:"indexed_at"`
	// SupersededBy is set when a newer version exists.
	SupersededBy string `json:"superseded_by,omitempty"`
}

// IndexStore provides persistent storage for the symbol index.
// Uses SQLite for storage and FTS5 for full-text search.
type IndexStore struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
}

// NewIndexStore creates a new index store backed by SQLite.
// The db parameter should be the session database (already initialized).
func NewIndexStore(db *sql.DB) (*IndexStore, error) {
	s := &IndexStore{
		db: db,
	}

	if err := s.initSchema(); err != nil {
		return nil, fmt.Errorf("initializing KB schema: %w", err)
	}

	return s, nil
}

// initSchema creates the necessary tables if they don't exist.
func (s *IndexStore) initSchema() error {
	schema := `
-- Symbol index entries
CREATE TABLE IF NOT EXISTS kb_index (
    file_path TEXT PRIMARY KEY,
    content_hash TEXT NOT NULL,
    parser TEXT NOT NULL,
    symbols_json TEXT NOT NULL,
    imports_json TEXT,
    indexed_at INTEGER NOT NULL,
    superseded_by TEXT
);

-- Full-text search on symbol names (separate table for simplicity)
CREATE VIRTUAL TABLE IF NOT EXISTS kb_symbols_fts USING fts5(
    symbol_name,
    file_path UNINDEXED
);

-- Cross-file reference index
CREATE TABLE IF NOT EXISTS kb_references (
    symbol_name TEXT NOT NULL,
    def_file TEXT NOT NULL,
    ref_file TEXT NOT NULL,
    ref_line INTEGER NOT NULL,
    PRIMARY KEY (symbol_name, def_file, ref_file, ref_line)
);

-- Index statistics for quick lookup
CREATE TABLE IF NOT EXISTS kb_stats (
    key TEXT PRIMARY KEY,
    value INTEGER NOT NULL
);

-- Index for faster symbol lookups
CREATE INDEX IF NOT EXISTS idx_kb_symbol_name ON kb_references(symbol_name);
CREATE INDEX IF NOT EXISTS idx_kb_def_file ON kb_references(def_file);
`

	_, err := s.db.Exec(schema)
	return err
}

// Put stores or updates an index entry.
func (s *IndexStore) Put(entry *IndexEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	symbolsJSON, err := json.Marshal(entry.Symbols)
	if err != nil {
		return fmt.Errorf("marshaling symbols: %w", err)
	}

	importsJSON, err := json.Marshal(entry.Imports)
	if err != nil {
		return fmt.Errorf("marshaling imports: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Insert or replace index entry
	_, err = tx.Exec(
		`INSERT OR REPLACE INTO kb_index 
		(file_path, content_hash, parser, symbols_json, imports_json, indexed_at, superseded_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.FilePath, entry.ContentHash, entry.Parser,
		symbolsJSON, importsJSON, entry.IndexedAt.Unix(), entry.SupersededBy,
	)
	if err != nil {
		return fmt.Errorf("inserting index entry: %w", err)
	}

	// Update FTS5 index
	// First delete existing entries for this file
	_, err = tx.Exec(`DELETE FROM kb_symbols_fts WHERE file_path = ?`, entry.FilePath)
	if err != nil {
		return fmt.Errorf("clearing FTS entries: %w", err)
	}

	// Insert new symbol names
	for _, sym := range entry.Symbols {
		_, err = tx.Exec(
			`INSERT INTO kb_symbols_fts (symbol_name, file_path) VALUES (?, ?)`,
			sym.Name, entry.FilePath,
		)
		if err != nil {
			return fmt.Errorf("inserting FTS entry: %w", err)
		}
	}

	// Update references
	_, err = tx.Exec(`DELETE FROM kb_references WHERE def_file = ? OR ref_file = ?`,
		entry.FilePath, entry.FilePath)
	if err != nil {
		return fmt.Errorf("clearing references: %w", err)
	}

	// Insert references from symbols
	for _, sym := range entry.Symbols {
		for _, ref := range sym.References {
			if !ref.IsDefinition {
				_, err = tx.Exec(
					`INSERT OR IGNORE INTO kb_references 
					(symbol_name, def_file, ref_file, ref_line)
					VALUES (?, ?, ?, ?)`,
					sym.Name, entry.FilePath, ref.FilePath, ref.Range.StartLine,
				)
				if err != nil {
					return fmt.Errorf("inserting reference: %w", err)
				}
			}
		}
	}

	return tx.Commit()
}

// Get retrieves an index entry by file path.
func (s *IndexStore) Get(filePath string) (*IndexEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var entry IndexEntry
	var symbolsJSON, importsJSON string
	var indexedAt int64

	err := s.db.QueryRow(
		`SELECT file_path, content_hash, parser, symbols_json, imports_json, 
		        indexed_at, superseded_by
		 FROM kb_index WHERE file_path = ?`,
		filePath,
	).Scan(
		&entry.FilePath, &entry.ContentHash, &entry.Parser,
		&symbolsJSON, &importsJSON, &indexedAt, &entry.SupersededBy,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	entry.IndexedAt = time.Unix(indexedAt, 0)

	if err := json.Unmarshal([]byte(symbolsJSON), &entry.Symbols); err != nil {
		return nil, fmt.Errorf("unmarshaling symbols: %w", err)
	}

	if importsJSON != "" {
		if err := json.Unmarshal([]byte(importsJSON), &entry.Imports); err != nil {
			return nil, fmt.Errorf("unmarshaling imports: %w", err)
		}
	}

	return &entry, nil
}

// Remove deletes an index entry.
func (s *IndexStore) Remove(filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete from FTS5
	_, err = tx.Exec(`DELETE FROM kb_symbols_fts WHERE file_path = ?`, filePath)
	if err != nil {
		return err
	}

	// Delete references
	_, err = tx.Exec(`DELETE FROM kb_references WHERE def_file = ? OR ref_file = ?`,
		filePath, filePath)
	if err != nil {
		return err
	}

	// Delete index entry
	_, err = tx.Exec(`DELETE FROM kb_index WHERE file_path = ?`, filePath)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// List returns all indexed file paths.
func (s *IndexStore) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT file_path FROM kb_index WHERE superseded_by = '' OR superseded_by IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}

	return paths, rows.Err()
}

// Stats returns statistics about the index.
func (s *IndexStore) Stats() (totalFiles, totalSymbols int, lastIndexed time.Time, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Count files
	err = s.db.QueryRow(
		`SELECT COUNT(*) FROM kb_index WHERE superseded_by = '' OR superseded_by IS NULL`,
	).Scan(&totalFiles)
	if err != nil {
		return 0, 0, time.Time{}, err
	}

	// Count symbols by summing JSON array lengths
	// This is approximate; in production we might want a counter table
	rows, err := s.db.Query(`SELECT symbols_json FROM kb_index`)
	if err != nil {
		return 0, 0, time.Time{}, err
	}
	defer rows.Close()

	totalSymbols = 0
	for rows.Next() {
		var jsonStr string
		if err := rows.Scan(&jsonStr); err != nil {
			continue
		}
		var symbols []Symbol
		if err := json.Unmarshal([]byte(jsonStr), &symbols); err == nil {
			totalSymbols += len(symbols)
		}
	}

	// Get last indexed time
	var lastIndexedUnix int64
	err = s.db.QueryRow(
		`SELECT MAX(indexed_at) FROM kb_index`,
	).Scan(&lastIndexedUnix)
	if err != nil && err != sql.ErrNoRows {
		return 0, 0, time.Time{}, err
	}

	return totalFiles, totalSymbols, time.Unix(lastIndexedUnix, 0), nil
}

// Close closes the store (does not close the underlying DB, which is shared).
func (s *IndexStore) Close() error {
	return nil
}

// Clear removes all index entries.
func (s *IndexStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM kb_index`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`DELETE FROM kb_references`)
	if err != nil {
		return err
	}

	// FTS5 contentless table clears automatically when source table is cleared

	return nil
}
