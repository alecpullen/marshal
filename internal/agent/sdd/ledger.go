package sdd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LedgerEntry is one line in the progress ledger.
type LedgerEntry struct {
	TaskNumber int
	BaseSHA    string
	HeadSHA    string
}

// Ledger is the durable progress record on disk. It records completed
// tasks so the orchestrator can resume after compaction without
// re-dispatching completed tasks.
type Ledger struct {
	path string
}

func NewLedger(ws *Workspace) *Ledger {
	return &Ledger{path: filepath.Join(ws.dir, "progress.md")}
}

// Read parses the ledger file. Returns an empty slice if the file does
// not exist or is empty.
func (l *Ledger) Read() []LedgerEntry {
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var entries []LedgerEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		entry := parseLedgerLine(line)
		if entry.TaskNumber > 0 {
			entries = append(entries, entry)
		}
	}
	return entries
}

// CompletedTasks returns a set of task numbers the ledger marks complete.
func (l *Ledger) CompletedTasks() map[int]bool {
	entries := l.Read()
	out := make(map[int]bool, len(entries))
	for _, e := range entries {
		out[e.TaskNumber] = true
	}
	return out
}

// Append writes one ledger line.
func (l *Ledger) Append(entry LedgerEntry) error {
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("sdd ledger: mkdir: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("sdd ledger: open: %w", err)
	}
	defer f.Close()
	line := fmt.Sprintf("Task %d: complete (commits %s..%s, review clean)\n",
		entry.TaskNumber, entry.BaseSHA, entry.HeadSHA)
	_, err = f.WriteString(line)
	return err
}

func parseLedgerLine(line string) LedgerEntry {
	// Expected format: "Task N: complete (commits BASE..HEAD, review clean)"
	if !strings.HasPrefix(line, "Task ") {
		return LedgerEntry{}
	}
	rest := strings.TrimPrefix(line, "Task ")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) < 2 {
		return LedgerEntry{}
	}
	num, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return LedgerEntry{}
	}
	entry := LedgerEntry{TaskNumber: num}
	// Extract commits BASE..HEAD from the parenthesised clause.
	commitIdx := strings.Index(parts[1], "commits ")
	if commitIdx >= 0 {
		rest := parts[1][commitIdx+8:]
		endIdx := strings.Index(rest, ",")
		if endIdx < 0 {
			endIdx = strings.Index(rest, ")")
		}
		if endIdx >= 0 {
			rest = rest[:endIdx]
		}
		dotIdx := strings.Index(rest, "..")
		if dotIdx >= 0 {
			entry.BaseSHA = rest[:dotIdx]
			entry.HeadSHA = rest[dotIdx+2:]
		}
	}
	return entry
}
