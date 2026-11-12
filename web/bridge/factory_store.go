package bridge

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	factoryRequestLimit = 2 << 20
	factoryImportLimit  = 1 << 20
	factoryRecordLimit  = 16 << 20
)

var (
	ErrFactoryConflict    = errors.New("factory conflict")
	ErrFactoryLocked      = errors.New("factory store already owned; confirm previous bridge stopped before removing owner.lock")
	ErrFactoryCorrupt     = errors.New("factory journal corrupt")
	ErrFactoryUnavailable = errors.New("factory store unavailable")
	ErrFactoryNotFound    = errors.New("factory item not found")
)

type factoryPayload struct {
	Format      uint64      `json:"format"`
	Sequence    uint64      `json:"sequence"`
	RequestID   string      `json:"requestId"`
	ItemID      string      `json:"itemId"`
	CommandHash string      `json:"commandHash"`
	Item        FactoryItem `json:"item"`
}
type factoryRecord struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}
type FactoryStore struct {
	mu          sync.Mutex
	file        *os.File
	lock        *os.File
	lockPath    string
	items       map[string]FactoryItem
	requests    map[string]factoryPayload
	seq         uint64
	unavailable bool
	closed      bool
	write       func([]byte) (int, error)
	sync        func() error
}

func factoryCopy[T any](v T) T      { b, _ := json.Marshal(v); var c T; _ = json.Unmarshal(b, &c); return c }
func factoryDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func factoryPlanHash(plan, spec string) string {
	var b bytes.Buffer
	for _, s := range []string{plan, spec} {
		_ = binary.Write(&b, binary.BigEndian, uint64(len(s)))
		b.WriteString(s)
	}
	return factoryDigest(b.Bytes())
}
func factoryCanonical(b []byte) ([]byte, error) {
	if len(b) > factoryRequestLimit {
		return nil, fmt.Errorf("request exceeds 2 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if e := d.Decode(&v); e != nil {
		return nil, e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return nil, fmt.Errorf("trailing command data")
	}
	return json.Marshal(v)
}
func OpenFactoryStore(dir string) (*FactoryStore, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	p := filepath.Join(dir, "owner.lock")
	l, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if os.IsExist(e) {
		return nil, ErrFactoryLocked
	}
	if e != nil {
		return nil, e
	}
	s := &FactoryStore{lock: l, lockPath: p, items: map[string]FactoryItem{}, requests: map[string]factoryPayload{}}
	fail := func(e error) (*FactoryStore, error) { s.Close(); return nil, e }
	token := make([]byte, 32)
	if _, e = rand.Read(token); e != nil {
		return fail(e)
	}
	if _, e = l.Write(token); e != nil {
		return fail(e)
	}
	if e = l.Sync(); e != nil {
		return fail(e)
	}
	s.file, e = os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return fail(e)
	}
	if e = s.replay(); e != nil {
		return fail(e)
	}
	if _, e = s.file.Seek(0, io.SeekEnd); e != nil {
		return fail(e)
	}
	// Persist the journal directory entry before acknowledging any mutation.
	d, err := os.Open(dir)
	if err != nil {
		return fail(err)
	}
	err = d.Sync()
	closeErr := d.Close()
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	s.write = s.file.Write
	s.sync = s.file.Sync
	return s, nil
}
func (s *FactoryStore) replay() error {
	// Read bounded records, so a corrupt or hostile journal cannot allocate without limit.
	var line []byte
	buf := make([]byte, 32<<10)
	var offset int64
	for {
		n, e := s.file.Read(buf)
		for _, c := range buf[:n] {
			line = append(line, c)
			if len(line) > factoryRecordLimit {
				return ErrFactoryCorrupt
			}
			if c != '\n' {
				continue
			}
			if e := s.replayRecord(line[:len(line)-1]); e != nil {
				return e
			}
			offset += int64(len(line))
			line = nil
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	if len(line) > 0 {
		if e := s.file.Truncate(offset); e != nil {
			return e
		}
		return s.file.Sync()
	}
	return nil
}
func (s *FactoryStore) replayRecord(line []byte) error {
	var r factoryRecord
	var p factoryPayload
	if json.Unmarshal(line, &r) != nil || factoryDigest(r.Payload) != r.Checksum || json.Unmarshal(r.Payload, &p) != nil {
		return ErrFactoryCorrupt
	}
	canonical, e := json.Marshal(p)
	if e != nil || !bytes.Equal(canonical, r.Payload) || p.Format != 1 || p.Sequence != s.seq+1 || p.RequestID == "" || p.ItemID != p.Item.ID || p.ItemID == "" {
		return ErrFactoryCorrupt
	}
	if _, ok := s.requests[p.RequestID]; ok {
		return ErrFactoryCorrupt
	}
	old := s.items[p.ItemID]
	if p.Item.Version != old.Version+1 || validateFactoryItem(p.Item) != nil {
		return ErrFactoryCorrupt
	}
	s.items[p.ItemID] = p.Item
	s.requests[p.RequestID] = p
	s.seq = p.Sequence
	return nil
}
func (s *FactoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var e error
	if s.file != nil {
		e = s.file.Close()
	}
	if s.lock != nil {
		a, ae := s.lock.Stat()
		b, be := os.Stat(s.lockPath)
		if ae == nil && be == nil && os.SameFile(a, b) {
			if x := os.Remove(s.lockPath); e == nil {
				e = x
			}
		}
		if x := s.lock.Close(); e == nil {
			e = x
		}
	}
	return e
}
func (s *FactoryStore) Get(id string) (FactoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.items[id]
	if !ok {
		return FactoryItem{}, ErrFactoryNotFound
	}
	return factoryCopy(i), nil
}
func (s *FactoryStore) List() []FactoryItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FactoryItem, 0, len(s.items))
	for _, i := range s.items {
		out = append(out, factoryCopy(i))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}
func factoryNormalize(r *FactoryCreate) error {
	if strings.TrimSpace(r.RequestID) == "" || strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.Plan) == "" || strings.TrimSpace(r.Config.RepoID) == "" {
		return fmt.Errorf("requestId, title, plan and repository required")
	}
	if len(r.Plan)+len(r.Spec) > factoryImportLimit {
		return fmt.Errorf("plan/spec exceed 1 MiB")
	}
	c := &r.Config
	if c.Native && c.Workspace != "" {
		return fmt.Errorf("native and workspace are mutually exclusive")
	}
	if c.HostID == "" {
		c.HostID = "local"
	}
	if c.HostID != "local" {
		return fmt.Errorf("factory requires local host")
	}
	if c.MaxRounds < 0 || c.CIPollSeconds < 0 || c.CIWaitSeconds < 0 {
		return fmt.Errorf("limits must be positive")
	}
	if c.MaxRounds == 0 {
		c.MaxRounds = 5
	}
	if c.CIPollSeconds == 0 {
		c.CIPollSeconds = 15
	}
	if c.CIWaitSeconds == 0 {
		c.CIWaitSeconds = 1800
	}
	for _, k := range c.ExpectedChecks {
		if k.Name == "" || (k.Kind != "check_run" && k.Kind != "commit_status") || (k.Kind == "check_run" && k.AppID <= 0) || (k.Kind == "commit_status" && k.AppID != 0) {
			return fmt.Errorf("invalid check identity")
		}
	}
	return nil
}
func validateFactoryItem(i FactoryItem) error {
	switch i.State {
	case "draft", "queued", "preparing", "implementing", "checking", "reviewing", "repairing", "needs_input", "stopping", "stopped", "recovering", "ready", "closed":
	default:
		return fmt.Errorf("invalid state")
	}
	r := i.Draft
	if e := factoryNormalize(&r); e != nil {
		return e
	}
	unsettled := 0
	for _, e := range i.Effects {
		switch e.Status {
		case "prepared", "dispatched", "uncertain":
			unsettled++
		case "completed":
		default:
			return fmt.Errorf("invalid effect status")
		}
	}
	if unsettled > 1 {
		return fmt.Errorf("multiple unsettled effects")
	}
	for n, a := range i.Approvals {
		if a.Revision != uint64(n+1) || strings.TrimSpace(a.Plan) == "" {
			return fmt.Errorf("invalid approval revision/plan")
		}
		ar := FactoryCreate{RequestID: "approval", Title: i.Title, Plan: a.Plan, Spec: a.Spec, Config: a.Config}
		if e := factoryNormalize(&ar); e != nil {
			return e
		}
		if len(a.Config.ExpectedChecks) == 0 && !a.Config.NoRequiredCIAcknowledged {
			return fmt.Errorf("expected checks or explicit no-required-CI acknowledgement required")
		}
		if len(a.Plan)+len(a.Spec) > factoryImportLimit || a.PlanHash != factoryPlanHash(a.Plan, a.Spec) {
			return fmt.Errorf("invalid approval")
		}
	}
	for _, r := range i.Rounds {
		if r.CI != nil {
			for _, c := range r.CI.Checks {
				if len(c.LogTail) > checkLogLimit {
					return fmt.Errorf("check log exceeds limit")
				}
			}
		}
	}
	return nil
}
func (s *FactoryStore) replayRequest(request, id, hash string) (FactoryItem, bool, error) {
	if p, ok := s.requests[request]; ok {
		if p.ItemID != id && id != "" || p.CommandHash != hash {
			return FactoryItem{}, true, ErrFactoryConflict
		}
		return factoryCopy(p.Item), true, nil
	}
	return FactoryItem{}, false, nil
}
func (s *FactoryStore) Create(req FactoryCreate, now time.Time) (FactoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	raw, e := json.Marshal(req)
	if e != nil {
		return FactoryItem{}, e
	}
	canonical, e := factoryCanonical(raw)
	if e != nil {
		return FactoryItem{}, e
	}
	hash := factoryDigest(append([]byte("create:"), canonical...))
	if i, ok, e := s.replayRequest(req.RequestID, "", hash); ok {
		return i, e
	}
	if e = factoryNormalize(&req); e != nil {
		return FactoryItem{}, e
	}
	idBytes := make([]byte, 16)
	if _, e = rand.Read(idBytes); e != nil {
		return FactoryItem{}, e
	}
	id := hex.EncodeToString(idBytes)
	now = now.UTC()
	i := FactoryItem{ID: id, Title: req.Title, State: "draft", Version: 1, Draft: req, RepoID: req.Config.RepoID, ProjectRoot: req.Config.ProjectRoot, HostID: req.Config.HostID, Branch: "marshal/factory/" + id, CreatedAt: now, UpdatedAt: now}
	return s.append(req.RequestID, hash, i)
}

// Update runs mutate under the store mutex. The callback must not perform I/O or reenter the store.
func (s *FactoryStore) Update(id, requestID string, expectedVersion uint64, command json.RawMessage, mutate func(*FactoryItem) error, now time.Time) (FactoryItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable {
		return FactoryItem{}, ErrFactoryUnavailable
	}
	if strings.TrimSpace(requestID) == "" {
		return FactoryItem{}, fmt.Errorf("requestId required")
	}
	b, e := factoryCanonical(command)
	if e != nil {
		return FactoryItem{}, e
	}
	identity, _ := json.Marshal(struct {
		ID      string
		Version uint64
		Command json.RawMessage
	}{id, expectedVersion, b})
	hash := factoryDigest(identity)
	if i, ok, e := s.replayRequest(requestID, id, hash); ok {
		return i, e
	}
	old, ok := s.items[id]
	if !ok {
		return FactoryItem{}, ErrFactoryNotFound
	}
	if old.Version != expectedVersion {
		return FactoryItem{}, ErrFactoryConflict
	}
	i := factoryCopy(old)
	if mutate == nil {
		return FactoryItem{}, fmt.Errorf("mutation required")
	}
	if e = mutate(&i); e != nil {
		return FactoryItem{}, e
	}
	if i.ID != old.ID || i.Version != old.Version {
		return FactoryItem{}, fmt.Errorf("immutable identity/version")
	}
	// Published approval and allowance history is append-only.
	if len(i.Approvals) < len(old.Approvals) || len(i.Grants) < len(old.Grants) {
		return FactoryItem{}, fmt.Errorf("immutable approval/grant history")
	}
	for n := range old.Approvals {
		a, _ := json.Marshal(i.Approvals[n])
		b, _ := json.Marshal(old.Approvals[n])
		if !bytes.Equal(a, b) {
			return FactoryItem{}, fmt.Errorf("immutable approval history")
		}
	}
	for n := range old.Grants {
		a, _ := json.Marshal(i.Grants[n])
		b, _ := json.Marshal(old.Grants[n])
		if !bytes.Equal(a, b) {
			return FactoryItem{}, fmt.Errorf("immutable grant history")
		}
	}
	i.Version++
	i.UpdatedAt = now.UTC()
	if e = validateFactoryItem(i); e != nil {
		return FactoryItem{}, e
	}
	return s.append(requestID, hash, i)
}
func (s *FactoryStore) append(request, hash string, i FactoryItem) (FactoryItem, error) {
	p := factoryPayload{1, s.seq + 1, request, i.ID, hash, i}
	payload, e := json.Marshal(p)
	if e != nil {
		return FactoryItem{}, e
	}
	line, e := json.Marshal(factoryRecord{payload, factoryDigest(payload)})
	if e != nil {
		return FactoryItem{}, e
	}
	line = append(line, '\n')
	if len(line) > factoryRecordLimit {
		return FactoryItem{}, fmt.Errorf("journal record exceeds 16 MiB")
	}
	n, e := s.write(line)
	if e == nil && n != len(line) {
		e = io.ErrShortWrite
	}
	if e == nil {
		e = s.sync()
	}
	if e != nil {
		s.unavailable = true
		return FactoryItem{}, fmt.Errorf("%w: %v", ErrFactoryUnavailable, e)
	}
	s.seq = p.Sequence
	s.items[i.ID] = factoryCopy(i)
	s.requests[request] = factoryCopy(p)
	return factoryCopy(i), nil
}
