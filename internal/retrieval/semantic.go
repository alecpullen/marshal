package retrieval

import (
	"context"
	"sort"
	"strings"
	"sync"

	"marshal/internal/db"
	"marshal/internal/llm/embedding"
)

type SemanticSource struct {
	db        *db.DB
	embedder  embedding.Embedder
	projectID int64

	mu         sync.Mutex
	cache      []db.VectorRow
	cacheGen   int64  // maxID last loaded
	cacheN     int    // count last loaded
	cacheModel string // embedder model when cache was built
}

func NewSemanticSource(database *db.DB, e embedding.Embedder, projectID int64) *SemanticSource {
	return &SemanticSource{db: database, embedder: e, projectID: projectID}
}

func (s *SemanticSource) Name() string { return "semantic" }

func (s *SemanticSource) vectors() ([]db.VectorRow, error) {
	count, maxID, err := s.db.ChunkGeneration(s.projectID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	model := s.embedder.Model()

	// Full reload on first load or model change.
	if s.cache == nil || model != s.cacheModel {
		rows, err := s.db.LoadVectors(s.projectID, model)
		if err != nil {
			return nil, err
		}
		s.cache = rows
		s.cacheN = count
		s.cacheGen = maxID
		s.cacheModel = model
		return s.cache, nil
	}

	// Fast path: nothing changed.
	if count == s.cacheN && maxID == s.cacheGen {
		return s.cache, nil
	}

	// Incremental: load only new/changed vectors.
	newRows, err := s.db.LoadVectorsSince(s.projectID, model, s.cacheGen)
	if err != nil {
		return nil, err
	}

	// Identify files affected by new/changed chunks.
	affected := map[string]bool{}
	for _, r := range newRows {
		affected[r.FilePath] = true
	}

	// Remove old cache entries for affected files (ReplaceFileChunks re-inserts).
	filtered := s.cache[:0]
	for _, r := range s.cache {
		if !affected[r.FilePath] {
			filtered = append(filtered, r)
		}
	}

	// If chunk count decreased, also remove entries for files no longer in DB.
	if count < s.cacheN {
		currentFiles, err := s.db.CurrentChunkFiles(s.projectID, model)
		if err != nil {
			return nil, err
		}
		pruned := filtered[:0]
		for _, r := range filtered {
			if currentFiles[r.FilePath] {
				pruned = append(pruned, r)
			}
		}
		filtered = pruned
	}

	// Append new rows.
	s.cache = append(filtered, newRows...)
	s.cacheN = count
	s.cacheGen = maxID
	s.cacheModel = model
	return s.cache, nil
}

func (s *SemanticSource) Retrieve(ctx context.Context, q Query) ([]Candidate, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	qv, err := s.embedder.Embed(ctx, []string{q.Text})
	if err != nil {
		return nil, err
	}
	if len(qv) != 1 {
		return nil, nil
	}
	rows, err := s.vectors()
	if err != nil {
		return nil, err
	}
	cands := make([]Candidate, 0, len(rows))
	for _, r := range rows {
		if q.PathPrefix != "" && !strings.HasPrefix(r.FilePath, q.PathPrefix) {
			continue
		}
		cands = append(cands, Candidate{
			FilePath: r.FilePath, StartLine: r.StartLine, EndLine: r.EndLine,
			Content: r.Content, Score: cosine(qv[0], r.Vector), SourceName: "semantic",
		})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
	if len(cands) > limit {
		cands = cands[:limit]
	}
	return cands, nil
}
