package db

import (
	"database/sql"
	"fmt"
	"strings"
)

const symbolInsertBatch = 200

type Symbol struct {
	ID        int64
	FilePath  string
	Kind      string // "function", "method", "type", "import"
	Name      string
	Receiver  string // e.g. "*Scanner"; empty for non-methods
	Signature string
	LineStart int
	LineEnd   int
}

// SaveSymbols replaces the symbol index for a project. It deletes all
// existing symbols for the project and inserts the provided rows. Callers
// are expected to pass the complete current symbol set for the project.
func (db *DB) SaveSymbols(projectID int64, symbols []Symbol) error {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin save symbols transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM symbols WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("delete existing symbols: %w", err)
	}

	for start := 0; start < len(symbols); start += symbolInsertBatch {
		end := start + symbolInsertBatch
		if end > len(symbols) {
			end = len(symbols)
		}
		chunk := symbols[start:end]
		placeholders := buildValues(len(chunk), 8)
		args := make([]any, 0, len(chunk)*8)
		for _, s := range chunk {
			args = append(args, projectID, s.FilePath, s.Kind, s.Name, s.Receiver, s.Signature, s.LineStart, s.LineEnd)
		}
		if _, err := tx.Exec(`INSERT INTO symbols (project_id, file_path, kind, name, receiver, signature, line_start, line_end) VALUES `+placeholders, args...); err != nil {
			return fmt.Errorf("insert symbols batch [%d:%d]: %w", start, end, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save symbols: %w", err)
	}
	return nil
}

// scanSymbol reads the next row from rows in the column order used by both
// GetSymbols and FindSymbols (id, file_path, kind, name, receiver,
// signature, line_start, line_end).
func scanSymbol(rows *sql.Rows) (Symbol, error) {
	var s Symbol
	if err := rows.Scan(&s.ID, &s.FilePath, &s.Kind, &s.Name, &s.Receiver, &s.Signature, &s.LineStart, &s.LineEnd); err != nil {
		return Symbol{}, err
	}
	return s, nil
}

// GetSymbols returns up to limit symbol rows for a project, ordered by file
// path then line start. limit <= 0 means "all rows" (unbounded).
func (db *DB) GetSymbols(projectID int64, limit int) ([]Symbol, error) {
	query := `SELECT id, file_path, kind, name, receiver, signature, line_start, line_end
		 FROM symbols
		 WHERE project_id = ?
		 ORDER BY file_path, line_start`
	args := []any{projectID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.sqlDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query symbols: %w", err)
	}
	defer rows.Close()

	var symbols []Symbol
	for rows.Next() {
		s, err := scanSymbol(rows)
		if err != nil {
			return nil, fmt.Errorf("scan symbol row: %w", err)
		}
		symbols = append(symbols, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate symbol rows: %w", err)
	}
	return symbols, nil
}

// FindSymbols returns symbols for a project matching an optional
// case-insensitive name substring and/or exact kind, ordered by file path
// then line start. limit defaults to 50 when <= 0 and is clamped to 200.
func (db *DB) FindSymbols(projectID int64, name, kind string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	query := `SELECT id, file_path, kind, name, receiver, signature, line_start, line_end
			   FROM symbols
			   WHERE project_id = ?`
	args := []any{projectID}
	if name != "" {
		query += ` AND LOWER(name) LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(strings.ToLower(name))+"%")
	}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY file_path, line_start LIMIT ?`
	args = append(args, limit)

	rows, err := db.sqlDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query symbols: %w", err)
	}
	defer rows.Close()

	var symbols []Symbol
	for rows.Next() {
		s, err := scanSymbol(rows)
		if err != nil {
			return nil, fmt.Errorf("scan symbol row: %w", err)
		}
		symbols = append(symbols, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate symbol rows: %w", err)
	}
	return symbols, nil
}
