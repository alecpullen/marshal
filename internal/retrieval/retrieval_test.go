package retrieval

import "testing"

func TestCosine(t *testing.T) {
	if got := cosine([]float32{1, 0}, []float32{1, 0}); got < 0.999 {
		t.Fatalf("identical vectors cosine=%v", got)
	}
	if got := cosine([]float32{1, 0}, []float32{0, 1}); got > 0.001 {
		t.Fatalf("orthogonal cosine=%v", got)
	}
}
