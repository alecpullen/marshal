package embedding

import (
	"context"
	"errors"
	"testing"
)

type fakeEmbedder struct {
	vecs [][]float32
	err  error
}

func (f fakeEmbedder) Embed(context.Context, []string) ([][]float32, error) { return f.vecs, f.err }
func (f fakeEmbedder) Model() string                                        { return "fake" }
func (f fakeEmbedder) Dims() int                                            { return 0 }

func TestSplitBatches(t *testing.T) {
	got := splitBatches([]string{"a", "b", "c", "d", "e"}, 2)
	if len(got) != 3 || len(got[0]) != 2 || len(got[2]) != 1 {
		t.Fatalf("splitBatches = %#v", got)
	}
	if len(splitBatches(nil, 2)) != 0 {
		t.Fatal("empty input should yield no batches")
	}
	if len(splitBatches([]string{"a"}, 0)) != 1 {
		t.Fatal("size<=0 should fall back to default and yield one batch")
	}
}

func TestCheckDims(t *testing.T) {
	got, err := checkDims([][]float32{{1, 2, 3}, {4, 5, 6}}, 0)
	if err != nil || got != 3 {
		t.Fatalf("discover dims = %d, err = %v", got, err)
	}
	if _, err := checkDims([][]float32{{1, 2}}, 3); !errors.Is(err, ErrDimMismatch) {
		t.Fatalf("want ErrDimMismatch, got %v", err)
	}
}

func TestWithRetryRetriesTransient(t *testing.T) {
	retryBackoff = 0
	calls := 0
	err := withRetry(context.Background(), func() error {
		calls++
		if calls < 3 {
			return retryable(errors.New("boom"))
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
}

func TestWithRetryStopsOnNonRetryable(t *testing.T) {
	retryBackoff = 0
	calls := 0
	sentinel := errors.New("fatal")
	err := withRetry(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
}

func TestProbe(t *testing.T) {
	dims, err := Probe(context.Background(), fakeEmbedder{vecs: [][]float32{{1, 2, 3, 4}}})
	if err != nil || dims != 4 {
		t.Fatalf("Probe dims = %d, err = %v", dims, err)
	}
}
