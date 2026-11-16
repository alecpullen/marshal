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
	"path"
	"path/filepath"
	"regexp"
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

// factoryScopedRequestID namespaces a client-supplied request identity. The
// journal keys replay by request ID, so two clients naming one ID must be two
// operations; what the client sent survives only in provenance.
func factoryScopedRequestID(clientID, requestID string) string {
	h := sha256.Sum256([]byte(clientID + "\x00" + requestID))
	return "mcp:" + hex.EncodeToString(h[:])
}
func factoryPlanHash(plan, spec string) string {
	var b bytes.Buffer
	for _, s := range []string{plan, spec} {
		_ = binary.Write(&b, binary.BigEndian, uint64(len(s)))
		b.WriteString(s)
	}
	return factoryDigest(b.Bytes())
}

// factoryDigestPattern is the shape of one SHA-256 hex digest. A selected
// file's hashes are checked against it because ContentSHA256 names a blob: a
// value that is not a digest is a path, and a path must never reach the store.
var factoryDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// factorySnapshotFileLimit matches the capture limit: an approval that selects
// more files than a capture can hold is refused rather than half-applied.
const factorySnapshotFileLimit = 100

// factoryApprovalHash binds one approval to exactly what was approved: the
// plan and specification bytes and, when the approval carries one, the
// reviewed selection's own digest.
//
// An approval without a snapshot keeps the digest it has always had, so every
// journal written before snapshots existed still validates and replays byte
// for byte. A snapshot-bearing approval uses an explicitly versioned digest,
// which is what keeps the two forms from ever being mistaken for each other.
func factoryApprovalHash(plan, spec string, snap *EditSnapshot) string {
	if snap == nil {
		return FactoryPlanHash(plan, spec)
	}
	// The field order here is the digest's definition, not an accident: a
	// verifier hashes this exact JSON.
	canonical, _ := json.Marshal(struct {
		Version            int    `json:"version"`
		Plan               string `json:"plan"`
		Spec               string `json:"spec"`
		EditSnapshotSHA256 string `json:"editSnapshotSha256"`
	}{Version: 2, Plan: plan, Spec: spec, EditSnapshotSHA256: snap.SHA256})
	return factoryDigest(canonical)
}

// factoryCloneSnapshot returns a snapshot that shares no backing array with s.
// A draft, an approval and a request body routinely hold the same snapshot in
// one process, and none of them may mutate another's selection.
func factoryCloneSnapshot(s *EditSnapshot) *EditSnapshot {
	if s == nil {
		return nil
	}
	out := &EditSnapshot{BaseCommit: s.BaseCommit, SHA256: s.SHA256}
	if s.Files != nil {
		out.Files = append([]SelectedFile(nil), s.Files...)
	}
	return out
}

// factoryValidateSnapshot refuses a selection a seed could not apply exactly.
// Every rule here is one the seed depends on: a base commit it can resolve, a
// blob name that is a digest, a path that stays inside the repository, and an
// operation whose hashes say what it must verify before writing.
func factoryValidateSnapshot(s *EditSnapshot) error {
	if s == nil {
		return nil
	}
	if !plannerCommitPattern.MatchString(s.BaseCommit) {
		return fmt.Errorf("%w: edit snapshot base commit is not a commit id", errFactoryInput)
	}
	if !factoryDigestPattern.MatchString(s.SHA256) {
		return fmt.Errorf("%w: edit snapshot digest is not a SHA-256", errFactoryInput)
	}
	// The recorded digest must describe this selection. A client that supplies
	// its own EditSnapshot is not trusted to have hashed what it sent, and the
	// approval digest binds this string, not the file list behind it.
	if digest, err := plannerSnapshotDigest(*s); err != nil {
		return fmt.Errorf("%w: edit snapshot digest: %v", errFactoryInput, err)
	} else if digest != s.SHA256 {
		return fmt.Errorf("%w: edit snapshot does not match its recorded digest", errFactoryInput)
	}
	if len(s.Files) > factorySnapshotFileLimit {
		return fmt.Errorf("%w: an edit snapshot holds at most %d files", errFactoryInput, factorySnapshotFileLimit)
	}
	seen := make(map[string]bool, len(s.Files))
	for _, file := range s.Files {
		if problem := factorySnapshotPathProblem(file.Path); problem != "" {
			return fmt.Errorf("%w: selected path %q %s", errFactoryInput, file.Path, problem)
		}
		if seen[file.Path] {
			return fmt.Errorf("%w: selected path %q appears twice", errFactoryInput, file.Path)
		}
		seen[file.Path] = true
		switch file.Operation {
		case plannerFileCreate:
			if file.BaseSHA256 != "" {
				return fmt.Errorf("%w: create of %q carries a base hash", errFactoryInput, file.Path)
			}
			if !factoryDigestPattern.MatchString(file.ContentSHA256) {
				return fmt.Errorf("%w: create of %q has no content hash", errFactoryInput, file.Path)
			}
		case plannerFileModify:
			if !factoryDigestPattern.MatchString(file.BaseSHA256) || !factoryDigestPattern.MatchString(file.ContentSHA256) {
				return fmt.Errorf("%w: modify of %q needs both hashes", errFactoryInput, file.Path)
			}
		case plannerFileDelete:
			if !factoryDigestPattern.MatchString(file.BaseSHA256) {
				return fmt.Errorf("%w: delete of %q has no base hash", errFactoryInput, file.Path)
			}
			if file.ContentSHA256 != "" {
				return fmt.Errorf("%w: delete of %q carries a content hash", errFactoryInput, file.Path)
			}
		default:
			return fmt.Errorf("%w: selected path %q has operation %q, not create, modify or delete", errFactoryInput, file.Path, file.Operation)
		}
	}
	// A selection that nests one path inside another cannot be applied in any
	// order: the seed would write the parent as a file and then fail to make a
	// directory of it, leaving a half-applied checkout.
	for _, file := range s.Files {
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("%w: selected path %q is inside selected path %q", errFactoryInput, file.Path, parent)
			}
		}
	}
	return nil
}

// factorySnapshotPathProblem explains why a selected path is not one a seed
// may touch, or returns "" when it is. A path is repository-relative and names
// no element of the repository's own metadata: writing through ".git" would
// change the repository rather than the work.
func factorySnapshotPathProblem(p string) string {
	switch {
	case p == "":
		return "is empty"
	case strings.HasPrefix(p, "/"):
		return "is absolute"
	case strings.ContainsRune(p, '\\'):
		return "is not a slash path"
	case strings.HasPrefix(p, ":"):
		// git reads a leading ':' as pathspec magic even after "--", so this
		// path would stage a different file than the one selected.
		return "names git pathspec magic"
	}
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "":
			return "has an empty element"
		case ".", "..":
			return "is not a clean relative path"
		case ".git":
			return "names repository metadata"
		}
	}
	return ""
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
	// The snapshot is copied before it is checked, so the caller's value and
	// the normalized one never share a selection even when nothing changes.
	r.EditSnapshot = factoryCloneSnapshot(r.EditSnapshot)
	if err := factoryValidateSnapshot(r.EditSnapshot); err != nil {
		return err
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
		ar := FactoryCreate{RequestID: "approval", Title: i.Title, Plan: a.Plan, Spec: a.Spec, Config: a.Config, EditSnapshot: a.EditSnapshot}
		if e := factoryNormalize(&ar); e != nil {
			return e
		}
		if len(a.Config.ExpectedChecks) == 0 && !a.Config.NoRequiredCIAcknowledged {
			return fmt.Errorf("expected checks or explicit no-required-CI acknowledgement required")
		}
		if len(a.Plan)+len(a.Spec) > factoryImportLimit || a.PlanHash != factoryApprovalHash(a.Plan, a.Spec, a.EditSnapshot) {
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
	unresolved := 0
	seenProposals := make(map[string]bool, len(i.Proposals))
	for _, p := range i.Proposals {
		if e := validateFactoryRevisionProposal(p); e != nil {
			return e
		}
		if seenProposals[p.ID] {
			return fmt.Errorf("duplicate proposal id")
		}
		seenProposals[p.ID] = true
		// A proposal's base names the approval it was written against, and both
		// sides are immutable, so the binding is checkable here as well as by the
		// controller. A record naming an approval that does not exist, or a hash
		// that is not that approval's, is corrupt rather than merely stale.
		if p.BaseRevision > uint64(len(i.Approvals)) {
			return fmt.Errorf("proposal names an approval that does not exist")
		}
		if i.Approvals[p.BaseRevision-1].PlanHash != p.BaseHash {
			return fmt.Errorf("proposal base hash does not match its approval")
		}
		if factoryRevisionUnresolved(p.Status) {
			unresolved++
		}
	}
	if unresolved > 1 {
		return fmt.Errorf("multiple unresolved proposals")
	}
	return validateFactoryCompletion(i)
}

// factoryCompletionStates are the values FactoryCompletion.Status accepts.
// The empty status is not here because it is not stored: it is the same "open"
// a missing record is, and an item that carries one is read as open rather
// than rewritten.
var factoryCompletionStates = map[string]bool{"open": true, "complete": true}

var factoryCompletionStopStates = map[string]bool{"requested": true, "stopped": true, "failed": true, "unknown": true}

var factoryAssessmentStates = map[string]bool{"pending": true, "available": true, "failed": true, "unavailable": true}

var factoryLinkOrigins = map[string]bool{"factory": true, "user": true}

var factoryMergeBatchStates = map[string]bool{"active": true, "stopped": true, "complete": true}

var factoryMergeStepStates = map[string]bool{"unattempted": true, "prepared": true, "dispatched": true, "confirmed": true, "failed": true, "uncertain": true}

// validateFactoryCompletion checks the business completion record the journal
// stores. It is absent on every item journalled before completion existed, so
// a nil record validates, and so does a record whose status is empty: both are
// open, and neither is rewritten to say so.
//
// What is checked is the evidence a surface switches on — the statuses, the
// stop state, the merge batch and its steps, and the origin of every PR link —
// plus the record identities, which are the keys a later reopen or merge batch
// names. A value no surface can render is corrupt rather than merely
// unfamiliar.
func validateFactoryCompletion(i FactoryItem) error {
	c := i.Completion
	if c == nil {
		return nil
	}
	if c.Status != "" && !factoryCompletionStates[c.Status] {
		return fmt.Errorf("invalid completion status")
	}
	if c.StopState != "" && !factoryCompletionStopStates[c.StopState] {
		return fmt.Errorf("invalid completion stop state")
	}
	if e := validateFactoryAssessment(c.Assessment); e != nil {
		return e
	}
	if e := validateFactoryPRLinks(c.Links); e != nil {
		return e
	}
	seen := make(map[string]bool, len(c.Records))
	for _, r := range c.Records {
		if strings.TrimSpace(r.ID) == "" || r.At.IsZero() || strings.TrimSpace(r.Actor) == "" {
			return fmt.Errorf("invalid completion record identity")
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate completion record id")
		}
		seen[r.ID] = true
		if e := validateFactoryRunChoice(r.RunChoice); e != nil {
			return e
		}
		if e := validateFactoryAssessment(r.Assessment); e != nil {
			return e
		}
		if e := validateFactoryPRLinks(r.Links); e != nil {
			return e
		}
	}
	b := c.Batch
	if b == nil {
		return nil
	}
	if strings.TrimSpace(b.ID) == "" || !factoryMergeBatchStates[b.State] {
		return fmt.Errorf("invalid merge batch")
	}
	if e := validateFactoryRunChoice(b.RunChoice); e != nil {
		return e
	}
	for _, s := range b.Steps {
		if !factoryMergeStepStates[s.State] {
			return fmt.Errorf("invalid merge step state")
		}
		// A step names the reviewed pull request a merge is sent for, so a
		// step without that identity — or with a strategy no forge applies —
		// is corrupt rather than merely unfamiliar: no surface could render it
		// and no batch could drive it.
		if strings.TrimSpace(s.RepoID) == "" || s.Number <= 0 || strings.TrimSpace(s.NodeID) == "" || strings.TrimSpace(s.HeadSHA) == "" || strings.TrimSpace(s.BaseRef) == "" {
			return fmt.Errorf("invalid merge step identity")
		}
		if !factoryMergeStrategies[s.Strategy] {
			return fmt.Errorf("invalid merge step strategy")
		}
	}
	return nil
}

// validateFactoryRunChoice checks the decision a completion or a merge batch
// records about the run that follows it.
func validateFactoryRunChoice(choice string) error {
	if choice != "" && choice != "continue" && choice != "request_stop" {
		return fmt.Errorf("invalid completion run choice")
	}
	return nil
}

// validateFactoryAssessment checks one piece of completion evidence. Its status
// is what a surface renders in place of the summary, so it is required.
func validateFactoryAssessment(a *FactoryAssessment) error {
	if a == nil {
		return nil
	}
	if !factoryAssessmentStates[a.Status] {
		return fmt.Errorf("invalid completion assessment status")
	}
	return nil
}

// validateFactoryPRLinks checks the pull requests a completion or an
// assessment is about. The origin is optional, because a link observed before
// it recorded who opened the PR still names a real PR, and the empty origin
// says exactly that.
func validateFactoryPRLinks(links []FactoryPRLink) error {
	for _, l := range links {
		if l.Origin != "" && !factoryLinkOrigins[l.Origin] {
			return fmt.Errorf("invalid pr link origin")
		}
	}
	return nil
}

// validateFactoryRevisionProposal checks one proposal as the journal stores it.
// A proposal's candidate, base and authorship are immutable content; its state
// moves, and a state that names an attempt must name one.
func validateFactoryRevisionProposal(p FactoryRevisionProposal) error {
	if strings.TrimSpace(p.ID) == "" || p.Version == 0 || p.BaseRevision == 0 || strings.TrimSpace(p.BaseHash) == "" {
		return fmt.Errorf("invalid proposal identity")
	}
	if strings.TrimSpace(p.Plan) == "" || len(p.Plan)+len(p.Spec) > factoryImportLimit || p.CandidateHash != factoryPlanHash(p.Plan, p.Spec) {
		return fmt.Errorf("invalid proposal content")
	}
	if !factoryRevisionProposalStates[p.Status] || !factoryRevisionAssessmentStates[p.AssessmentState] {
		return fmt.Errorf("invalid proposal state")
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) {
		return fmt.Errorf("invalid proposal timestamp")
	}
	if (p.AssessmentState == "pending" || p.AssessmentState == "assessing") && strings.TrimSpace(p.AssessmentAttemptID) == "" {
		return fmt.Errorf("invalid proposal assessment attempt")
	}
	if a := p.Assessment; a != nil {
		if !factoryRevisionClassifications[a.Classification] || !factoryRevisionRoutings[a.Recommendation] || !factoryRevisionStrategies[a.Strategy] || strings.TrimSpace(a.AssessorID) == "" || a.CandidateHash != p.CandidateHash || a.BaseRevision != p.BaseRevision {
			return fmt.Errorf("invalid proposal assessment")
		}
	}
	if d := p.Delivery; d != nil {
		if !factoryRevisionStrategies[d.Strategy] || !factoryRevisionDeliveryStates[d.State] || d.CandidateHash != p.CandidateHash {
			return fmt.Errorf("invalid proposal delivery")
		}
	}
	if p.Routing != "" && !factoryRevisionRoutings[p.Routing] {
		return fmt.Errorf("invalid proposal routing")
	}
	if p.PauseState != "" && !factoryRevisionPauseStates[p.PauseState] {
		return fmt.Errorf("invalid proposal pause state")
	}
	return nil
}

// factoryRevisionCarryOver checks one stored proposal against the update that
// carried it forward. Everything but its state, assessment, authority and
// delivery is journal history, and its version moves at most one step per
// mutation, so a caller writing from a stale read conflicts.
func factoryRevisionCarryOver(a, b FactoryRevisionProposal) error {
	if a.ID != b.ID || a.BaseRevision != b.BaseRevision || a.BaseHash != b.BaseHash || a.Plan != b.Plan || a.Spec != b.Spec || a.CandidateHash != b.CandidateHash || a.CreatedBy != b.CreatedBy || a.AutoAssess != b.AutoAssess || !a.CreatedAt.Equal(b.CreatedAt) {
		return fmt.Errorf("immutable proposal content")
	}
	if b.Version < a.Version || b.Version > a.Version+1 {
		return fmt.Errorf("invalid proposal version")
	}
	// A state that moved has to say so in the version. Without this, two item
	// versions could carry the same proposal version with different states, and
	// the ProposalVersion guard would admit a command written from the older
	// read: the promise in FactoryRevisionProposal's comment would rest on
	// caller discipline instead of on the layer that makes it.
	if !bytes.Equal(factoryRevisionState(a), factoryRevisionState(b)) && b.Version != a.Version+1 {
		return fmt.Errorf("proposal state moved without a version step")
	}
	return nil
}

// factoryRevisionState projects the fields a mutation may move, so the store
// can tell a proposal carried forward unchanged from one that advanced.
// Everything absent here is immutable history, checked by factoryRevisionCarryOver.
func factoryRevisionState(p FactoryRevisionProposal) []byte {
	b, _ := json.Marshal(struct {
		Status              string                     `json:"status"`
		AssessmentState     string                     `json:"assessmentState"`
		AssessmentAttemptID string                     `json:"assessmentAttemptId"`
		Assessment          *FactoryRevisionAssessment `json:"assessment"`
		AuthorizedBy        string                     `json:"authorizedBy"`
		Authority           string                     `json:"authority"`
		Routing             string                     `json:"routing"`
		PauseState          string                     `json:"pauseState"`
		PauseRequestID      string                     `json:"pauseRequestId"`
		Delivery            *FactoryRevisionDelivery   `json:"delivery"`
		FollowUpRequestID   string                     `json:"followUpRequestId"`
		FollowUpItemID      string                     `json:"followUpItemId"`
		UpdatedAt           time.Time                  `json:"updatedAt"`
	}{p.Status, p.AssessmentState, p.AssessmentAttemptID, p.Assessment, p.AuthorizedBy,
		p.Authority, p.Routing, p.PauseState, p.PauseRequestID, p.Delivery,
		p.FollowUpRequestID, p.FollowUpItemID, p.UpdatedAt})
	return b
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

// ItemForRequest returns the item a journaled request produced, so a durable
// intent whose association was lost can be reconciled with the record the same
// request already created instead of creating a second one.
func (s *FactoryStore) ItemForRequest(request string) (FactoryItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(request) == "" {
		return FactoryItem{}, false
	}
	p, ok := s.requests[request]
	if !ok {
		return FactoryItem{}, false
	}
	return factoryCopy(p.Item), true
}

// Create stores a handoff whose replay identity and content are both the
// request it received.
func (s *FactoryStore) Create(req FactoryCreate, now time.Time) (FactoryItem, error) {
	return s.CreateResolved(req, req, now)
}

// CreateResolved stores a handoff whose replay identity is the operator's
// request but whose content is the resolved configuration.
//
// The two are kept apart on purpose: a client retrying an unchanged create
// after the project's defaults moved on must receive the original work item,
// so the identity is hashed over what the operator sent, never over the
// resolved result.
func (s *FactoryStore) CreateResolved(req, resolved FactoryCreate, now time.Time) (FactoryItem, error) {
	return s.createResolved(req, resolved, nil, now)
}

// CreateAuthorized stores a handoff an authenticated MCP client proposed,
// journaling its provenance in the same append that creates the item: there is
// no second audit write that could succeed behind a failed create, or fail
// behind a successful one.
//
// It performs local validation and the append only. The caller holds the
// policy lock and has already resolved, normalized and preflighted the
// candidate, and a request that already committed replays its original item
// before resolved is read at all.
func (s *FactoryStore) CreateAuthorized(req, resolved FactoryCreate, p factoryPrincipal, now time.Time) (FactoryItem, error) {
	return s.createResolved(req, resolved, &p, now)
}

func (s *FactoryStore) createResolved(req, resolved FactoryCreate, p *factoryPrincipal, now time.Time) (FactoryItem, error) {
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
	if e = factoryNormalize(&resolved); e != nil {
		return FactoryItem{}, e
	}
	idBytes := make([]byte, 16)
	if _, e = rand.Read(idBytes); e != nil {
		return FactoryItem{}, e
	}
	id := hex.EncodeToString(idBytes)
	now = now.UTC()
	i := FactoryItem{ID: id, Title: resolved.Title, State: "draft", Version: 1, Draft: resolved, RepoID: resolved.Config.RepoID, ProjectRoot: resolved.Config.ProjectRoot, HostID: resolved.Config.HostID, Branch: "marshal/factory/" + id, CreatedAt: now, UpdatedAt: now}
	if p != nil {
		// The attribution rides the append that creates the item, so a create
		// that committed is never missing its provenance and a create that
		// failed leaves nothing behind.
		i.History = append(i.History, p.event("create", "draft created", "", now))
	}
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
	// Proposed revisions are journal history too: a stored proposal is
	// carried forward, never rewritten, and only its state moves.
	if len(i.Proposals) < len(old.Proposals) {
		return FactoryItem{}, fmt.Errorf("immutable proposal history")
	}
	for n := range old.Proposals {
		if e := factoryRevisionCarryOver(old.Proposals[n], i.Proposals[n]); e != nil {
			return FactoryItem{}, e
		}
	}
	i.Version++
	i.UpdatedAt = now.UTC()
	if e = validateFactoryItem(i); e != nil {
		return FactoryItem{}, e
	}
	return s.append(requestID, hash, i)
}

// factoryReplaceIdentity is the journal's replay identity for a draft
// replacement: the version it was made against, the whole normalized
// candidate and the authority it was admitted under.
type factoryReplaceIdentity struct {
	Kind      string        `json:"kind"`
	Version   uint64        `json:"expectedVersion"`
	Candidate FactoryCreate `json:"candidate"`
	ClientID  string        `json:"clientId"`
	ProjectID string        `json:"projectId"`
	Action    string        `json:"action"`
	Authority string        `json:"authority,omitempty"`
}

// ReplaceDraft replaces an unapproved draft's content in one journal append.
//
// The state, the identity and the version are checked here, under the store
// lock, so a draft that was approved between the caller's read and this commit
// is refused rather than overwritten: an approval is a frozen snapshot and
// stays one. Only the title, the plan, the spec and the configuration move;
// every earlier event, approval, grant, round, effect and every immutable
// identity field is carried over untouched, and the replacement's attribution
// is part of the same append.
func (s *FactoryStore) ReplaceDraft(id, requestID string, expectedVersion uint64, candidate FactoryCreate, p factoryPrincipal, now time.Time) (FactoryItem, error) {
	c := candidate
	// The candidate names the journal's request identity, so two clients that
	// sent one request ID are two operations, and the version the caller read
	// is part of what is hashed.
	c.RequestID = requestID
	if e := factoryNormalize(&c); e != nil {
		return FactoryItem{}, e
	}
	command, e := json.Marshal(factoryReplaceIdentity{
		Kind: "replace", Version: expectedVersion, Candidate: c,
		ClientID: p.ClientID, ProjectID: p.ProjectID, Action: p.Action, Authority: p.Authority,
	})
	if e != nil {
		return FactoryItem{}, e
	}
	return s.Update(id, requestID, expectedVersion, command, func(i *FactoryItem) error {
		if i.State != "draft" {
			return ErrFactoryConflict
		}
		// What the work item is about, and which project and repository it
		// belongs to, cannot move: an edit replaces the content of one draft.
		if c.Config.RepoID != i.RepoID || c.Config.ProjectRoot != i.ProjectRoot || c.Config.HostID != i.HostID {
			return ErrFactoryConflict
		}
		// The item's title and its draft's title are one field reported
		// twice, so a replacement moves both: a stale Draft.Title would be
		// projected beside the new title and read as a contradiction.
		i.Title = c.Title
		i.Draft.Title = c.Title
		i.Draft.Plan = c.Plan
		i.Draft.Spec = c.Spec
		i.Draft.Config = c.Config
		i.History = append(i.History, p.event("replace", "draft replaced", "", now))
		return nil
	}, now)
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
