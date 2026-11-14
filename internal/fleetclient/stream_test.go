package fleetclient

import (
	"io"
	"strings"
	"testing"
)

func TestTheParserJoinsDataLinesAndSkipsHeartbeats(t *testing.T) {
	const raw = ": hb\n" +
		"\n" +
		"id: 7\n" +
		"data: {\"kind\":\"activity\",\n" +
		"data: \"activity\":\"x\"}\n" +
		"\n" +
		": another heartbeat, between two frames\n" +
		"\n" +
		"id: 8\n" +
		"data: {\"kind\":\"run\"}\n" +
		"\n"

	reader := newEventReader(strings.NewReader(raw))

	first, err := reader.next()
	if err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if first.ID != "7" {
		t.Errorf("first frame's ID = %q, want 7", first.ID)
	}
	if got, want := string(first.Data), "{\"kind\":\"activity\",\n\"activity\":\"x\"}"; got != want {
		t.Errorf("first frame's data = %q, want %q", got, want)
	}
	if reader.lastID != "7" {
		t.Errorf("lastID = %q, want 7", reader.lastID)
	}

	second, err := reader.next()
	if err != nil {
		t.Fatalf("second frame: %v", err)
	}
	if second.ID != "8" {
		t.Errorf("second frame's ID = %q, want 8", second.ID)
	}
	if got, want := string(second.Data), `{"kind":"run"}`; got != want {
		t.Errorf("second frame's data = %q, want %q", got, want)
	}
	if reader.lastID != "8" {
		t.Errorf("lastID = %q, want 8", reader.lastID)
	}

	if _, err := reader.next(); err != io.EOF {
		t.Fatalf("next after the last frame = %v, want io.EOF", err)
	}
}

// TestTheParserKeepsTheLastID checks the overflow nudge, which carries no id
// line, does not lose the position a reconnect resumes from.
func TestTheParserKeepsTheLastID(t *testing.T) {
	const raw = "id: 12\ndata: {\"kind\":\"activity\"}\n\n" +
		"data: {\"type\":\"replay_overflow\"}\n\n"

	reader := newEventReader(strings.NewReader(raw))

	first, err := reader.next()
	if err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if first.ID != "12" {
		t.Errorf("first frame's ID = %q, want 12", first.ID)
	}

	nudge, err := reader.next()
	if err != nil {
		t.Fatalf("overflow frame: %v", err)
	}
	if nudge.ID != "" {
		t.Errorf("overflow frame's ID = %q, want none", nudge.ID)
	}
	if got, want := string(nudge.Data), `{"type":"replay_overflow"}`; got != want {
		t.Errorf("overflow frame's data = %q, want %q", got, want)
	}
	if reader.lastID != "12" {
		t.Errorf("lastID = %q, want 12: a frame with no id does not move it", reader.lastID)
	}
}

// TestTheParserEmitsTheLastFrameWithoutABlankLine checks a stream that ends
// right after a frame still delivers it.
func TestTheParserEmitsTheLastFrameWithoutABlankLine(t *testing.T) {
	reader := newEventReader(strings.NewReader("id: 3\ndata: {\"kind\":\"run\"}"))

	frame, err := reader.next()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if frame.ID != "3" || string(frame.Data) != `{"kind":"run"}` {
		t.Fatalf("frame = %+v, want id 3 with the run payload", frame)
	}
	if _, err := reader.next(); err != io.EOF {
		t.Fatalf("next = %v, want io.EOF", err)
	}
}

// TestTheFrameSourceHoldsFramesUntilTaken checks the buffer the connect drains
// after its reads.
func TestTheFrameSourceHoldsFramesUntilTaken(t *testing.T) {
	src := newFrameSource()
	go src.read(strings.NewReader(
		"id: 1\ndata: {\"kind\":\"activity\"}\n\n" +
			"id: 2\ndata: {\"kind\":\"run\"}\n\n",
	))

	var frames []eventFrame
	for len(frames) < 2 {
		if err := src.wait(t.Context()); err != nil {
			t.Fatalf("wait: %v", err)
		}
		more, done, err := src.take()
		if err != nil {
			t.Fatalf("take: %v", err)
		}
		frames = append(frames, more...)
		if done {
			return
		}
	}
}
