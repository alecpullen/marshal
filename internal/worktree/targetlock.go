package worktree

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The root-use protocol is the cross-process half of workspace ownership.
//
// Every Marshal caller that reads or writes a workspace root takes a SHARED
// root-use lease for as long as it touches the root; a lifecycle mutation that
// will rewrite or delete the root takes the EXCLUSIVE target lock on the same
// key. Both live in one advisory lock file per target, so the operating system
// arbitrates between processes exactly as it does inside one: a shared holder
// blocks an exclusive acquirer, and an exclusive holder blocks every new shared
// user until it releases.
//
// Alongside the lock file each root carries durable use notes: one file per
// active lease, written before the lease is returned and removed only when the
// lease reports terminal settlement. A note that outlives its process (a crash,
// a SIGKILL, a stopped debugger) is UNKNOWN use, not idle use: a dead process
// owner proves nothing about the commands it left behind, so an exclusive
// acquirer refuses rather than timing the note out. Nothing here supervises
// processes; it records and reconciles the evidence command ownership already
// produces.

// TargetLocksDirName is the per-repository directory, inside the common git
// directory, that holds the advisory lock files and use notes. It lives beside
// the object database, so every worktree of one repository shares it and a
// clone or a copy gets its own.
const TargetLocksDirName = "marshal-target-locks"

// Use kinds. A note says what kind of work holds a root so an operator reading
// the evidence — and a refusal message — can tell a direct tool operation from
// a managed command or a live child.
const (
	UseKindTool     = "tool"
	UseKindCommand  = "command"
	UseKindChild    = "child"
	UseKindPipeline = "pipeline"
)

// useNoteVersion is the durable note schema this build writes and understands.
// A note whose version is anything else is unknown use: it is never read as an
// idle root, because a newer writer may have recorded evidence this build
// cannot interpret.
const useNoteVersion = 1

// targetPollInterval is how often a blocked acquirer retries the platform's
// non-blocking lock primitive while also checking its context. Polling, rather
// than a blocking lock call, is what makes the wait cancellable.
const targetPollInterval = 10 * time.Millisecond

var (
	// ErrTargetBusy reports that a root is in use — by a live lease, by a
	// managed command, or by retained unknown evidence — so the requested
	// exclusivity was refused.
	ErrTargetBusy = errors.New("workspace target is in use")
	// ErrTargetLockUnsupported is the capability error of a platform with no
	// verified advisory-lock binding. It is never replaced by an unlocked
	// success: a silently in-process-only lock would claim cross-process
	// exclusion it does not provide.
	ErrTargetLockUnsupported = errors.New("advisory workspace locking is not supported on this platform")
)

// UseIdentity names one admitted root user. The fields mirror the identities
// command ownership already records, so a use note and a command execution can
// be reconciled against each other.
type UseIdentity struct {
	OwnerSessionID string `json:"owner_session_id,omitempty"`
	ActorID        string `json:"actor_id,omitempty"`
	ExecutionID    string `json:"execution_id,omitempty"`
	// Kind is one of the UseKind constants.
	Kind string `json:"kind,omitempty"`
}

// useNote is one lease's durable evidence: who holds the root, from which
// process, whether the command it covers was observed to start, and — after a
// release without terminal settlement — that it is unknown rather than done.
type useNote struct {
	Version         int         `json:"version"`
	Root            string      `json:"root"`
	Identity        UseIdentity `json:"identity"`
	RuntimeID       string      `json:"runtime_id"`
	PID             int         `json:"pid,omitempty"`
	ProcessIdentity string      `json:"process_identity,omitempty"`
	Started         bool        `json:"started,omitempty"`
	StartedAt       time.Time   `json:"started_at,omitempty"`
	PublishedAt     time.Time   `json:"published_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	// Unknown marks a note the holder abandoned without terminal evidence. It
	// is retained evidence, not a timeout: exclusive acquisition refuses while
	// it exists.
	Unknown bool `json:"unknown,omitempty"`
}

// describe renders the note for a refusal message without exposing the whole
// record.
func (n useNote) describe() string {
	parts := make([]string, 0, 4)
	if kind := strings.TrimSpace(n.Identity.Kind); kind != "" {
		parts = append(parts, kind)
	}
	if id := strings.TrimSpace(n.Identity.ExecutionID); id != "" {
		parts = append(parts, id)
	}
	if owner := strings.TrimSpace(n.Identity.OwnerSessionID); owner != "" {
		parts = append(parts, "owner "+owner)
	}
	if n.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid %d", n.PID))
	}
	if n.Unknown {
		parts = append(parts, "unknown")
	}
	if len(parts) == 0 {
		return "an unidentified user"
	}
	return strings.Join(parts, " ")
}

// targetPaths is the resolved on-disk identity of one target root.
type targetPaths struct {
	repository string
	// root is the canonical workspace root the notes and lock name.
	root string
	dir  string
	// lock is the one file both the shared root use and the exclusive target
	// lock are taken on. Sharing the key is what makes an ordinary read block a
	// mutation across processes.
	lock string
}

// resolveTarget canonicalizes a repository and a root into the lock identity
// they share. Two spellings of the same path — a symlinked parent, a redundant
// "..", a trailing separator — resolve to one key, so aliases cannot both hold
// the same target.
func resolveTarget(repository, root string) (targetPaths, error) {
	repository = strings.TrimSpace(repository)
	root = strings.TrimSpace(root)
	if repository == "" {
		return targetPaths{}, errors.New("worktree: a canonical repository is required for workspace locking")
	}
	if root == "" {
		return targetPaths{}, errors.New("worktree: a workspace root is required for workspace locking")
	}
	repo := CanonicalPath(repository)
	if info, err := os.Stat(repo); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("%s is not a directory", repo)
		}
		return targetPaths{}, fmt.Errorf("worktree: repository %s: %w", repo, err)
	}
	canonicalRoot := CanonicalPath(root)
	sum := sha256.Sum256([]byte(canonicalRoot))
	key := hex.EncodeToString(sum[:])[:32]
	return targetPaths{
		repository: repo,
		root:       canonicalRoot,
		dir:        filepath.Join(repo, TargetLocksDirName, key),
		lock:       filepath.Join(repo, TargetLocksDirName, key, "lock"),
	}, nil
}

// prepare creates the lock directory and records which canonical root the hash
// names, so an operator inspecting the common git directory can tell the files
// apart. A directory that exists with no lock file and no use note is evidence
// of nothing and never blocks anything.
func (p targetPaths) prepare() error {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return fmt.Errorf("worktree: create target lock dir %s: %w", p.dir, err)
	}
	marker := filepath.Join(p.dir, "root")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := os.WriteFile(marker, []byte(p.root+"\n"), 0o644); err != nil {
		return fmt.Errorf("worktree: record target root %s: %w", marker, err)
	}
	return nil
}

// UseLease is one held shared root-use lease. It is held for the duration of
// one operation — a direct tool call, a managed command's whole execution, a
// child's model lifetime — and released exactly once.
type UseLease struct {
	mu       sync.Mutex
	paths    targetPaths
	identity UseIdentity
	// noteName is the file name of this lease's durable note, under paths.dir.
	noteName string
	gate     *rootGate
	file     *os.File

	started         bool
	pid             int
	processIdentity string
	publishedAt     time.Time
	updatedAt       time.Time
	// gateHeld reports whether this lease still owns its share of the
	// process-local gate, so Release never gives back a share it lost.
	gateHeld bool
	released bool
}

// useRuntimeID identifies this process's use notes. It is random per process
// so a new process never mistakes an older process's notes for its own.
var useRuntimeID = newUseRuntimeID()

// useSequence makes each lease's note name unique within the process even when
// two leases are taken for one root at the same instant.
var useSequence struct {
	mu sync.Mutex
	n  uint64
}

func newUseRuntimeID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", os.Getpid())
	}
	return hex.EncodeToString(buf)
}

func nextUseSequence() uint64 {
	useSequence.mu.Lock()
	defer useSequence.mu.Unlock()
	useSequence.n++
	return useSequence.n
}

// AcquireWorkspaceUse takes a shared root-use lease on root, in repository,
// and publishes the durable use note that records who holds it.
//
// The wait is cancellable: while an exclusive target lock is held this polls
// the platform lock and the process-local gate, returning the context's error
// when it ends first. Nothing is published and no lock is retained on failure.
//
// The two string arguments are the canonical repository (the common git
// directory identity) and the workspace root. A caller with no git repository
// to participate in passes no lease at all rather than an unlocked one.
func AcquireWorkspaceUse(ctx context.Context, repository, root string, id UseIdentity) (*UseLease, error) {
	paths, err := resolveTarget(repository, root)
	if err != nil {
		return nil, err
	}
	if err := paths.prepare(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gate := rootGateFor(paths.lock)
	if err := gate.acquireShared(ctx); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrTargetBusy, paths.root, err)
	}
	file, err := openTargetLock(ctx, paths.lock, false)
	if err != nil {
		gate.releaseShared()
		if errors.Is(err, ErrTargetLockUnsupported) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrTargetBusy, paths.root, err)
	}
	now := time.Now()
	lease := &UseLease{
		paths:       paths,
		identity:    id,
		gate:        gate,
		file:        file,
		publishedAt: now,
		updatedAt:   now,
		gateHeld:    true,
	}
	lease.noteName = fmt.Sprintf("use-%s-%d.json", useRuntimeID, nextUseSequence())
	if err := lease.writeNote(lease.snapshot()); err != nil {
		releaseTargetFile(file)
		gate.releaseShared()
		return nil, err
	}
	// A shared acquisition that loses its context after it has published is
	// still a held lease: the caller owns the release, exactly as it does for a
	// lease whose context ends later.
	return lease, nil
}

// snapshot builds the note this lease publishes now.
func (l *UseLease) snapshot() useNote {
	return useNote{
		Version:         useNoteVersion,
		Root:            l.paths.root,
		Identity:        l.identity,
		RuntimeID:       useRuntimeID,
		PID:             l.pid,
		ProcessIdentity: l.processIdentity,
		Started:         l.started,
		StartedAt:       l.startedAt(),
		PublishedAt:     l.publishedAt,
		UpdatedAt:       l.updatedAt,
	}
}

func (l *UseLease) startedAt() time.Time {
	if !l.started {
		return time.Time{}
	}
	return l.updatedAt
}

// MarkStarted records process-start evidence on the lease's use note: the pid
// and identity of the process the command actually launched. It is evidence for
// reconciliation — a later reader can tell a command that started from one that
// never did — and it never extends or transfers the lease.
func (l *UseLease) MarkStarted(pid int, processIdentity string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return fmt.Errorf("worktree: use lease for %s is already released", l.paths.root)
	}
	l.pid = pid
	l.processIdentity = strings.TrimSpace(processIdentity)
	l.started = true
	l.updatedAt = time.Now()
	return l.writeNote(l.snapshot())
}

// Release drops the shared lease. terminal reports whether the work it covered
// was observed to settle: a terminal release removes the note, so the root
// becomes idle; a non-terminal release keeps it as unknown use, because an
// unobserved end is exactly the evidence a later mutation must refuse. Release
// is idempotent.
func (l *UseLease) Release(terminal bool) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true

	var errs []error
	if l.noteName != "" {
		if terminal {
			if err := os.Remove(filepath.Join(l.paths.dir, l.noteName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("worktree: remove use note for %s: %w", l.paths.root, err))
			}
		} else {
			note := l.snapshot()
			note.Unknown = true
			note.UpdatedAt = time.Now()
			if err := l.writeNote(note); err != nil {
				errs = append(errs, err)
			}
		}
		l.noteName = ""
	}
	if l.file != nil {
		if err := releaseTargetFile(l.file); err != nil {
			errs = append(errs, err)
		}
		l.file = nil
	}
	if l.gateHeld {
		l.gate.releaseShared()
		l.gateHeld = false
	}
	return errors.Join(errs...)
}

// writeNote publishes one note atomically: a temporary file in the same
// directory, then a rename onto the lease's name. A reader therefore never
// observes a half-written note, and an existing note is replaced in one step.
func (l *UseLease) writeNote(note useNote) error {
	data, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("worktree: encode use note for %s: %w", l.paths.root, err)
	}
	final := filepath.Join(l.paths.dir, l.noteName)
	tmp, err := os.CreateTemp(l.paths.dir, ".use-*.tmp")
	if err != nil {
		return fmt.Errorf("worktree: create use note for %s: %w", l.paths.root, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("worktree: write use note for %s: %w", l.paths.root, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("worktree: close use note for %s: %w", l.paths.root, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("worktree: publish use note for %s: %w", l.paths.root, err)
	}
	return nil
}

// AcquireTargetLock takes the exclusive lifecycle lock on root, in repository,
// and returns its release. It refuses with ErrTargetBusy when the root has a
// live user or retained unknown use; it never waits for a user it can already
// see, and it never reports success on a lock it did not get.
//
// The lock is the same file ordinary shared root-use leases are taken on, so an
// in-flight read in another process blocks this acquisition exactly as a local
// one does. The caller must not hold a shared lease on the same roots: the
// protocol acquires exclusivity instead of upgrading, which is why lifecycle
// callers take the exclusive lease first and let their internal git calls
// inherit it.
func AcquireTargetLock(ctx context.Context, repository, root string) (func(), error) {
	paths, err := resolveTarget(repository, root)
	if err != nil {
		return nil, err
	}
	if err := paths.prepare(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Retained evidence is refused before any lock is attempted: a note whose
	// owner died leaves no held descriptor, and an unlocked file alone does not
	// establish that the commands it covered stopped.
	if notes, err := readUseNotes(paths); err != nil {
		return nil, err
	} else if len(notes) > 0 {
		return nil, busyError(paths, notes)
	}
	gate := rootGateFor(paths.lock)
	if !gate.tryExclusive() {
		return nil, fmt.Errorf("%w: %s is used by a live operation in this process", ErrTargetBusy, paths.root)
	}
	file, err := openTargetLock(ctx, paths.lock, true)
	if err != nil {
		gate.releaseExclusive()
		if errors.Is(err, ErrTargetLockUnsupported) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrTargetBusy, paths.root, err)
	}
	// Re-read under the exclusive lock: a user that published between the first
	// read and the lock (or a note left by a crashed process whose descriptor is
	// already gone) must still refuse the mutation.
	if notes, err := readUseNotes(paths); err != nil {
		releaseTargetFile(file)
		gate.releaseExclusive()
		return nil, err
	} else if len(notes) > 0 {
		releaseTargetFile(file)
		gate.releaseExclusive()
		return nil, busyError(paths, notes)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = releaseTargetFile(file)
			gate.releaseExclusive()
		})
	}, nil
}

// busyError names the evidence that refuses the mutation, so a lifecycle caller
// can report which command or child is still live instead of a bare "busy".
func busyError(paths targetPaths, notes []useNote) error {
	held := make([]string, 0, len(notes))
	for _, note := range notes {
		held = append(held, note.describe())
	}
	sort.Strings(held)
	return fmt.Errorf("%w: %s is held by %s", ErrTargetBusy, paths.root, strings.Join(held, ", "))
}

// readUseNotes lists the durable use notes recorded for one target. An
// unreadable note, a malformed one, and one written by an unknown schema
// version are all reported as unknown use rather than skipped: evidence this
// build cannot interpret is never evidence of an idle root.
func readUseNotes(paths targetPaths) ([]useNote, error) {
	entries, err := os.ReadDir(paths.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("worktree: read target use notes for %s: %w", paths.root, err)
	}
	var notes []useNote
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "use-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(paths.dir, name))
		if rerr != nil {
			notes = append(notes, useNote{Root: paths.root, Unknown: true})
			continue
		}
		var note useNote
		if json.Unmarshal(data, &note) != nil || note.Version != useNoteVersion {
			notes = append(notes, useNote{Root: paths.root, Unknown: true})
			continue
		}
		if note.Root == "" {
			note.Root = paths.root
		}
		notes = append(notes, note)
	}
	return notes, nil
}

// openTargetLock opens the target's advisory lock file and takes the requested
// lock, polling until it is granted or ctx ends. A platform with no verified
// binding returns ErrTargetLockUnsupported before anything is created.
func openTargetLock(ctx context.Context, path string, exclusive bool) (*os.File, error) {
	if !platformTargetLocking {
		return nil, ErrTargetLockUnsupported
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("worktree: open target lock %s: %w", path, err)
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		got, lerr := tryTargetLock(file, exclusive)
		if lerr != nil {
			file.Close()
			return nil, lerr
		}
		if got {
			return file, nil
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(targetPollInterval):
		}
	}
}

// releaseTargetFile explicitly unlocks and then closes. Unlocking is
// belt-and-braces: closing the descriptor drops every lock the process holds on
// it, but being explicit keeps release independent of descriptor-lifetime
// details. The lock file itself is never deleted: a second acquirer may already
// have it open.
func releaseTargetFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return errors.Join(unlockTargetFile(file), file.Close())
}

// rootGate is the process-local half of the protocol. The OS lock alone is not
// enough inside one process on every platform, and a channel-based gate can be
// waited on with a context, which a sync.RWMutex cannot.
type rootGate struct {
	mu        sync.Mutex
	shared    int
	exclusive bool
}

var rootGates = struct {
	mu sync.Mutex
	m  map[string]*rootGate
}{m: make(map[string]*rootGate)}

// rootGateFor returns the process-local gate for one lock file. Entries are
// retained for the process lifetime: the set of roots one process touches is
// small and bounded.
func rootGateFor(path string) *rootGate {
	key := filepath.Clean(path)
	rootGates.mu.Lock()
	defer rootGates.mu.Unlock()
	gate, ok := rootGates.m[key]
	if !ok {
		gate = &rootGate{}
		rootGates.m[key] = gate
	}
	return gate
}

func (g *rootGate) tryShared() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exclusive {
		return false
	}
	g.shared++
	return true
}

func (g *rootGate) releaseShared() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.shared > 0 {
		g.shared--
	}
}

func (g *rootGate) tryExclusive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exclusive || g.shared > 0 {
		return false
	}
	g.exclusive = true
	return true
}

func (g *rootGate) releaseExclusive() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.exclusive = false
}

// acquireShared waits for a shared gate, honouring ctx.
func (g *rootGate) acquireShared(ctx context.Context) error {
	for {
		if g.tryShared() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(targetPollInterval):
		}
	}
}
