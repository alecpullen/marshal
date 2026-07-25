package sdd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// Progress is the append-only event log for the SDD pipeline. The zero value
// is ready to use; the file lives at <ws.ProgressPath()> (per the Workspace
// helper added in P2 Task 2).
type Progress struct{}

// Append writes one event line to progress.md in the format
// `<ISO8601-UTC> <TASK_ID> <EVENT> key=value key=value ...`. `kv` is a flat
// list of alternating key/value strings. If `len(kv)` is odd, the trailing
// key is dropped (defensive — the controller will always pass pairs).
func (p *Progress) Append(ws *Workspace, taskID, event string, kv ...string) error {
	if _, err := ws.Ensure(); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(time.Now().UTC().Format(time.RFC3339))
	b.WriteByte(' ')
	b.WriteString(taskID)
	b.WriteByte(' ')
	b.WriteString(event)
	// Drop trailing key if odd.
	pairs := kv
	if len(pairs)%2 != 0 {
		pairs = pairs[:len(pairs)-1]
	}
	for i := 0; i < len(pairs); i += 2 {
		b.WriteByte(' ')
		b.WriteString(pairs[i])
		b.WriteByte('=')
		b.WriteString(pairs[i+1])
	}
	b.WriteByte('\n')

	f, err := os.OpenFile(ws.ProgressPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("sdd progress: open: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("sdd progress: write: %w", err)
	}
	return nil
}

// Read returns one entry per non-empty line in progress.md. Returns
// (nil, nil) if the file does not exist. Per-line parsing (timestamp, taskID,
// event, kv pairs) is the controller's job in P4.
func (p *Progress) Read(ws *Workspace) ([]string, error) {
	f, err := os.Open(ws.ProgressPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sdd progress: read: %w", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sdd progress: scan: %w", err)
	}
	return out, nil
}

// Tail returns the last n lines of progress.md. If the file has fewer than
// n lines, all lines are returned. If the file is missing, returns
// (nil, nil). Used by health-loop detection in P3 and by postmortems.
func (p *Progress) Tail(ws *Workspace, n int) ([]string, error) {
	all, err := p.Read(ws)
	if err != nil {
		return nil, err
	}
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}
