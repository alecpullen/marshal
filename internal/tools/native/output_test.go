package native

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestBoundedOutputRetainsPrefixAndReportsTruncation(t *testing.T) {
	b := NewBoundedOutput(5, nil)
	n, err := b.Write([]byte("abc"))
	if n != 3 || err != nil {
		t.Fatalf("Write(abc) = (%d, %v), want (3, nil)", n, err)
	}
	if b.Truncated() {
		t.Fatal("Truncated() = true after first write that fits")
	}

	n, err = b.Write([]byte("def"))
	if n != 3 || err != nil {
		t.Fatalf("Write(def) = (%d, %v), want (3, nil)", n, err)
	}
	if !b.Truncated() {
		t.Fatal("Truncated() = false after overflow")
	}
	if got := b.String(); got != "abcde" {
		t.Fatalf("String() = %q, want %q", got, "abcde")
	}
}

func TestBoundedOutputForwardsOnlyRetainedBytes(t *testing.T) {
	var obs bytes.Buffer
	b := NewBoundedOutput(5, &obs)
	_, _ = b.Write([]byte("abcdefgh"))
	if got := b.String(); got != "abcde" {
		t.Fatalf("String() = %q, want %q", got, "abcde")
	}
	if obs.String() != "abcde" {
		t.Fatalf("observer content = %q, want %q", obs.String(), "abcde")
	}
}

func TestBoundedOutputConcurrentSnapshot(t *testing.T) {
	b := NewBoundedOutput(100, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = b.Write([]byte("hello world\n"))
				_ = b.String()
				_ = b.Truncated()
			}
		}()
	}
	wg.Wait()
	if len(b.String()) > 100 {
		t.Fatalf("String() len = %d, want <= 100", len(b.String()))
	}
}

func TestOutputLimitUsesDefault(t *testing.T) {
	if got := OutputLimit(-1); got != defaultMaxOutputBytes {
		t.Fatalf("OutputLimit(-1) = %d, want %d", got, defaultMaxOutputBytes)
	}
	if got := OutputLimit(0); got != defaultMaxOutputBytes {
		t.Fatalf("OutputLimit(0) = %d, want %d", got, defaultMaxOutputBytes)
	}
	if got := OutputLimit(5000); got != 5000 {
		t.Fatalf("OutputLimit(5000) = %d, want 5000", got)
	}
}

func TestBoundedOutputEmptyWrite(t *testing.T) {
	b := NewBoundedOutput(5, nil)
	n, err := b.Write(nil)
	if n != 0 || err != nil {
		t.Fatalf("Write(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if b.Truncated() {
		t.Fatal("Truncated() = true after empty write")
	}
}

func TestBoundedOutputTailKeepsRecentBytes(t *testing.T) {
	b := NewBoundedOutputTail(5, nil)
	for _, chunk := range []string{"abc", "def", "ghi"} {
		n, err := b.Write([]byte(chunk))
		if n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = (%d, %v), want (%d, nil)", chunk, n, err, len(chunk))
		}
	}
	if got := b.String(); got != "efghi" {
		t.Fatalf("String() = %q, want %q", got, "efghi")
	}
	if !b.Truncated() {
		t.Fatal("Truncated() = false after dropping older bytes")
	}
	if len(b.String()) > 5 {
		t.Fatalf("String() len = %d, want <= 5", len(b.String()))
	}
}

func TestBoundedOutputTailForwardsEveryChunkAfterCap(t *testing.T) {
	var obs bytes.Buffer
	b := NewBoundedOutputTail(4, &obs)
	for _, chunk := range []string{"aaaa", "bbbb", "late-marker"} {
		if _, err := b.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write(%q) returned %v", chunk, err)
		}
	}
	if obs.String() != "aaaabbbblate-marker" {
		t.Fatalf("observer content = %q, want every chunk forwarded", obs.String())
	}
	if got := b.String(); got != "rker" {
		t.Fatalf("String() = %q, want the most recent %d bytes", got, 4)
	}
}

func TestBoundedOutputTailSingleOversizedWrite(t *testing.T) {
	obs := &countingWriter{}
	b := NewBoundedOutputTail(8, obs)
	// 64 KiB in one Write must stay bounded without retaining the whole chunk.
	big := bytes.Repeat([]byte("0123456789"), 6400)
	for i := range big {
		if i%7 == 0 {
			big[i] = 'x'
		}
	}
	n, err := b.Write(big)
	if n != len(big) || err != nil {
		t.Fatalf("Write(big) = (%d, %v), want (%d, nil)", n, err, len(big))
	}
	if got := b.String(); got != string(big[len(big)-8:]) {
		t.Fatalf("String() = %q, want the last 8 bytes", got)
	}
	if !b.Truncated() {
		t.Fatal("Truncated() = false after an oversized write")
	}
	if obs.n != len(big) {
		t.Fatalf("observer received %d bytes, want %d", obs.n, len(big))
	}
}

func TestBoundedOutputTailAvoidsSplitRunes(t *testing.T) {
	b := NewBoundedOutputTail(6, nil)
	if _, err := b.Write([]byte("éé")); err != nil { // 4 bytes: two 2-byte runes
		t.Fatalf("Write returned %v", err)
	}
	if got := b.String(); got != "éé" {
		t.Fatalf("String() = %q, want %q", got, "éé")
	}
	if _, err := b.Write([]byte("é")); err != nil { // exactly fills the limit
		t.Fatalf("Write returned %v", err)
	}
	if got := b.String(); got != "ééé" {
		t.Fatalf("String() = %q, want %q", got, "ééé")
	}
	// A chunk boundary can land mid-rune: the retained text must still be
	// valid UTF-8, and the completing chunk must put the rune back.
	if _, err := b.Write([]byte{0xC3}); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	got := b.String()
	if !utf8.ValidString(got) {
		t.Fatalf("String() = %q, want valid UTF-8", got)
	}
	if got != "éé" {
		t.Fatalf("String() = %q, want %q while a rune is incomplete", got, "éé")
	}
	if _, err := b.Write([]byte{0xA9}); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	if got := b.String(); got != "ééé" {
		t.Fatalf("String() = %q, want %q once the rune completes", got, "ééé")
	}
	if !b.Truncated() {
		t.Fatal("Truncated() = false after trimming split runes")
	}
}

// TestBoundedOutputTailNeverReturnsBrokenUTF8 covers the defensive case: a
// buffer whose tail is nothing but continuation bytes must still stringify to
// valid UTF-8, even if that means returning less than the retained bytes.
func TestBoundedOutputTailNeverReturnsBrokenUTF8(t *testing.T) {
	b := NewBoundedOutputTail(4, nil)
	if _, err := b.Write([]byte{0xA9, 0xA9, 0xA9, 0xA9}); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	if got := b.String(); !utf8.ValidString(got) {
		t.Fatalf("String() = %q, want valid UTF-8", got)
	}
	// A byte that is neither a start nor a continuation is a complete (if
	// invalid) rune; it must not make String() panic or loop.
	if _, err := b.Write([]byte{0xFF}); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	if got := b.String(); !utf8.ValidString(got) {
		t.Fatalf("String() = %q, want valid UTF-8", got)
	}
}

func TestBoundedOutputTailForwardErrorPropagates(t *testing.T) {
	sentinel := errors.New("observer failed")
	b := NewBoundedOutputTail(4, failingWriter{err: sentinel})
	n, err := b.Write([]byte("late"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Write error = %v, want %v", err, sentinel)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0 from a failing observer", n)
	}
}

func TestBoundedOutputTailShortWriteReportsErrShortWrite(t *testing.T) {
	b := NewBoundedOutputTail(4, &shortWriter{limit: 2})
	n, err := b.Write([]byte("late"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Write error = %v, want io.ErrShortWrite", err)
	}
	if n != 2 {
		t.Fatalf("Write n = %d, want 2", n)
	}
}

func TestBoundedOutputTailConcurrentReadWrite(t *testing.T) {
	b := NewBoundedOutputTail(64, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = b.Write([]byte("hello world\n"))
				_ = b.String()
				_ = b.Truncated()
			}
		}()
	}
	wg.Wait()
	got := b.String()
	if len(got) > 64 {
		t.Fatalf("String() len = %d, want <= 64", len(got))
	}
	if !strings.HasSuffix(got, "hello world\n") {
		t.Fatalf("String() = %q, want it to end with the last written chunk", got)
	}
}

func TestNewCommandOutputSelectsRetentionMode(t *testing.T) {
	for _, tt := range []struct {
		name      string
		retain    bool
		wantFirst string
	}{
		{name: "prefix", retain: false, wantFirst: "abcd"},
		{name: "tail", retain: true, wantFirst: "efgh"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var obs bytes.Buffer
			b := NewCommandOutput(4, &obs, tt.retain)
			if _, err := b.Write([]byte("abcd")); err != nil {
				t.Fatalf("Write returned %v", err)
			}
			if _, err := b.Write([]byte("efgh")); err != nil {
				t.Fatalf("Write returned %v", err)
			}
			if got := b.String(); got != tt.wantFirst {
				t.Fatalf("String() = %q, want %q", got, tt.wantFirst)
			}
			if tt.retain && obs.String() != "abcdefgh" {
				t.Fatalf("observer content = %q, want all bytes in tail mode", obs.String())
			}
			if !tt.retain && obs.String() != "abcd" {
				t.Fatalf("observer content = %q, want prefix only", obs.String())
			}
		})
	}
}

// countingWriter counts accepted bytes.
type countingWriter struct{ n int }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

// failingWriter always fails, returning no bytes.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// shortWriter accepts at most limit bytes without an error.
type shortWriter struct{ limit int }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		return w.limit, nil
	}
	return len(p), nil
}
