package db

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// Chunk is one embeddable unit of a file.
type Chunk struct {
	FilePath   string
	FileHash   string
	Kind       string // "code" | "doc"
	SymbolName string
	StartLine  int
	EndLine    int
	Content    string
	TokenCount int
}

// ChunkWithVector pairs a Chunk with its embedding for insertion.
type ChunkWithVector struct {
	Chunk
	Model  string
	Dim    int
	Vector []float32
}

// FileChunkState is the stored (hash, model) for a file's chunks, used to
// decide whether a file needs re-embedding.
type FileChunkState struct {
	FileHash string
	Model    string
}

// VectorRow is one chunk's vector plus enough to render a retrieval hit.
type VectorRow struct {
	ChunkID   int64
	FilePath  string
	StartLine int
	EndLine   int
	Content   string
	Vector    []float32
}

// encodeVector serializes a float32 slice as little-endian bytes.
func encodeVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVector reverses encodeVector and validates the result length.
func decodeVector(b []byte, dims int) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vector blob length %d is not a multiple of 4", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	if dims > 0 && len(v) != dims {
		return nil, fmt.Errorf("vector length mismatch: got %d, want %d", len(v), dims)
	}
	return v, nil
}

// ReplaceFileChunks deletes a file's existing chunks and inserts the given
// chunks + embeddings in one transaction. Per-project locked.
func (db *DB) ReplaceFileChunks(projectID int64, filePath, fileHash string, chunks []ChunkWithVector) error {
	unlock := db.locks.Lock(projectID)
	defer unlock()

	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin replace chunks: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM chunks WHERE project_id = ? AND file_path = ?`, projectID, filePath); err != nil {
		return fmt.Errorf("delete file chunks: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, c := range chunks {
		res, err := tx.Exec(
			`INSERT INTO chunks (project_id, file_path, file_hash, kind, symbol_name, start_line, end_line, content, token_count, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, filePath, fileHash, c.Kind, c.SymbolName, c.StartLine, c.EndLine, c.Content, c.TokenCount, now)
		if err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
		chunkID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("chunk id: %w", err)
		}
		if _, err := tx.Exec(
			`INSERT INTO embeddings (chunk_id, model, dim, vector) VALUES (?, ?, ?, ?)`,
			chunkID, c.Model, c.Dim, encodeVector(c.Vector)); err != nil {
			return fmt.Errorf("insert embedding: %w", err)
		}
	}
	return tx.Commit()
}

// DeleteFileChunks removes a file's chunks (embeddings cascade).
func (db *DB) DeleteFileChunks(projectID int64, filePath string) error {
	unlock := db.locks.Lock(projectID)
	defer unlock()
	if _, err := db.sqlDB.Exec(`DELETE FROM chunks WHERE project_id = ? AND file_path = ?`, projectID, filePath); err != nil {
		return fmt.Errorf("delete file chunks: %w", err)
	}
	return nil
}

// ChunkedFiles returns the stored (hash, model) per file for a project.
func (db *DB) ChunkedFiles(projectID int64) (map[string]FileChunkState, error) {
	rows, err := db.sqlDB.Query(
		`SELECT DISTINCT c.file_path, c.file_hash, e.model
		   FROM chunks c JOIN embeddings e ON e.chunk_id = c.id
		  WHERE c.project_id = ?`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query chunked files: %w", err)
	}
	defer rows.Close()
	out := map[string]FileChunkState{}
	for rows.Next() {
		var path string
		var st FileChunkState
		if err := rows.Scan(&path, &st.FileHash, &st.Model); err != nil {
			return nil, err
		}
		out[path] = st
	}
	return out, rows.Err()
}

// LoadVectors returns all chunk vectors for a project+model (read path).
func (db *DB) LoadVectors(projectID int64, model string) ([]VectorRow, error) {
	rows, err := db.sqlDB.Query(
		`SELECT c.id, c.file_path, c.start_line, c.end_line, c.content, e.vector, e.dim
		   FROM chunks c JOIN embeddings e ON e.chunk_id = c.id
		  WHERE c.project_id = ? AND e.model = ?`, projectID, model)
	if err != nil {
		return nil, fmt.Errorf("load vectors: %w", err)
	}
	defer rows.Close()
	var out []VectorRow
	for rows.Next() {
		var r VectorRow
		var blob []byte
		var dims int
		if err := rows.Scan(&r.ChunkID, &r.FilePath, &r.StartLine, &r.EndLine, &r.Content, &blob, &dims); err != nil {
			return nil, err
		}
		vec, err := decodeVector(blob, dims)
		if err != nil {
			return nil, fmt.Errorf("load vectors: %w", err)
		}
		r.Vector = vec
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChunkGeneration returns (row count, max chunk id) for a project — a cheap
// signal a reader caches on to detect index changes.
func (db *DB) ChunkGeneration(projectID int64) (int, int64, error) {
	var count int
	var maxID sql.NullInt64
	err := db.sqlDB.QueryRow(
		`SELECT COUNT(*), MAX(id) FROM chunks WHERE project_id = ?`, projectID).Scan(&count, &maxID)
	if err != nil {
		return 0, 0, fmt.Errorf("chunk generation: %w", err)
	}
	return count, maxID.Int64, nil
}
