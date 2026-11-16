package pipeline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

// Manifest is the immutable identity of one pipeline run. It is written once
// at run creation and must never change; recovery validates every field
// against the current environment before resuming.
type Manifest struct {
	SchemaVersion      int    `json:"schema_version"`
	RunID              string `json:"run_id"`
	PlanPath           string `json:"plan_path"`
	PlanHash           string `json:"plan_hash"`
	RepoRoot           string `json:"repo_root"`
	TargetBranch       string `json:"target_branch"`
	PipelineBranch     string `json:"pipeline_branch"`
	BaseSHA            string `json:"base_sha"`
	WorktreePath       string `json:"worktree_path"`
	CreatedAt          string `json:"created_at"`
	MaxFixRounds       int    `json:"max_fix_rounds"`
	MaxDispatchRetries int    `json:"max_dispatch_retries"`
	MaxTotalTokens     int    `json:"max_total_tokens"`
	VerifyBuild        string `json:"verify_build"`
	VerifyTest         string `json:"verify_test"`
}

// Checkpoint is one durable state transition, appended to checkpoints.jsonl.
// Recovery replays the last valid record to reconstruct the controller.
type Checkpoint struct {
	Seq           int    `json:"seq"`
	RunID         string `json:"run_id"`
	Timestamp     string `json:"timestamp"`
	Phase         string `json:"phase"`
	TaskN         int    `json:"task_n"`
	Attempt       int    `json:"attempt"`
	FixRound      int    `json:"fix_round"`
	RequestedRole string `json:"requested_role,omitempty"`
	ResolvedRole  string `json:"resolved_role,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	Fallback      bool   `json:"fallback,omitempty"`
	BaseSHA       string `json:"base_sha,omitempty"`
	HeadSHA       string `json:"head_sha,omitempty"`
	BriefPath     string `json:"brief_path,omitempty"`
	ReportPath    string `json:"report_path,omitempty"`
	PackagePath   string `json:"package_path,omitempty"`
	VerdictPath   string `json:"verdict_path,omitempty"`
	GateQuestion  string `json:"gate_question,omitempty"`
	GateAnswer    string `json:"gate_answer,omitempty"`
	TokensUsed    int    `json:"tokens_used,omitempty"`
	TokensMax     int    `json:"tokens_max,omitempty"`
	ErrorDetail   string `json:"error_detail,omitempty"`
}

// RunStore owns the durable manifest and checkpoint files for one run.
type RunStore struct {
	paths Paths

	// takeoverSwapHook, when non-nil, runs between the takeover's read of
	// the existing lock and the move-aside, so a test can simulate another
	// owner replacing the lock in that window. Production code never sets
	// it.
	takeoverSwapHook func()
}

func NewRunStore(paths Paths) *RunStore {
	return &RunStore{paths: paths}
}

func (rs *RunStore) manifestPath() string {
	return filepath.Join(rs.paths.Dir, "manifest.json")
}

func (rs *RunStore) checkpointPath() string {
	return filepath.Join(rs.paths.Dir, "checkpoints.jsonl")
}

// CreateManifest writes the manifest atomically. A second call returns an
// error: the manifest is immutable for the run's lifetime.
func (rs *RunStore) CreateManifest(m Manifest) error {
	p := rs.manifestPath()
	if _, err := os.Stat(p); err == nil {
		return fmt.Errorf("runstore: manifest already exists")
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("runstore: marshal manifest: %w", err)
	}
	return atomicWriteSync(p, data)
}

// Manifest reads and decodes the manifest. A missing file is an error.
func (rs *RunStore) Manifest() (Manifest, error) {
	data, err := os.ReadFile(rs.manifestPath())
	if err != nil {
		return Manifest{}, fmt.Errorf("runstore: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("runstore: parse manifest: %w", err)
	}
	return m, nil
}

// atomicWriteSync writes data to a temp file, fsyncs, then renames to the
// final path. This ensures the file is either fully present or absent after
// a crash — never partially written.
func atomicWriteSync(path string, data []byte) (retErr error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("runstore: create temp %s: %w", tmp, err)
	}
	defer func() {
		if retErr != nil {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("runstore: write temp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("runstore: sync temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("runstore: close temp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("runstore: rename %s: %w", path, err)
	}
	return nil
}

// AppendCheckpoint appends one checkpoint as a single JSON line with flush
// and sync. The caller sets Seq; the run store does not auto-increment
// because the controller manages the sequence.
func (rs *RunStore) AppendCheckpoint(c Checkpoint) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("runstore: marshal checkpoint: %w", err)
	}
	data = append(data, '\n')
	f, err := os.OpenFile(rs.checkpointPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("runstore: open checkpoints: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("runstore: write checkpoint: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("runstore: sync checkpoints: %w", err)
	}
	return nil
}

// LastCheckpoint returns the last valid (complete, parseable) checkpoint
// line. A partial final line — caused by a crash mid-write — is ignored.
func (rs *RunStore) LastCheckpoint() (Checkpoint, bool, error) {
	lines, err := rs.scanLines()
	if err != nil {
		return Checkpoint{}, false, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var c Checkpoint
		if err := json.Unmarshal(lines[i], &c); err != nil {
			continue // partial or malformed final line
		}
		return c, true, nil
	}
	return Checkpoint{}, false, nil
}

// Replay returns all valid checkpoints in append order. A partial or
// malformed line in the middle of the file is treated as corruption and
// returned as an error; only a partial final line is tolerated.
func (rs *RunStore) Replay() ([]Checkpoint, error) {
	lines, err := rs.scanLines()
	if err != nil {
		return nil, err
	}
	var out []Checkpoint
	for i, line := range lines {
		var c Checkpoint
		if err := json.Unmarshal(line, &c); err != nil {
			if i == len(lines)-1 && isPartialLine(line) {
				continue
			}
			return nil, fmt.Errorf("runstore: malformed checkpoint at line %d: %w", i+1, err)
		}
		out = append(out, c)
	}
	return out, nil
}

func (rs *RunStore) scanLines() ([][]byte, error) {
	data, err := os.ReadFile(rs.checkpointPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("runstore: read checkpoints: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	var lines [][]byte
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	return lines, scanner.Err()
}

// isPartialLine reports whether a line that failed to parse is likely a
// truncated final write (missing closing brace) rather than corruption.
func isPartialLine(line []byte) bool {
	return !bytes.Contains(line, []byte("}"))
}

// RunLock records the process that currently owns a run. Only one live
// owner is permitted; a stale lock requires explicit takeover.
type RunLock struct {
	RunID      string `json:"run_id"`
	PID        int    `json:"pid"`
	Host       string `json:"host"`
	AcquiredAt string `json:"acquired_at"`
}

func (rs *RunStore) lockPath() string {
	return filepath.Join(rs.paths.Dir, "run.lock")
}

// LockPath is the lock file's path, for messages that tell the user which
// file to delete when a stale lock cannot be judged safe to take over.
func (rs *RunStore) LockPath() string {
	return rs.lockPath()
}

// AcquireLock creates the lock file. If one already exists, it returns an
// error — the caller must inspect the existing lock and decide whether to
// take over.
func (rs *RunStore) AcquireLock(lock RunLock) error {
	p := rs.lockPath()
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("runstore: marshal lock: %w", err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("runstore: run is already locked")
		}
		return fmt.Errorf("runstore: create lock: %w", err)
	}
	// The lock file must not be left behind on a partial write: a stale
	// lock would block every later run on these paths. On any failure the
	// file is removed. The descriptor is closed explicitly so a Close
	// error is reported rather than silently swallowed by a deferred call.
	writeErr := func() error {
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("runstore: write lock: %w", err)
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("runstore: sync lock: %w", err)
		}
		return nil
	}()
	closeErr := f.Close()
	if writeErr != nil {
		os.Remove(p)
		return writeErr
	}
	if closeErr != nil {
		os.Remove(p)
		return fmt.Errorf("runstore: close lock: %w", closeErr)
	}
	return nil
}

// Lock returns the current lock, or ok=false when none exists.
func (rs *RunStore) Lock() (RunLock, bool, error) {
	data, err := os.ReadFile(rs.lockPath())
	if os.IsNotExist(err) {
		return RunLock{}, false, nil
	}
	if err != nil {
		return RunLock{}, false, fmt.Errorf("runstore: read lock: %w", err)
	}
	var l RunLock
	if err := json.Unmarshal(data, &l); err != nil {
		return RunLock{}, false, fmt.Errorf("runstore: parse lock: %w", err)
	}
	return l, true, nil
}

// ReleaseLock removes the lock file. It is a no-op when no lock exists.
func (rs *RunStore) ReleaseLock() error {
	err := os.Remove(rs.lockPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// TakeoverLock replaces a run lock the caller has judged stale. expected is
// the lock the caller read before judging it; lock is the new owner's lock.
//
// The whole read → park → verify → claim → verify sequence runs inside an
// exclusive section (flock on a sidecar mutex file, so the kernel serialises
// takers), which is what makes exactly one taker win: without it two takers
// can both observe an absent lock file in another taker's park window and
// both claim the slot, and a taker can read a lock a concurrent winner has
// just installed and park that instead of the stale one.
//
// The stale lock is parked under a unique name rather than deleted, its
// identity is checked against expected, the slot is claimed with a
// create-if-absent primitive, and the claim is re-verified before the taker
// reports ownership. A lock that does not match expected is never touched, so
// a displaced or refused takeover leaves the lock intact.
//
// The caller must have already judged the existing owner stale and reported
// it to the user.
func (rs *RunStore) TakeoverLock(expected, lock RunLock) error {
	return rs.withTakeoverSection(func() error {
		return rs.takeOverLock(expected, lock)
	})
}

// takeOverLock is TakeoverLock's body, which requires the takeover section.
func (rs *RunStore) takeOverLock(expected, lock RunLock) error {
	p := rs.lockPath()
	rs.clearDeadSwapFiles(p)
	cur, ok, err := rs.Lock()
	if err != nil {
		// A lock we cannot read cannot be judged; refuse to move it.
		return err
	}
	if !ok {
		// The owner released the lock. No other taker can be mid-swap — the
		// swap runs in this same section — so the slot is free and ours to
		// take.
		return rs.AcquireLock(lock)
	}
	if cur != expected {
		// Not the lock the caller judged stale: another owner installed (or
		// restored) one after the caller's read. Refuse and leave it alone.
		return fmt.Errorf("runstore: lock changed while taking over")
	}
	if rs.takeoverSwapHook != nil {
		// Test-only: simulate a foreign writer that does not take the
		// section, landing between the read above and the move-aside.
		rs.takeoverSwapHook()
	}
	suffix := nextSwapSuffix()
	quarantine := p + ".takeover." + suffix
	if err := os.Rename(p, quarantine); err != nil {
		if os.IsNotExist(err) {
			// The owner released the lock after our read.
			return rs.AcquireLock(lock)
		}
		return fmt.Errorf("runstore: park stale lock: %w", err)
	}
	// The parked file must be the lock judged stale. When it is not, another
	// owner replaced it in the window: put theirs back and refuse.
	parked, perr := readLock(quarantine)
	if perr != nil || parked != expected {
		if rerr := restoreLock(quarantine, p); rerr != nil {
			return fmt.Errorf("runstore: lock changed while taking over, "+
				"and the displaced lock could not be restored: %w", rerr)
		}
		return fmt.Errorf("runstore: lock changed while taking over")
	}
	if err := rs.claimLock(lock, nextSwapSuffix()); err != nil {
		// The claim failed. Put the parked lock back rather than leaving the
		// run unlocked; restoreLock cannot displace a lock another owner
		// installed in the meantime.
		if rerr := restoreLock(quarantine, p); rerr != nil {
			return fmt.Errorf("%w (and the parked lock could not be restored: %v)", err, rerr)
		}
		return err
	}
	// Post-claim re-verify: the lock file must now hold this taker's lock and
	// nothing else. A claim that did not take effect must never be reported as
	// ownership — that is a second owner.
	got, ok, err := rs.Lock()
	if err != nil || !ok || got != lock {
		if rerr := restoreLock(quarantine, p); rerr != nil {
			return fmt.Errorf("runstore: takeover did not take effect, "+
				"and the displaced lock could not be restored: %w", rerr)
		}
		return fmt.Errorf("runstore: takeover did not take effect: the run lock is not ours")
	}
	if err := os.Remove(quarantine); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("runstore: remove parked lock: %w", err)
	}
	return nil
}

// withTakeoverSection runs fn while holding the run's exclusive takeover
// section: an advisory lock on a sidecar mutex file that is never renamed or
// replaced, so every taker in every process locks the same inode.
func (rs *RunStore) withTakeoverSection(fn func() error) (retErr error) {
	f, err := os.OpenFile(rs.lockMutexPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("runstore: open takeover mutex: %w", err)
	}
	if err := flockExclusive(f); err != nil {
		f.Close()
		return fmt.Errorf("runstore: take takeover mutex: %w", err)
	}
	defer func() {
		if err := funlock(f); err != nil && retErr == nil {
			retErr = fmt.Errorf("runstore: release takeover mutex: %w", err)
		}
		if err := f.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("runstore: close takeover mutex: %w", err)
		}
	}()
	return fn()
}

// lockMutexPath is the sidecar file the takeover section locks.
func (rs *RunStore) lockMutexPath() string {
	return rs.lockPath() + ".mutex"
}

// swapSeq keeps parked and half-written lock names unique within a process.
// Unlike a timestamp it cannot repeat, so two takers never share a name.
var swapSeq atomic.Uint64

// nextSwapSuffix returns a swap-file suffix unique across processes (pid) and
// within one (a strictly increasing counter).
func nextSwapSuffix() string {
	return fmt.Sprintf("%d.%d", os.Getpid(), swapSeq.Add(1))
}

// clearDeadSwapFiles removes parked and half-written lock files left behind
// by a taker that died inside the section. It is safe because the caller
// holds the section: no live taker can own a swap file of its own.
func (rs *RunStore) clearDeadSwapFiles(p string) {
	for _, pattern := range []string{p + ".takeover.*", p + ".new.*"} {
		left, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, f := range left {
			os.Remove(f)
		}
	}
}

// claimLock installs lock at the lock path without ever replacing an existing
// lock file: the content is written to a sibling first and hard-linked into
// place, an atomic create-if-absent with no empty-file window. A filesystem
// without hard links falls back to the O_CREATE|O_EXCL claim AcquireLock
// uses, which is the same primitive.
func (rs *RunStore) claimLock(lock RunLock, suffix string) error {
	p := rs.lockPath()
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("runstore: marshal lock: %w", err)
	}
	tmp := p + ".new." + suffix
	// The temp is synced before it is linked, so the file that becomes the
	// run lock is never a torn write.
	if err := writeFileSync(tmp, data); err != nil {
		return fmt.Errorf("runstore: write lock: %w", err)
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, p); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("runstore: run is already locked")
		}
		return rs.AcquireLock(lock)
	}
	return nil
}

// writeFileSync writes data to path and fsyncs it, removing the file if any
// step fails.
func writeFileSync(path string, data []byte) (retErr error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			os.Remove(path)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// restoreLock puts a parked lock file back at dst and reports failures: a park
// that cannot be undone is a run lock the run has lost, so the caller must
// surface it instead of reporting a clean refusal. When another owner has
// claimed the slot in the meantime (EEXIST), theirs is left untouched and the
// parked copy is dropped — there is nothing to restore and nothing went wrong.
func restoreLock(parked, dst string) error {
	err := os.Link(parked, dst)
	switch {
	case err == nil:
	case os.IsExist(err):
		return removeParkedLock(parked)
	default:
		// No hard links on this filesystem: an exclusive create of the same
		// content restores the slot with the same no-clobber semantics.
		if rerr := restoreByExclusiveCreate(parked, dst); rerr != nil {
			if os.IsExist(rerr) {
				return removeParkedLock(parked)
			}
			return rerr
		}
	}
	return removeParkedLock(parked)
}

// restoreByExclusiveCreate recreates dst from the parked file's content
// without ever replacing an existing lock file.
func restoreByExclusiveCreate(parked, dst string) error {
	data, err := os.ReadFile(parked)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(dst)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(dst)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// removeParkedLock drops a parked lock file that has been restored or
// superseded.
func removeParkedLock(parked string) error {
	if err := os.Remove(parked); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("runstore: remove parked lock %s: %w", parked, err)
	}
	return nil
}

// readLock decodes the lock file at p.
func readLock(p string) (RunLock, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return RunLock{}, err
	}
	var l RunLock
	if err := json.Unmarshal(data, &l); err != nil {
		return RunLock{}, err
	}
	return l, nil
}
