package streaming

import (
	"errors"
	"strings"
	"testing"
)

func TestDecoderSingleDataLine(t *testing.T) {
	d := NewDecoder(strings.NewReader("data: hello\n\n"))

	if !d.Next() {
		t.Fatalf("Next() = false, want true (err = %v)", d.Err())
	}
	got := d.Event()
	want := Event{Data: "hello"}
	if got != want {
		t.Fatalf("Event() = %#v, want %#v", got, want)
	}
}

func TestDecoderMultipleDataLinesJoinWithNewline(t *testing.T) {
	d := NewDecoder(strings.NewReader("data: line one\ndata: line two\ndata: line three\n\n"))

	if !d.Next() {
		t.Fatalf("Next() = false, want true (err = %v)", d.Err())
	}
	want := "line one\nline two\nline three"
	if got := d.Event().Data; got != want {
		t.Fatalf("Event().Data = %q, want %q", got, want)
	}
}

func TestDecoderParsesEventAndIDFields(t *testing.T) {
	d := NewDecoder(strings.NewReader("id: 42\nevent: message\ndata: hello\n\n"))

	if !d.Next() {
		t.Fatalf("Next() = false, want true (err = %v)", d.Err())
	}
	got := d.Event()
	want := Event{ID: "42", Event: "message", Data: "hello"}
	if got != want {
		t.Fatalf("Event() = %#v, want %#v", got, want)
	}
}

func TestDecoderIgnoresCommentLines(t *testing.T) {
	d := NewDecoder(strings.NewReader(": this is a heartbeat comment\ndata: hello\n: another comment\n\n"))

	if !d.Next() {
		t.Fatalf("Next() = false, want true (err = %v)", d.Err())
	}
	want := "hello"
	if got := d.Event().Data; got != want {
		t.Fatalf("Event().Data = %q, want %q", got, want)
	}
}

func TestDecoderCleanEOFAfterTerminatedEvent(t *testing.T) {
	d := NewDecoder(strings.NewReader("data: hello\n\n"))

	if !d.Next() {
		t.Fatalf("Next() = false on first call, want true (err = %v)", d.Err())
	}
	if d.Next() {
		t.Fatalf("Next() = true on second call, want false at EOF")
	}
	if d.Err() != nil {
		t.Fatalf("Err() = %v, want nil at clean EOF", d.Err())
	}
}

func TestDecoderCleanEOFWithNoTrailingBlankLine(t *testing.T) {
	// Input ends immediately after a data: line, with no terminating blank
	// line. The final event is still delivered (sawAny is true when the
	// scanner loop exits), and the subsequent call hits clean EOF.
	d := NewDecoder(strings.NewReader("data: hello"))

	if !d.Next() {
		t.Fatalf("Next() = false on first call, want true (err = %v)", d.Err())
	}
	if got := d.Event().Data; got != "hello" {
		t.Fatalf("Event().Data = %q, want %q", got, "hello")
	}
	if d.Next() {
		t.Fatalf("Next() = true on second call, want false at EOF")
	}
	if d.Err() != nil {
		t.Fatalf("Err() = %v, want nil at clean EOF", d.Err())
	}
}

func TestDecoderNoEventsCleanEOF(t *testing.T) {
	d := NewDecoder(strings.NewReader(""))

	if d.Next() {
		t.Fatalf("Next() = true, want false on empty input")
	}
	if d.Err() != nil {
		t.Fatalf("Err() = %v, want nil at clean EOF", d.Err())
	}
}

// errReader is an io.Reader that returns a fixed number of bytes followed by
// a non-EOF error.
type errReader struct {
	data []byte
	err  error
	sent bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, r.data)
		return n, nil
	}
	return 0, r.err
}

func TestDecoderReadErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom: connection reset")
	r := &errReader{data: []byte("data: hello\n"), err: wantErr}
	d := NewDecoder(r)

	if d.Next() {
		t.Fatalf("Next() = true, want false on read error")
	}
	if !errors.Is(d.Err(), wantErr) {
		t.Fatalf("Err() = %v, want %v", d.Err(), wantErr)
	}
}

func TestDecoderLargeDataLine(t *testing.T) {
	large := strings.Repeat("x", 100*1024) // 100KB, above bufio's 64KB default
	d := NewDecoder(strings.NewReader("data: " + large + "\n\n"))

	if !d.Next() {
		t.Fatalf("Next() = false, want true (err = %v)", d.Err())
	}
	if got := d.Event().Data; got != large {
		t.Fatalf("Event().Data length = %d, want %d (mismatch or truncation)", len(got), len(large))
	}
	if d.Err() != nil {
		t.Fatalf("Err() = %v, want nil", d.Err())
	}
}
