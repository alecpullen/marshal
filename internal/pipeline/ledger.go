package pipeline

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// completeLineRe matches the ledger's completion line. The exact format is
// a global constraint: other tooling and the human both read it.
var completeLineRe = regexp.MustCompile(`^Task (\d+): complete \(commits `)

const minorPrefix = " (minor): "

// Ledger is the durable record of a run's progress. It survives context
// compaction and process death; after either, it — not memory — is the
// authority on which tasks are done.
type Ledger struct {
	Path string
}

// MarkComplete records a finished, reviewed task. base and head are full
// SHAs; the line carries their 7-character short forms.
func (l Ledger) MarkComplete(task int, base, head string) error {
	return l.Note("Task %d: complete (commits %s..%s, review clean)", task, short(base), short(head))
}

// RecordMinor records a Minor review finding for the end-of-run roll-up.
func (l Ledger) RecordMinor(task int, finding string) error {
	return l.Note("Task %d%s%s", task, minorPrefix, finding)
}

// Note appends one line to the ledger, creating it if absent.
func (l Ledger) Note(format string, args ...any) error {
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("pipeline ledger: open: %w", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, format+"\n", args...); err != nil {
		return fmt.Errorf("pipeline ledger: write: %w", err)
	}
	return nil
}

// CompletedTasks returns the set of task numbers the ledger marks complete.
// A missing ledger is an empty set, not an error: it means nothing has run.
func (l Ledger) CompletedTasks() (map[int]bool, error) {
	lines, err := l.lines()
	if err != nil {
		return nil, err
	}
	done := map[int]bool{}
	for _, line := range lines {
		if m := completeLineRe.FindStringSubmatch(line); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			done[n] = true
		}
	}
	return done, nil
}

// Minors returns the recorded Minor findings, in the order recorded. The
// controller hands this list to the final branch review so it can triage
// what must be fixed before merge.
func (l Ledger) Minors() ([]string, error) {
	lines, err := l.lines()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range lines {
		if strings.Contains(line, minorPrefix) {
			out = append(out, line)
		}
	}
	return out, nil
}

// Tail returns the last n non-empty lines.
func (l Ledger) Tail(n int) ([]string, error) {
	lines, err := l.lines()
	if err != nil {
		return nil, err
	}
	if len(lines) <= n {
		return lines, nil
	}
	return lines[len(lines)-n:], nil
}

func (l Ledger) lines() ([]string, error) {
	f, err := os.Open(l.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pipeline ledger: read: %w", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("pipeline ledger: scan: %w", err)
	}
	return out, nil
}

// short truncates a SHA to 7 characters, leaving shorter strings alone.
func short(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}
