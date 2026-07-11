package native

import (
	"io"
	"sync"
)

// BoundedOutput is a concurrency-safe writer that retains at most limit bytes
// and optionally forwards the retained prefix to an observer. After the limit
// is reached, subsequent writes are counted as truncated but never stored or
// forwarded.
type BoundedOutput struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
	observer  io.Writer
}

// NewBoundedOutput creates a BoundedOutput that retains at most limit bytes.
// If limit is non-positive the behavior is undefined; callers should use
// OutputLimit to normalize the value. observer may be nil. The internal
// buffer is pre-allocated with capacity min(limit, 32*1024) so a large user
// limit does not eagerly reserve memory.
func NewBoundedOutput(limit int, observer io.Writer) *BoundedOutput {
	cap := limit
	if cap > 32*1024 {
		cap = 32 * 1024
	}
	if cap < 0 {
		cap = 0
	}
	return &BoundedOutput{
		buf:      make([]byte, 0, cap),
		limit:    limit,
		observer: observer,
	}
}

// Write implements io.Writer. It retains and forwards only the prefix that
// fits within the limit. Observer errors are returned only for the forwarded
// prefix. Fully dropped writes (after the limit is reached) return len(p), nil
// without calling the observer. When the observer accepts fewer bytes than
// supplied without an error, io.ErrShortWrite is returned.
func (b *BoundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := b.limit - len(b.buf)
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}

	n := remaining
	if len(p) < n {
		n = len(p)
	}

	b.buf = append(b.buf, p[:n]...)

	if n < len(p) {
		b.truncated = true
	}

	if b.observer != nil {
		written, err := b.observer.Write(p[:n])
		if err != nil {
			return written, err
		}
		if written < n {
			return written, io.ErrShortWrite
		}
	}

	return len(p), nil
}

// String returns the retained output as a string. It is safe to call
// concurrently with Write.
func (b *BoundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// Truncated returns true if any bytes have been dropped because the limit was
// exceeded. It is safe to call concurrently with Write.
func (b *BoundedOutput) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// OutputLimit returns defaultMaxOutputBytes for non-positive input, otherwise
// returns limit unchanged.
func OutputLimit(limit int) int {
	if limit <= 0 {
		return defaultMaxOutputBytes
	}
	return limit
}
