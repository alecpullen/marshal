package bridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Audit event names. Only security-relevant actions are recorded: a log
// that captures everything is one nobody reads.
const (
	AuditSpawn           = "spawn"
	AuditSpawnDenied     = "spawn_denied"
	AuditPendingApproved = "pending_approved"
	AuditPendingDenied   = "pending_denied"
	AuditGateOverride    = "gate_override"
	AuditPush            = "push"
	AuditPatchExport     = "patch_export"
	AuditClientCreated   = "client_created"
	AuditClientRevoked   = "client_revoked"
	AuditRepoRegistered  = "repo_registered"
	AuditRepoRemoved     = "repo_removed"
	AuditPrune           = "prune"
)

// defaultAuditMaxBytes is when a file rotates. Records are small, so a
// busy fleet still greps in milliseconds.
const defaultAuditMaxBytes = 16 << 20

// AuditEvent is one record.
//
// There is deliberately no field capable of holding a credential — no
// token, no hash, no header, no environment. Every field is an
// identifier, an enumerated reason, or a byte count. That is why the
// redaction test can assert the property rather than scrub for it: a
// secret has nowhere to go.
type AuditEvent struct {
	TS       time.Time `json:"ts"`
	Event    string    `json:"event"`
	OwnerID  string    `json:"ownerId,omitempty"`
	AgentID  string    `json:"agentId,omitempty"`
	ClientID string    `json:"clientId,omitempty"`
	RepoID   string    `json:"repoId,omitempty"`
	Origin   string    `json:"origin,omitempty"`
	// Reason is an operator- or policy-supplied explanation: a gate
	// override's justification, or why a spawn was refused.
	Reason string `json:"reason,omitempty"`
	// Detail is a short non-sensitive specific: a branch name, a repo id.
	Detail string `json:"detail,omitempty"`
	// Bytes is set by prune records.
	Bytes int64 `json:"bytes,omitempty"`
}

// AuditLog is an append-only JSONL store.
type AuditLog struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
}

func NewAuditLog(stateDir string) *AuditLog {
	return &AuditLog{dir: filepath.Join(stateDir, "audit"), maxBytes: defaultAuditMaxBytes}
}

// currentAuditName is the active file for a moment in time.
func currentAuditName(t time.Time) string {
	return t.UTC().Format("2006-01") + ".jsonl"
}

// Append writes one record. It stamps TS so callers cannot backdate.
func (a *AuditLog) Append(e AuditEvent) error {
	e.TS = time.Now().UTC()

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("bridge: create audit dir: %w", err)
	}
	path := filepath.Join(a.dir, currentAuditName(e.TS))
	if err := a.rotateIfNeeded(path); err != nil {
		return err
	}

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("bridge: encode audit event: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("bridge: open audit log: %w", err)
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// rotateIfNeeded renames a full file aside so the active one stays
// small. Records are never rewritten, only rolled over.
func (a *AuditLog) rotateIfNeeded(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < a.maxBytes {
		return nil
	}
	for n := 1; ; n++ {
		rolled := fmt.Sprintf("%s.%d", path, n)
		if _, err := os.Stat(rolled); os.IsNotExist(err) {
			return os.Rename(path, rolled)
		}
	}
}

// Tail returns up to n of the most recent records, oldest first. It
// reads only the active file: the UI feed shows recent activity, while
// investigation greps the directory directly.
func (a *AuditLog) Tail(n int) ([]AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	path := filepath.Join(a.dir, currentAuditName(time.Now()))
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []AuditEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e AuditEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue // a torn final line must not break the feed
		}
		out = append(out, e)
		if len(out) > n {
			out = out[1:]
		}
	}
	return out, sc.Err()
}
