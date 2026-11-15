package fleetclient

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
)

// eventFrame is one event read off the fleet stream.
type eventFrame struct {
	ID   string // the frame's own id line; "" for the overflow nudge
	Data []byte // the joined data lines
}

// eventReader parses one Server-Sent Events stream.
type eventReader struct {
	r *bufio.Reader
	// lastID is the last id line seen: the position a reconnect resumes from.
	lastID string
}

// newEventReader reads frames from r.
func newEventReader(r io.Reader) *eventReader {
	return &eventReader{r: bufio.NewReader(r)}
}

// next returns the stream's next event. It returns io.EOF when the stream ends.
//
// A frame is its `id:` and `data:` lines, several `data:` lines joined with
// newlines, ended by a blank line. A line starting with `:` is a heartbeat and
// is ignored.
func (e *eventReader) next() (eventFrame, error) {
	var (
		data    []string
		frameID string
	)
	for {
		line, err := e.r.ReadString('\n')
		if err != nil && err != io.EOF {
			return eventFrame{}, err
		}
		end := err == io.EOF

		text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch {
		case text == "":
			// A blank line ends the frame. A frame with no data carries
			// nothing to dispatch.
			if len(data) > 0 {
				return eventFrame{ID: frameID, Data: []byte(strings.Join(data, "\n"))}, nil
			}
		case strings.HasPrefix(text, ":"):
			// A heartbeat or comment.
		default:
			// A line with no colon is a field name with an empty value, as
			// the event stream format says.
			field, value, ok := strings.Cut(text, ":")
			if !ok {
				field, value = text, ""
			} else {
				value = strings.TrimPrefix(value, " ")
			}
			switch field {
			case "id":
				e.lastID, frameID = value, value
			case "data":
				data = append(data, value)
			}
		}

		if end {
			if len(data) > 0 {
				return eventFrame{ID: frameID, Data: []byte(strings.Join(data, "\n"))}, nil
			}
			return eventFrame{}, io.EOF
		}
	}
}

// frameSource reads the stream's frames in its own goroutine and holds them
// until the client takes them.
//
// Frames that arrive while the connect's reads are in flight wait here, so the
// client applies them after the reads, in arrival order.
type frameSource struct {
	mu      sync.Mutex
	pending []eventFrame
	err     error
	done    bool

	// ready signals that there may be something to take, or that the reader
	// is done. It holds one token, so a burst of frames costs one wake-up.
	ready chan struct{}
}

// newFrameSource is an empty source; read fills it.
func newFrameSource() *frameSource {
	return &frameSource{ready: make(chan struct{}, 1)}
}

// read parses frames until the stream ends, holding each for the client.
func (s *frameSource) read(r io.Reader) {
	reader := newEventReader(r)
	for {
		frame, err := reader.next()
		if err != nil {
			s.finish(err)
			return
		}
		s.mu.Lock()
		s.pending = append(s.pending, frame)
		s.mu.Unlock()
		s.notify()
	}
}

// finish records the stream's end.
func (s *frameSource) finish(err error) {
	s.mu.Lock()
	s.done = true
	if err != nil && err != io.EOF {
		s.err = err
	}
	s.mu.Unlock()
	s.notify()
}

// notify wakes the client without blocking.
func (s *frameSource) notify() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// take returns the frames buffered so far, clearing them, and reports whether
// the reader has finished and, if it failed, why. It does not wait.
func (s *frameSource) take() ([]eventFrame, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	frames := s.pending
	s.pending = nil
	return frames, s.done, s.err
}

// wait blocks until there may be more to take, or ctx ends.
func (s *frameSource) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ready:
		return nil
	}
}
