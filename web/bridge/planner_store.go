package bridge

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The planner store is the durable half of the planner service: one JSON
// record per planner and one append-only JSONL journal of its events, in a
// private directory under the bridge's state directory.
//
// Every record write is atomic — a temp file in the same directory, fsynced,
// renamed into place — so a bridge that dies mid-write leaves the previous
// record intact rather than a truncated one. Journal appends are not atomic
// per line and do not need to be: a torn final line is skipped on read.
//
// Nothing here stores a secret. A record names paths the bridge owns, IDs it
// minted, and the decisions a browser already made; no environment, no
// credential and no prompt text is written to disk.
const (
	// plannerDirName is the subdirectory of the state directory that holds
	// planner records and their journals.
	plannerDirName = "planners"
	// plannerEventRetained bounds the journal: older events are shed so a long
	// session cannot grow it without limit.
	plannerEventRetained = 5000
	// plannerEventPayloadLimit bounds one event's data. A longer payload is
	// stored as a truncation marker; it never fails the turn that produced it.
	plannerEventPayloadLimit = 64 << 10
	// plannerIDLength is 16 random bytes in lowercase hex.
	plannerIDLength = 32
	// plannerFileMode and plannerDirMode keep planner state private to the
	// user who runs the bridge.
	plannerFileMode = 0o600
	plannerDirMode  = 0o700
)

// plannerIDPattern is the only shape a planner or workspace ID may take. A
// caller-supplied ID is checked against it before it can become a path
// component, so no request can name a file outside the planner directory.
var plannerIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// validPlannerID reports whether id is an ID this bridge could have minted.
func validPlannerID(id string) bool {
	return len(id) == plannerIDLength && plannerIDPattern.MatchString(id)
}

// newPlannerID mints one planner or workspace ID: 16 bytes from crypto/rand in
// lowercase hex. IDs are never derived from a caller's input.
func newPlannerID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate planner id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// plannerStore owns planner state on disk. It is safe for concurrent use: the
// journal line counts are guarded, and record writes are independent files.
type plannerStore struct {
	dir string

	mu    sync.Mutex
	lines map[string]int
}

func newPlannerStore(stateDir string) *plannerStore {
	return &plannerStore{dir: filepath.Join(stateDir, plannerDirName), lines: map[string]int{}}
}

// recordPath validates id and returns the record's path. A caller-supplied ID
// that is not a planner ID is refused here rather than used as a filename.
func (s *plannerStore) recordPath(id string) (string, error) {
	if !validPlannerID(id) {
		return "", fmt.Errorf("%w: %q is not a planner id", errPlannerInvalid, id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

func (s *plannerStore) journalPath(id string) (string, error) {
	if !validPlannerID(id) {
		return "", fmt.Errorf("%w: %q is not a planner id", errPlannerInvalid, id)
	}
	return filepath.Join(s.dir, id+".events.log"), nil
}

// read loads one record. A missing record is ErrPlannerNotFound; an unreadable
// or malformed one is unavailable, never "no planner", because a corrupt
// record must not silently free the workspace it may still own.
func (s *plannerStore) read(id string) (PlannerRecord, error) {
	path, err := s.recordPath(id)
	if err != nil {
		return PlannerRecord{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PlannerRecord{}, fmt.Errorf("%w: %s", errPlannerNotFound, id)
		}
		return PlannerRecord{}, fmt.Errorf("%w: read planner record: %v", errPlannerUnavailable, err)
	}
	var rec PlannerRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return PlannerRecord{}, fmt.Errorf("%w: decode planner record %s: %v", errPlannerUnavailable, id, err)
	}
	if rec.ID != id {
		return PlannerRecord{}, fmt.Errorf("%w: planner record %s names %q", errPlannerUnavailable, id, rec.ID)
	}
	return rec, nil
}

// write persists one record atomically. The directory is created private; the
// file is fsynced and renamed, so a reader sees either the old record or the
// new one.
func (s *plannerStore) write(rec PlannerRecord) error {
	path, err := s.recordPath(rec.ID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("%w: encode planner record: %v", errPlannerUnavailable, err)
	}
	if err := os.MkdirAll(s.dir, plannerDirMode); err != nil {
		return fmt.Errorf("%w: create planner directory: %v", errPlannerUnavailable, err)
	}
	return plannerWriteAtomic(path, data)
}

// plannerWriteAtomic writes data to path through a fsynced temp file in the
// same directory, then renames it and fsyncs the directory. A failure removes
// the temp file and leaves whatever was there before untouched.
func plannerWriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".planner-*")
	if err != nil {
		return fmt.Errorf("%w: create temp file: %v", errPlannerUnavailable, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("%w: %v", errPlannerUnavailable, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(plannerFileMode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: %v", errPlannerUnavailable, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: replace %s: %v", errPlannerUnavailable, filepath.Base(path), err)
	}
	// The rename is the visible step; syncing the directory is what makes it
	// survive a power loss.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// list loads every readable record, ordered by ID. An unreadable record is
// skipped: it cannot claim a workspace it cannot be read from.
func (s *plannerStore) list() ([]PlannerRecord, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: read planner directory: %v", errPlannerUnavailable, err)
	}
	out := make([]PlannerRecord, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !validPlannerID(id) {
			continue
		}
		rec, err := s.read(id)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// appendEvent journals one event and enforces both bounds: a payload over the
// per-event limit is replaced by a marker, and the journal keeps only the
// newest plannerEventRetained events. Neither case fails the caller.
func (s *plannerStore) appendEvent(id string, ev PlannerEvent) error {
	path, err := s.journalPath(id)
	if err != nil {
		return err
	}
	if len(ev.Data) > plannerEventPayloadLimit {
		marker, mErr := json.Marshal(map[string]any{
			"truncated": true,
			"kind":      ev.Kind,
			"bytes":     len(ev.Data),
			"limit":     plannerEventPayloadLimit,
		})
		if mErr != nil {
			return fmt.Errorf("%w: encode truncation marker: %v", errPlannerUnavailable, mErr)
		}
		ev.Data = marker
		ev.Truncated = true
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("%w: encode planner event: %v", errPlannerUnavailable, err)
	}
	line = append(line, '\n')
	if err := os.MkdirAll(s.dir, plannerDirMode); err != nil {
		return fmt.Errorf("%w: create planner directory: %v", errPlannerUnavailable, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, plannerFileMode)
	if err != nil {
		return fmt.Errorf("%w: open planner journal: %v", errPlannerUnavailable, err)
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: append planner event: %v", errPlannerUnavailable, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: sync planner journal: %v", errPlannerUnavailable, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("%w: close planner journal: %v", errPlannerUnavailable, err)
	}
	s.mu.Lock()
	count, ok := s.lines[id]
	s.mu.Unlock()
	if !ok {
		counted, cErr := s.countEvents(path)
		if cErr != nil {
			return cErr
		}
		count = counted
	}
	count++
	s.mu.Lock()
	s.lines[id] = count
	s.mu.Unlock()
	if count <= plannerEventRetained {
		return nil
	}
	return s.compactJournal(id, path)
}

// compactJournal rewrites the journal with only its newest
// plannerEventRetained events. It runs after the append that exceeded the cap,
// so a reader between the two sees more events than the bound, never fewer.
func (s *plannerStore) compactJournal(id, path string) error {
	events, err := s.readJournal(path)
	if err != nil {
		return err
	}
	if len(events) <= plannerEventRetained {
		s.mu.Lock()
		s.lines[id] = len(events)
		s.mu.Unlock()
		return nil
	}
	kept := events[len(events)-plannerEventRetained:]
	var buf bytes.Buffer
	for _, ev := range kept {
		line, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("%w: encode planner event: %v", errPlannerUnavailable, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := plannerWriteAtomic(path, buf.Bytes()); err != nil {
		return err
	}
	s.mu.Lock()
	s.lines[id] = len(kept)
	s.mu.Unlock()
	return nil
}

func (s *plannerStore) countEvents(path string) (int, error) {
	events, err := s.readJournal(path)
	if err != nil {
		return 0, err
	}
	return len(events), nil
}

// readJournal parses the journal in order. A line that does not decode — the
// torn tail of a crashed append — is skipped rather than failing the replay.
func (s *plannerStore) readJournal(path string) ([]PlannerEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: read planner journal: %v", errPlannerUnavailable, err)
	}
	out := make([]PlannerEvent, 0, bytes.Count(data, []byte("\n")))
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev PlannerEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Sequence == 0 || ev.Kind == "" {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// events replays the journal from just after sequence after. An empty result
// is a valid answer: nothing newer has happened.
func (s *plannerStore) events(id string, after uint64) ([]PlannerEvent, error) {
	path, err := s.journalPath(id)
	if err != nil {
		return nil, err
	}
	events, err := s.readJournal(path)
	if err != nil {
		return nil, err
	}
	out := make([]PlannerEvent, 0, len(events))
	for _, ev := range events {
		if ev.Sequence > after {
			out = append(out, ev)
		}
	}
	return out, nil
}

// lastSequence is the highest sequence the journal holds, which is where the
// next planner event continues from.
func (s *plannerStore) lastSequence(id string) (uint64, error) {
	path, err := s.journalPath(id)
	if err != nil {
		return 0, err
	}
	events, err := s.readJournal(path)
	if err != nil {
		return 0, err
	}
	var last uint64
	for _, ev := range events {
		if ev.Sequence > last {
			last = ev.Sequence
		}
	}
	return last, nil
}

// plannerTextHash is the digest a turn keeps of the text its request ID was
// used for, so a replay is recognised without storing the prompt twice.
func plannerTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// plannerHeartbeatWindow is how recent a record's heartbeat must be for its
// owner to count as live. An older one is unknown, never free.
const plannerHeartbeatWindow = 90 * time.Second

// plannerRecordLive reports whether rec still has a live owner: this process
// wrote it and refreshed its heartbeat recently. A record from another bridge
// generation is not live, which is what makes recovery a rewrite rather than a
// resume.
func plannerRecordLive(rec PlannerRecord, now time.Time) bool {
	if rec.State != plannerStateActive || rec.PID != os.Getpid() {
		return false
	}
	return now.Sub(rec.Heartbeat) < plannerHeartbeatWindow
}
