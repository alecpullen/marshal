package retrieval

import (
	"context"
	"math"
)

type Candidate struct {
	FilePath   string
	StartLine  int
	EndLine    int
	Content    string
	Score      float64
	SourceName string
}

type Query struct {
	Text       string
	Limit      int
	PathPrefix string
}

type Source interface {
	Name() string
	Retrieve(ctx context.Context, q Query) ([]Candidate, error)
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
