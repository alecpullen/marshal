package index

import (
	"context"
	"fmt"

	"marshal/internal/db"
	"marshal/internal/llm/embedding"
	"marshal/internal/repo"
)

// Stats holds the result counters from a Reindex call.
type Stats struct {
	FilesEmbedded int
	ChunksWritten int
	FilesSkipped  int
	FilesPurged   int
}

// Indexer incrementally re-embeds files whose hash or model has changed.
type Indexer struct {
	db       *db.DB
	embedder embedding.Embedder
}

// NewIndexer creates an Indexer. A nil embedder makes Reindex a no-op.
func NewIndexer(database *db.DB, e embedding.Embedder) *Indexer {
	return &Indexer{db: database, embedder: e}
}

// Reindex re-embeds only stale files (changed hash or changed model), purges
// files no longer present, and skips unchanged files. A nil embedder makes it
// a no-op.
func (ix *Indexer) Reindex(ctx context.Context, projectID int64, scanned []repo.ScannedFile, symbolsByFile map[string][]db.Symbol) (Stats, error) {
	var st Stats
	if ix.embedder == nil {
		return st, nil
	}
	model := ix.embedder.Model()

	state, err := ix.db.ChunkedFiles(projectID)
	if err != nil {
		return st, err
	}

	seen := map[string]bool{}
	for _, sf := range scanned {
		if sf.ReadErr != nil {
			continue
		}
		seen[sf.Path] = true
		prev, ok := state[sf.Path]
		if ok && prev.FileHash == sf.Hash && prev.Model == model {
			st.FilesSkipped++
			continue
		}
		chunks := Chunk(sf, symbolsByFile[sf.Path])
		if len(chunks) == 0 {
			// Nothing to embed; clear any stale chunks for this file.
			if ok {
				if err := ix.db.DeleteFileChunks(projectID, sf.Path); err != nil {
					return st, err
				}
			}
			continue
		}
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Content
		}
		vecs, err := ix.embedder.Embed(ctx, texts)
		if err != nil {
			return st, fmt.Errorf("embed %s: %w", sf.Path, err)
		}
		if len(vecs) != len(chunks) {
			return st, fmt.Errorf("embed %s: %d vecs for %d chunks", sf.Path, len(vecs), len(chunks))
		}
		cwv := make([]db.ChunkWithVector, len(chunks))
		for i, c := range chunks {
			cwv[i] = db.ChunkWithVector{Chunk: c, Model: model, Dim: len(vecs[i]), Vector: vecs[i]}
		}
		if err := ix.db.ReplaceFileChunks(projectID, sf.Path, sf.Hash, cwv); err != nil {
			return st, err
		}
		st.FilesEmbedded++
		st.ChunksWritten += len(chunks)
	}

	for path := range state {
		if !seen[path] {
			if err := ix.db.DeleteFileChunks(projectID, path); err != nil {
				return st, err
			}
			st.FilesPurged++
		}
	}
	return st, nil
}
