// Package embedding provides Marshal's local-first text embedding capability:
// an Embedder abstraction plus Ollama-native and OpenAI-compatible backends.
// Embedders are NOT safe for concurrent use (they cache the vector dimension
// after the first embed); callers serialize or construct one per worker.
package embedding

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Embedder is implemented by every embedding backend Marshal can talk to.
type Embedder interface {
	// Embed returns one vector per input text, in input order. An empty
	// input slice returns an empty result and no error.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Model returns the embedding model name (stored alongside vectors by
	// the semantic index so a model change marks vectors stale).
	Model() string
	// Dims returns the embedding dimension, discovered on the first
	// successful Embed and cached; 0 before the first embed.
	Dims() int
}

// ErrDimMismatch is returned when a backend yields a vector whose dimension
// differs from the dimension established by the first successful embed.
var ErrDimMismatch = errors.New("embedding: vector dimension mismatch")

const (
	// defaultBatchSize bounds how many inputs are sent per request.
	defaultBatchSize = 64
	// maxRetryAttempts bounds retry attempts on transient errors.
	maxRetryAttempts = 3
)

// retryBackoff is the fixed delay between retry attempts. It is a var (not a
// const) so tests can set it to 0.
var retryBackoff = 200 * time.Millisecond

// splitBatches splits texts into consecutive slices of at most size.
func splitBatches(texts []string, size int) [][]string {
	if size <= 0 {
		size = defaultBatchSize
	}
	var batches [][]string
	for start := 0; start < len(texts); start += size {
		end := start + size
		if end > len(texts) {
			end = len(texts)
		}
		batches = append(batches, texts[start:end])
	}
	return batches
}

// transientErr marks an error as retryable so withRetry will retry it.
type transientErr struct{ err error }

func (t transientErr) Error() string { return t.err.Error() }
func (t transientErr) Unwrap() error { return t.err }

// retryable wraps err so withRetry treats it as transient. Returns nil for nil.
func retryable(err error) error {
	if err == nil {
		return nil
	}
	return transientErr{err}
}

func isRetryable(err error) bool {
	var t transientErr
	return errors.As(err, &t)
}

// withRetry calls fn up to maxRetryAttempts times, retrying only errors marked
// retryable(), with a fixed backoff, honoring ctx cancellation.
func withRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		err = fn()
		if err == nil || !isRetryable(err) {
			return err
		}
		if attempt == maxRetryAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryBackoff):
		}
	}
	return err
}

// checkDims verifies every vector in vecs has dimension want (when want > 0),
// or discovers and returns the dimension from the first vector (when want ==
// 0). Returns ErrDimMismatch on any inconsistency.
func checkDims(vecs [][]float32, want int) (int, error) {
	for _, v := range vecs {
		if want == 0 {
			want = len(v)
		}
		if len(v) != want {
			return 0, fmt.Errorf("%w: got %d, want %d", ErrDimMismatch, len(v), want)
		}
	}
	return want, nil
}

// Probe embeds a short fixed string and returns the discovered dimension. Used
// by a future "test embedding connection" affordance and by tests.
func Probe(ctx context.Context, e Embedder) (int, error) {
	vecs, err := e.Embed(ctx, []string{"ping"})
	if err != nil {
		return 0, err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return 0, errors.New("embedding: probe returned no vector")
	}
	return len(vecs[0]), nil
}
