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

	mu       sync.Mutex
	cache    []db.VectorRow
	cacheGen int64 // maxID last loaded
	cacheN   int   // count last loaded
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
	if s.cache == nil || count != s.cacheN || maxID != s.cacheGen {
		rows, err := s.db.LoadVectors(s.projectID, s.embedder.Model())
		if err != nil {
			return nil, err
		}
		s.cache, s.cacheN, s.cacheGen = rows, count, maxID
	}
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
