package streaming

import (
	"bufio"
	"io"
	"strings"
)

// Event is one parsed SSE event. Data joins multiple `data:` lines (per the
// SSE spec, consecutive data: lines within one event are newline-joined).
type Event struct {
	ID    string
	Event string
	Data  string
}

// Decoder reads Server-Sent Events from r. It knows nothing about the
// payload format inside Data — callers decide how to interpret it.
type Decoder struct {
	scanner *bufio.Scanner
	event   Event
	err     error
}

func NewDecoder(r io.Reader) *Decoder {
	scanner := bufio.NewScanner(r)
	// Some providers emit large single-line chunks; raise the buffer above
	// bufio's 64KB default line limit.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	return &Decoder{scanner: scanner}
}

// Next advances to the next event. Returns false at EOF or on read error;
// check Err() to distinguish the two.
func (d *Decoder) Next() bool {
	d.event = Event{}
	var dataLines []string
	sawAny := false

	for d.scanner.Scan() {
		line := d.scanner.Text()
		if line == "" {
			if sawAny {
				d.event.Data = strings.Join(dataLines, "\n")
				return true
			}
			continue
		}
		sawAny = true
		switch {
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, "event:"):
			d.event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "id:"):
			d.event.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, ":"):
			// comment/heartbeat line — ignore
		}
	}

	if err := d.scanner.Err(); err != nil {
		d.err = err
		return false
	}
	if sawAny {
		d.event.Data = strings.Join(dataLines, "\n")
		return true
	}
	return false
}

func (d *Decoder) Event() Event { return d.event }
func (d *Decoder) Err() error   { return d.err }
