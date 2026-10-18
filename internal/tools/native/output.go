package native

import (
	"io"
	"sync"
	"unicode/utf8"
)

// BoundedOutput is a concurrency-safe writer that retains at most limit bytes
// and optionally forwards output to an observer. The default prefix mode
// retains and forwards the leading bytes and stops once the limit is reached.
// Tail mode retains the most recent bytes instead and forwards every incoming
// chunk, so an observer keeps receiving output after the capture cap.
type BoundedOutput struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	tail      bool
	truncated bool
	observer  io.Writer
}

// NewBoundedOutput creates a BoundedOutput that retains at most limit bytes of
// the prefix. If limit is non-positive the behavior is undefined; callers
// should use OutputLimit to normalize the value. observer may be nil. The
// internal buffer is pre-allocated with capacity min(limit, 32*1024) so a large
// user limit does not eagerly reserve memory.
func NewBoundedOutput(limit int, observer io.Writer) *BoundedOutput {
	return newBoundedOutput(limit, observer, false)
}

// NewBoundedOutputTail creates a BoundedOutput in tail mode: it retains the
// most recent limit bytes while forwarding every incoming chunk to observer.
// Managed commands use it so output keeps flowing to their observers after the
// capture cap, while the retained text stays bounded and never begins on a
// partial UTF-8 rune. limit and observer follow NewBoundedOutput's contract.
func NewBoundedOutputTail(limit int, observer io.Writer) *BoundedOutput {
	return newBoundedOutput(limit, observer, true)
}

// NewCommandOutput selects the retention mode for one command stream: tail
// mode when retainTail is set, prefix mode otherwise. limit and observer
// follow NewBoundedOutput's contract.
func NewCommandOutput(limit int, observer io.Writer, retainTail bool) *BoundedOutput {
	if retainTail {
		return NewBoundedOutputTail(limit, observer)
	}
	return NewBoundedOutput(limit, observer)
}

func newBoundedOutput(limit int, observer io.Writer, tail bool) *BoundedOutput {
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
		tail:     tail,
		observer: observer,
	}
}

// Write implements io.Writer. In prefix mode it retains and forwards only the
// prefix that fits within the limit; fully dropped writes (after the limit is
// reached) return len(p), nil without calling the observer. In tail mode it
// forwards every byte to the observer and retains only the most recent bytes.
// In both modes, when the observer accepts fewer bytes than supplied without
// an error, io.ErrShortWrite is returned.
func (b *BoundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.tail {
		b.retainTail(p)
		return b.forward(p)
	}

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

// retainTail keeps the most recent limit bytes of p in the internal buffer.
// A single chunk larger than the limit is trimmed before it is copied and older
// bytes are dropped before the new chunk is appended, so the retained
// allocation never grows with the incoming chunk. Only the truncation edge is
// repaired: the oldest bytes go first, so a multi-byte UTF-8 rune split by the
// window is dropped rather than returned half-encoded. The newest byte always
// comes from the end of a complete chunk, which is the stream's own boundary.
func (b *BoundedOutput) retainTail(p []byte) {
	if len(p) == 0 {
		return
	}
	if b.limit <= 0 {
		b.truncated = true
		return
	}
	if len(p) > b.limit {
		b.truncated = true
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
	} else {
		if excess := len(b.buf) + len(p) - b.limit; excess > 0 {
			b.truncated = true
			// copy handles the overlap between source and destination.
			b.buf = append(b.buf[:0], b.buf[excess:]...)
		}
		b.buf = append(b.buf, p...)
	}
	if trimmed := trimLeadingRuneFragment(b.buf); len(trimmed) < len(b.buf) {
		b.truncated = true
		b.buf = trimmed
	}
}

// forward hands the whole chunk to the observer, if any.
func (b *BoundedOutput) forward(p []byte) (int, error) {
	if b.observer == nil {
		return len(p), nil
	}
	written, err := b.observer.Write(p)
	if err != nil {
		return written, err
	}
	if written < len(p) {
		return written, io.ErrShortWrite
	}
	return len(p), nil
}

// String returns the retained output as a string. It is safe to call
// concurrently with Write. In tail mode the result is repaired to a UTF-8 rune
// boundary at both ends: the truncation edge (whose first bytes may be the
// remains of a dropped rune) and the capture edge (whose last bytes may be the
// start of a rune the next chunk completes). The internal buffer keeps the raw
// bytes, so a later Write can still complete a split trailing rune.
func (b *BoundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tail {
		return string(trimTrailingRuneFragment(trimLeadingRuneFragment(b.buf)))
	}
	return string(b.buf)
}

// Truncated returns true if any bytes have been dropped because the limit was
// exceeded (or, in tail mode, because they only completed a rune that no
// longer fits). It is safe to call concurrently with Write.
func (b *BoundedOutput) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// trimLeadingRuneFragment drops leading UTF-8 continuation bytes so retained
// text never starts in the middle of a multi-byte rune (which a consumer would
// render as a replacement character).
func trimLeadingRuneFragment(b []byte) []byte {
	i := 0
	for i < len(b) && !utf8.RuneStart(b[i]) {
		i++
	}
	if i == 0 {
		return b
	}
	return b[i:]
}

// trimTrailingRuneFragment drops a trailing incomplete or invalid rune so
// returned text stays valid UTF-8. A complete rune — including an explicitly
// encoded U+FFFD, which DecodeRune reports with size > 1 — is left alone.
// Normal text stops scanning within utf8.UTFMax bytes of the end; the wider
// bound only matters when the tail is malformed.
func trimTrailingRuneFragment(b []byte) []byte {
	for back := 1; back <= len(b); back++ {
		i := len(b) - back
		if !utf8.RuneStart(b[i]) {
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			return b[:i] // invalid trailing encoding
		}
		if size == back {
			return b // the final rune reaches the end and is complete
		}
		return b[:i+size] // a later rune's start byte is incomplete
	}
	return b
}

// OutputLimit returns defaultMaxOutputBytes for non-positive input, otherwise
// returns limit unchanged.
func OutputLimit(limit int) int {
	if limit <= 0 {
		return defaultMaxOutputBytes
	}
	return limit
}
