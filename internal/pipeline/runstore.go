package pipeline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
func atomicWriteSync(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("runstore: create temp %s: %w", tmp, err)
	}
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
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(p)
		return fmt.Errorf("runstore: write lock: %w", err)
	}
	if err := f.Sync(); err != nil {
		os.Remove(p)
		return fmt.Errorf("runstore: sync lock: %w", err)
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

// TakeoverLock forcibly removes the existing lock and acquires a new one.
// The caller must have already reported the stale owner to the user.
func (rs *RunStore) TakeoverLock(lock RunLock) error {
	if err := os.Remove(rs.lockPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("runstore: remove stale lock: %w", err)
	}
	return rs.AcquireLock(lock)
}
