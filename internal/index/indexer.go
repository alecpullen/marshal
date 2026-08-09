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
	db         *db.DB
	embedder   embedding.Embedder
	onProgress func(message string)
}

// workItem is a file awaiting embedding. remaining counts the chunks not yet
// embedded; when it reaches zero the file is persisted to the database.
type workItem struct {
	path      string
	hash      string
	chunks    []db.Chunk
	vectors   [][]float32
	remaining int
}

// NewIndexer creates an Indexer. A nil embedder makes Reindex a no-op.
func NewIndexer(database *db.DB, e embedding.Embedder) *Indexer {
	return &Indexer{db: database, embedder: e}
}

// Reindex re-embeds only stale files (changed hash or changed model), purges
// files no longer present, and skips unchanged files. A nil embedder makes it
// a no-op. Chunks are collected across files and embedded in batches of up to
// 64 inputs so a single Embed call serves many files.
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

	const maxBatchInputs = 64

	seen := map[string]bool{}
	var items []workItem
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
			if ok {
				if err := ix.db.DeleteFileChunks(projectID, sf.Path); err != nil {
					return st, err
				}
			}
			continue
		}
		items = append(items, workItem{
			path:      sf.Path,
			hash:      sf.Hash,
			chunks:    chunks,
			vectors:   make([][]float32, len(chunks)),
			remaining: len(chunks),
		})
	}

	for path := range state {
		if !seen[path] {
			if err := ix.db.DeleteFileChunks(projectID, path); err != nil {
				return st, err
			}
			st.FilesPurged++
		}
	}

	if len(items) == 0 {
		return st, nil
	}

	type chunkRef struct {
		itemIdx  int
		chunkIdx int
		content  string
	}
	var refs []chunkRef
	for i, it := range items {
		for j, c := range it.chunks {
			refs = append(refs, chunkRef{itemIdx: i, chunkIdx: j, content: c.Content})
		}
	}

	for start := 0; start < len(refs); start += maxBatchInputs {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		end := start + maxBatchInputs
		if end > len(refs) {
			end = len(refs)
		}
		batch := refs[start:end]
		texts := make([]string, len(batch))
		for i, r := range batch {
			texts[i] = r.content
		}
		vecs, err := ix.embedder.Embed(ctx, texts)
		if err != nil {
			return st, fmt.Errorf("embed batch: %w", err)
		}
		if len(vecs) != len(batch) {
			return st, fmt.Errorf("embed batch: %d vectors for %d inputs", len(vecs), len(batch))
		}
		for i, r := range batch {
			items[r.itemIdx].vectors[r.chunkIdx] = vecs[i]
		}
		if ix.onProgress != nil {
			ix.onProgress(fmt.Sprintf("embedded batch %d/%d chunks", end, len(refs)))
		}
		// Persist each file as soon as all of its chunks have been embedded,
		// so a later batch failure does not discard earlier successful work.
		for _, r := range batch {
			it := &items[r.itemIdx]
			it.remaining--
			if it.remaining == 0 {
				n, err := ix.persist(projectID, model, it)
				if err != nil {
					return st, err
				}
				st.FilesEmbedded++
				st.ChunksWritten += n
			}
		}
	}
	return st, nil
}

// persist writes a fully-embedded file's chunks to the database and reports
// progress. It is called as soon as a file's last chunk is embedded, so a
// later batch failure does not discard earlier successful work. It returns the
// number of chunks written.
func (ix *Indexer) persist(projectID int64, model string, it *workItem) (int, error) {
	cwv := make([]db.ChunkWithVector, len(it.chunks))
	for i, c := range it.chunks {
		cwv[i] = db.ChunkWithVector{Chunk: c, Model: model, Dim: len(it.vectors[i]), Vector: it.vectors[i]}
	}
	if err := ix.db.ReplaceFileChunks(projectID, it.path, it.hash, cwv); err != nil {
		return 0, err
	}
	if ix.onProgress != nil {
		ix.onProgress(fmt.Sprintf("embedded %s (%d chunks)", it.path, len(it.chunks)))
	}
	return len(it.chunks), nil
}
