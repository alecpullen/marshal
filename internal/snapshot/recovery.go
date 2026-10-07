package snapshot

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// This file is the approved recovery path for a store the bounded layout cannot
// fix by shrinking a limit.
//
// The situation it addresses, stated plainly because every decision below is
// made in its shadow:
//
//	An existing pre-v2 store can be arbitrarily large. Old snapshots were
//	parent-chained and reachable from a branch, so pruning refs freed nothing,
//	and an interrupted `git gc` during shutdown left abandoned `tmp_pack_*`
//	files behind. On the store this work was written for that was 243 GiB in one
//	workspace: ~171 GiB of abandoned temporary packs plus a 71 GiB history pack
//	retained because it was reachable from `master`.
//
// The new code never creates that again, but it cannot undo it. So this file
// removes it — and it is the ONLY code in the whole feature that can delete a
// user's rollback history, which is why every mutation here is gated on the
// offline confirmation below and on nothing else.
//
// Four invariants hold throughout:
//
//  1. NOTHING IS EVER MUTATED WITHOUT THE OFFLINE ACKNOWLEDGEMENT. Old Marshal
//     binaries do not honour the store lock, so this process cannot prove no
//     legacy writer is active. The acknowledgement is an OPERATIONAL
//     PREREQUISITE the operator asserts, never a guarantee this code claims.
//  2. NOTHING IS EVER DELETED EXCEPT THE NAMED SHADOW STORE OR A RECOGNIZED
//     TEMPORARY ARTIFACT. No arbitrary user-supplied path is ever resolved for
//     deletion, and no path outside the managed root is ever touched. The
//     project's own working tree and its `.git` are never read, written, or
//     removed.
//  3. NO REPACK, EVER. An unsafe automatic `git gc` is precisely what caused the
//     original defect, so it appears nowhere. Migration copies objects one at a
//     time through the manager's own bounded writer.
//  4. NOTHING IS DELETED ON A REFUSAL OR AN INTERRUPTION. EOF, a refusal, a
//     headless invocation, a changed target, and every injected crash boundary
//     all leave the store recoverable and every published ref intact.

// ---------------------------------------------------------------------------
// Offline confirmation
// ---------------------------------------------------------------------------

// offlinePrerequisite is the exact text shown before any legacy mutation.
//
// It is deliberately blunt about the limitation. It does NOT say "the store is
// exclusively yours" or "old binaries are locked out": the operator has to
// understand that the only thing standing between old Marshal instances and a
// corrupted store is their own assurance that those instances are stopped.
const offlinePrerequisite = `This operation MODIFIES or REMOVES an old-format Marshal snapshot store.

Before you continue, ALL of the following must be true:

  * Every older Marshal process that could write this store is STOPPED.
  * No older Marshal process will be started again until this command finishes.
  * No editor, backup tool, or git command is repacking or pruning anything
    inside the snapshots directory.

WHY THIS MATTERS:
  Old Marshal binaries do not understand the new advisory store lock. They will
  not see this command running, will not wait for it, and can write into the
  same store at the same time. This command CANNOT detect them or exclude them.
  Your assurance that they are stopped is the only protection.

This is an operational prerequisite YOU are asserting. It is not a
guarantee this command can verify or enforce.`

// Confirmer is the controlling-terminal abstraction every destructive recovery
// operation goes through, so a test can substitute a scripted terminal and no
// test ever needs a real tty.
type Confirmer interface {
	// Write sends operator-facing text to the terminal, NOT to the caller's
	// stdout. The prompt must appear where the human is looking even when the
	// command's stdout is redirected into a file or a pipe.
	io.Writer
	// IsTerminal reports whether a controlling terminal is available at all. A
	// false answer refuses the operation before anything is printed: there is no
	// point asking a question nobody can read.
	IsTerminal() bool
	// Prompt writes a prompt (with no trailing newline) and reads one line.
	// ErrConfirmationEOF means the input ended without an answer — an EOF is a
	// refusal, never an affirmative.
	Prompt(prompt string) (string, error)
	// Close releases the terminal. It is safe to call more than once.
	Close() error
	// Description names the terminal for diagnostics, e.g. "/dev/tty".
	Description() string
}

// ErrConfirmationEOF is returned when the terminal's input ended without an
// answer. EOF is deliberately an error rather than an empty string: an empty
// answer and a closed terminal must both refuse, but only one of them is worth
// explaining to the operator.
var ErrConfirmationEOF = errors.New("confirmation input ended without an answer")

// confirmAck is the exact word that acknowledges the offline prerequisite. It
// must be typed in full: "y" is not accepted, because the acknowledgement is of
// a specific, consequential statement rather than a generic yes/no.
const confirmAck = "i-have-stopped-all-old-marshal-instances"

// requireOfflineAcknowledgement prints the offline prerequisite and requires the
// acknowledgement word.
//
// It returns nil only when the operator typed the acknowledgement exactly. Every
// other outcome — no terminal at all, a read error, EOF, a refusal, or any other
// text — returns an error and performs nothing.
func requireOfflineAcknowledgement(c Confirmer, action string, notices ...string) error {
	for _, notice := range notices {
		if notice == "" {
			continue
		}
		fmt.Fprintln(terminalWriter(c), notice)
	}
	if c == nil {
		return storeErrorf(ReasonLegacyRecoveryRequired,
			"offline recovery of an old-format snapshot store requires a controlling terminal, "+
				"and none was provided for %s", action)
	}
	if !c.IsTerminal() {
		return storeErrorf(ReasonLegacyRecoveryRequired,
			"offline recovery of an old-format snapshot store requires a controlling terminal; "+
				"%s cannot be confirmed on %s — a piped or headless invocation is refused by design, "+
				"because old Marshal binaries ignore the new store lock and cannot be detected",
			action, c.Description())
	}
	fmt.Fprintln(c)
	fmt.Fprintln(c, offlinePrerequisite)
	fmt.Fprintln(c)
	answer, err := c.Prompt(fmt.Sprintf("Type %q to confirm, or anything else to abort: ", confirmAck))
	if err != nil {
		return storeErrorf(ReasonLegacyRecoveryRequired, "%s was not confirmed: %s", action, err)
	}
	if strings.TrimSpace(answer) != confirmAck {
		return storeErrorf(ReasonLegacyRecoveryRequired,
			"%s was not confirmed; the acknowledgement did not match, so nothing was changed", action)
	}
	return nil
}

// requireTypedIdentity requires the operator to type want exactly at the
// controlling terminal.
//
// Ordinary stdin cannot satisfy this: the answer comes from the tty, so a pipe,
// a here-document, an agent-launched command, or an environment variable cannot
// supply it even when it contains the correct text.
func requireTypedIdentity(c Confirmer, what, want, action string) error {
	if c == nil || !c.IsTerminal() {
		return storeErrorf(ReasonLegacyRecoveryRequired,
			"%s requires typing %s at a controlling terminal, and none is available", action, what)
	}
	answer, err := c.Prompt(fmt.Sprintf("To confirm, type the %s %s: ", what, want))
	if err != nil {
		return storeErrorf(ReasonLegacyRecoveryRequired, "%s was not confirmed: %s", action, err)
	}
	if strings.TrimSpace(answer) != want {
		return storeErrorf(ReasonLegacyRecoveryRequired,
			"%s was not confirmed: the typed %s did not match, so nothing was changed", action, what)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Terminal implementations
// ---------------------------------------------------------------------------

// ttyConfirmer is the production Confirmer: an independently opened controlling
// terminal used for both output and input.
type ttyConfirmer struct {
	path string
	out  io.Writer
	in   *bufio.Reader
	// closes release the handles. They are nil when the handles are not ours.
	closes []io.Closer
}

// Write writes to the controlling terminal, not to the caller's stdout.
func (t *ttyConfirmer) Write(p []byte) (int, error) {
	if t == nil || t.out == nil {
		return 0, storeErrorf(ReasonLegacyRecoveryRequired, "no controlling terminal is available")
	}
	return t.out.Write(p)
}

// IsTerminal reports whether a controlling terminal was opened.
func (t *ttyConfirmer) IsTerminal() bool { return t != nil && t.in != nil && t.out != nil }

// Description names the terminal device.
func (t *ttyConfirmer) Description() string {
	if t == nil || t.path == "" {
		return "the controlling terminal"
	}
	if !t.IsTerminal() {
		return t.path
	}
	return t.path
}

// Prompt writes a prompt and reads one line from the terminal.
func (t *ttyConfirmer) Prompt(prompt string) (string, error) {
	if !t.IsTerminal() {
		return "", storeErrorf(ReasonLegacyRecoveryRequired, "no controlling terminal is available")
	}
	if _, err := io.WriteString(t.out, prompt); err != nil {
		return "", storeError(ReasonUnreadableFile, fmt.Errorf("write to %s: %w", t.Description(), err))
	}
	line, err := t.in.ReadString('\n')
	if errors.Is(err, io.EOF) {
		if strings.TrimSpace(line) == "" {
			return "", ErrConfirmationEOF
		}
		// A final line with no trailing newline is still an answer.
		return line, nil
	}
	if err != nil {
		return "", storeError(ReasonUnreadableFile, fmt.Errorf("read from %s: %w", t.Description(), err))
	}
	return line, nil
}

// Close releases the terminal handles.
func (t *ttyConfirmer) Close() error {
	if t == nil {
		return nil
	}
	var errs []error
	for _, c := range t.closes {
		errs = append(errs, c.Close())
	}
	t.closes = nil
	return errors.Join(errs...)
}

var _ Confirmer = (*ttyConfirmer)(nil)

// unavailableConfirmer is the Confirmer returned when no controlling terminal
// could be opened. It reports the reason in Description so the refusal explains
// itself, and it can never answer a prompt.
type unavailableConfirmer struct{ reason string }

func (u *unavailableConfirmer) Write([]byte) (int, error) {
	return 0, storeErrorf(ReasonLegacyRecoveryRequired, "no controlling terminal: %s", u.reason)
}
func (u *unavailableConfirmer) IsTerminal() bool { return false }
func (u *unavailableConfirmer) Prompt(string) (string, error) {
	return "", storeErrorf(ReasonLegacyRecoveryRequired, "no controlling terminal: %s", u.reason)
}
func (u *unavailableConfirmer) Close() error { return nil }
func (u *unavailableConfirmer) Description() string {
	return "no controlling terminal (" + u.reason + ")"
}

var _ Confirmer = (*unavailableConfirmer)(nil)

// OpenConfirmer opens the process's CONTROLLING TERMINAL, independently of
// stdin, stdout, and stderr.
//
// The device differs by platform: `/dev/tty` on Unix, the console device
// (`CONIN$`/`CONOUT$`) on Windows. The path is opened directly rather than
// inherited, which is the whole point — a process with no controlling terminal
// (a daemon, a CI job, an agent's tool invocation) cannot open one, and the
// operation is then refused on that ground alone instead of accepting an answer
// that a pipe supplied.
//
// It never returns an error: an unopenable terminal yields a Confirmer whose
// IsTerminal is false, so a caller can report a refusal in the operator's own
// words instead of a bare error.
func OpenConfirmer() Confirmer {
	if runtime.GOOS == "windows" {
		// The Windows console device. Writing prompts to CONOUT$ and reading
		// answers from CONIN$ is the platform's equivalent of /dev/tty: it is
		// the console attached to this process, not the redirected std handles.
		out, outErr := os.OpenFile("CONOUT$", os.O_RDWR, 0)
		if outErr != nil {
			return &unavailableConfirmer{reason: "cannot open the console device CONOUT$: " + outErr.Error()}
		}
		in, inErr := os.OpenFile("CONIN$", os.O_RDWR, 0)
		if inErr != nil {
			_ = out.Close()
			return &unavailableConfirmer{reason: "cannot open the console device CONIN$: " + inErr.Error()}
		}
		return &ttyConfirmer{
			path:   "the console device",
			out:    out,
			in:     bufio.NewReader(in),
			closes: []io.Closer{in, out},
		}
	}
	f, err := os.OpenFile(controllingTerminalPath, os.O_RDWR, 0)
	if err != nil {
		return &unavailableConfirmer{
			reason: fmt.Sprintf("cannot open %s: %v", controllingTerminalPath, err),
		}
	}
	return &ttyConfirmer{
		path:   controllingTerminalPath,
		out:    f,
		in:     bufio.NewReader(f),
		closes: []io.Closer{f},
	}
}

// controllingTerminalPath is the Unix controlling-terminal device.
const controllingTerminalPath = "/dev/tty"

// ScriptedConfirmer is a Confirmer driven by canned input. It exists so every
// confirmation test runs deterministically and without a tty.
//
// Its zero value is a REFUSAL: no terminal and no input, which is exactly what a
// headless invocation must produce.
type ScriptedConfirmer struct {
	// Terminal reports whether a controlling terminal is available. The zero
	// value is false, which refuses.
	Terminal bool
	// Answers are the lines returned by successive prompts, in order. When they
	// run out, Prompt returns ErrConfirmationEOF — an exhausted script REFUSES
	// rather than answering.
	Answers []string
	// Output collects everything written to the terminal, so a test can assert
	// what the operator was actually shown. It is also returned by Text.
	Output strings.Builder
	// OnPrompt, when set, runs immediately before each answer is returned. It
	// exists so a test can change the target BETWEEN a confirmation and the act
	// it authorised, which is the "renewed confirmation is required" case.
	OnPrompt func(prompt string)

	next int
}

// IsTerminal reports whether this scripted terminal exists.
func (s *ScriptedConfirmer) IsTerminal() bool { return s != nil && s.Terminal }

// Description names the scripted terminal.
func (s *ScriptedConfirmer) Description() string {
	if s.IsTerminal() {
		return "scripted terminal"
	}
	return "no controlling terminal"
}

// Write records terminal output.
func (s *ScriptedConfirmer) Write(p []byte) (int, error) {
	if s == nil {
		return 0, storeErrorf(ReasonInternal, "scripted terminal is nil")
	}
	return s.Output.Write(p)
}

// Prompt returns the next scripted answer.
func (s *ScriptedConfirmer) Prompt(prompt string) (string, error) {
	if s == nil || !s.IsTerminal() {
		return "", storeErrorf(ReasonLegacyRecoveryRequired, "no controlling terminal is available")
	}
	if _, err := s.Output.WriteString(prompt); err != nil {
		return "", storeError(ReasonInternal, err)
	}
	if s.next >= len(s.Answers) {
		return "", ErrConfirmationEOF
	}
	answer := s.Answers[s.next]
	s.next++
	if s.OnPrompt != nil {
		s.OnPrompt(prompt)
	}
	return answer + "\n", nil
}

// Close is a no-op for a scripted terminal.
func (s *ScriptedConfirmer) Close() error { return nil }

// Text returns everything the scripted terminal has been shown or asked.
func (s *ScriptedConfirmer) Text() string {
	if s == nil {
		return ""
	}
	return s.Output.String()
}

var _ Confirmer = (*ScriptedConfirmer)(nil)

// ---------------------------------------------------------------------------
// Fingerprinting
// ---------------------------------------------------------------------------

// TargetFingerprint is a stable identity for one legacy store, used to detect
// that the thing a confirmation was obtained for is no longer the thing that
// would be destroyed.
//
// It is computed from the store's own refs and its measured size, NOT from its
// mtime: a reset releases the store lock while it waits for input, and during
// that window an old Marshal instance could add a snapshot without changing
// anything else this code would notice. The ref list and the measured size both
// change in that case, which is exactly what re-confirmation is for.
type TargetFingerprint struct {
	// Workspace is the workspace hash the fingerprint was taken for.
	Workspace string
	// Bytes is the measured store size at fingerprint time.
	Bytes int64
	// Refs is the sorted snapshot-hash list at fingerprint time.
	Refs []string
	// Branches is the sorted "ref hash" list at fingerprint time.
	Branches []string
	// Digest is a hash over the fields above.
	Digest string
}

// String renders the digest for diagnostics.
func (f *TargetFingerprint) String() string {
	if f == nil {
		return "<no fingerprint>"
	}
	return f.Digest
}

// Same reports whether two fingerprints describe the same store state.
func (f *TargetFingerprint) Same(other *TargetFingerprint) bool {
	if f == nil || other == nil {
		return false
	}
	return f.Workspace == other.Workspace && f.Digest == other.Digest
}

// fingerprintLegacy computes the current fingerprint of a legacy shadow store.
func (m *Manager) fingerprintLegacy(ctx context.Context, workspace string) (*TargetFingerprint, error) {
	path, err := m.containedLegacyStorePath(workspace)
	if err != nil {
		return nil, err
	}
	out := &TargetFingerprint{Workspace: workspace}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A store that is already gone has a well-defined fingerprint of
			// its own; it is not an error, and two "absent" fingerprints match.
			out.Digest = fingerprintDigest(out)
			return out, nil
		}
		return nil, m.storeError(ReasonUnreadableFile, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, m.storeErrorf(ReasonSymlinkEscape, path, "store path is a symlink")
	}
	if out.Bytes, err = m.measureTreeAllocated(path, info); err != nil {
		return nil, err
	}
	if gitDirExists(path) {
		_, hashes, err := m.snapshotRefsInDir(ctx, path)
		if err != nil {
			return nil, err
		}
		out.Refs = append(out.Refs, hashes...)
		branches, err := m.legacyBranches(ctx, path)
		if err != nil {
			return nil, err
		}
		for _, b := range branches {
			out.Branches = append(out.Branches, b.Ref+" "+b.Hash)
		}
	}
	sort.Strings(out.Refs)
	sort.Strings(out.Branches)
	out.Digest = fingerprintDigest(out)
	return out, nil
}

// fingerprintDigest hashes a fingerprint's fields.
func fingerprintDigest(f *TargetFingerprint) string {
	h := sha256.New()
	fmt.Fprintf(h, "workspace=%s\nbytes=%d\n", f.Workspace, f.Bytes)
	for _, r := range f.Refs {
		fmt.Fprintf(h, "ref=%s\n", r)
	}
	for _, b := range f.Branches {
		fmt.Fprintf(h, "branch=%s\n", b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Managed-root containment
// ---------------------------------------------------------------------------

// containedLegacyStorePath resolves the shadow-store path of one workspace and
// proves it is a DIRECT child of the managed snapshots root.
//
// Two checks, and both are load-bearing:
//
//  1. The workspace identifier must be a plain 12-hex hash, so no "../", no
//     absolute path, and no filesystem path at all can be smuggled through
//     `--workspace`. A user-supplied PATH is therefore never resolved for
//     deletion — the argument is an IDENTIFIER, and the path is derived from the
//     manager's own root.
//  2. The resolved path must still be a direct child of that root and must not
//     be a symlink. A symlink planted at the store path is refused rather than
//     followed, so nothing here can reach outside the store.
func (m *Manager) containedLegacyStorePath(workspace string) (string, error) {
	if err := m.checkReady(); err != nil {
		return "", err
	}
	if !validWorkspaceID(workspace) {
		return "", storeErrorf(ReasonInvalidObjectHash,
			"workspace %q is not a 12-character hexadecimal workspace hash, so it does not name a "+
				"store inside the managed snapshots root", workspace)
	}
	path, err := WorkspaceStorePath(m.root, workspace)
	if err != nil {
		return "", err
	}
	// The path was joined from a validated element and the manager's own root,
	// so it is contained by construction. It is verified anyway, because the
	// redundancy is what turns a future validation bug into a refusal instead of
	// a delete outside the store.
	rel, err := filepath.Rel(m.root, path)
	if err != nil {
		return "", storeError(ReasonSymlinkEscape, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		strings.Contains(rel, string(filepath.Separator)) {
		return "", storeErrorf(ReasonSymlinkEscape,
			"store path %q is not a direct child of the managed snapshots root %q", path, m.root)
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}
		return "", m.storeError(ReasonUnreadableFile, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", m.storeErrorf(ReasonSymlinkEscape, path,
			"store path is a symbolic link; refusing to act on it")
	}
	if !info.IsDir() {
		return "", m.storeErrorf(ReasonUnreadableFile, path, "store path is not a directory")
	}
	return path, nil
}

// proveInsideRoot refuses a path that is not inside the managed snapshots root.
func (m *Manager) proveInsideRoot(path string) error {
	rel, err := filepath.Rel(m.root, path)
	if err != nil {
		return storeError(ReasonSymlinkEscape, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(rel) {
		return m.storeErrorf(ReasonSymlinkEscape, path,
			"path %q is not inside the managed snapshots root %q", path, m.root)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The accounted deleting state
// ---------------------------------------------------------------------------

// legacyDeletingMarker is the infix of the name a store is renamed to before it
// is removed.
//
// Renaming first is what makes an interrupted reset RECOVERABLE. A crash while a
// store is being removed leaves a directory whose name says exactly what it is:
// an accounted remnant of a deletion that did not finish. The store's refs are
// already out of the lookup namespace at that point, so the deletion is
// committed rather than half-decided, and cleanup can simply finish it.
const legacyDeletingMarker = ".deleting-"

// formatLegacyDeletingName builds the remnant name for one workspace. The
// suffix keeps the remnant unique even when two recoveries against the same
// workspace are interrupted in quick succession.
func formatLegacyDeletingName(workspace, suffix string) string {
	return workspace + legacyDeletingMarker + suffix
}

// parseLegacyRemnantName splits a remnant name back into its workspace hash.
func parseLegacyRemnantName(name string) (string, bool) {
	i := strings.Index(name, legacyDeletingMarker)
	if i < 0 {
		return "", false
	}
	workspace := name[:i]
	if !validWorkspaceID(workspace) {
		return "", false
	}
	suffix := name[i+len(legacyDeletingMarker):]
	if suffix == "" || strings.ContainsAny(suffix, `/\`) {
		return "", false
	}
	return workspace, true
}

// ---------------------------------------------------------------------------
// Live-writer evidence
// ---------------------------------------------------------------------------

// DefaultLiveWriterWindow is how recently a recognized disposable artifact must
// have been modified for that to count as live-writer evidence.
//
// A window is needed because the artifact classes are asymmetric: a Git LOCK
// file is unambiguous evidence on its own (Git holds it only for the duration of
// a write), whereas a `tmp_pack_*` file can be either an active repack's work or
// years-old debris. Freshness is what separates them, and the window is generous
// because the cost of a false refusal (the operator retries later) is far lower
// than the cost of a false acceptance (an interrupted repack).
const DefaultLiveWriterWindow = 5 * time.Minute

// liveWriterEvidence is what was found, for the refusal message.
type liveWriterEvidence struct {
	// LockFiles are Git's own lock files, which prove an active writer.
	LockFiles []string
	// RecentArtifacts are recognized temporary artifacts modified inside the
	// freshness window.
	RecentArtifacts []string
	// Window is the window that was applied.
	Window time.Duration
}

// Empty reports whether there is no evidence at all.
func (e liveWriterEvidence) Empty() bool {
	return len(e.LockFiles) == 0 && len(e.RecentArtifacts) == 0
}

// collectLiveWriterEvidence looks for signs that something other than this
// process is writing a legacy store.
//
// It is deliberately CONSERVATIVE in what it concludes and HONEST about what it
// can conclude. Finding a lock file or a just-written temporary pack refuses the
// operation; finding nothing does NOT establish that no writer exists, which is
// why the offline acknowledgement is still required afterwards. This check is an
// extra guard on top of the prerequisite, never a substitute for it.
func (m *Manager) collectLiveWriterEvidence(ctx context.Context) (liveWriterEvidence, error) {
	ev := liveWriterEvidence{Window: m.liveWriterWindowEffective()}
	survey, err := m.SurveyLegacy(ctx)
	if err != nil {
		return ev, err
	}
	for _, l := range survey.Stores {
		if !l.IsRepository {
			continue
		}
		one, err := m.liveWriterEvidenceIn(l.Path)
		if err != nil {
			return ev, err
		}
		ev.LockFiles = append(ev.LockFiles, one.LockFiles...)
		ev.RecentArtifacts = append(ev.RecentArtifacts, one.RecentArtifacts...)
	}
	sort.Strings(ev.LockFiles)
	sort.Strings(ev.RecentArtifacts)
	return ev, nil
}

// liveWriterEvidenceIn looks for live-writer evidence in one store.
func (m *Manager) liveWriterEvidenceIn(dir string) (liveWriterEvidence, error) {
	ev := liveWriterEvidence{Window: m.liveWriterWindowEffective()}
	locks, err := m.legacyLockFiles(dir)
	if err != nil {
		return ev, err
	}
	ev.LockFiles = locks
	if ev.Window > 0 {
		cutoff := m.Now().Add(-ev.Window)
		disposables, _, err := m.legacyPackArtifacts(dir)
		if err != nil {
			return ev, err
		}
		for _, d := range disposables {
			info, err := os.Lstat(d.Path)
			if err != nil {
				continue
			}
			if info.ModTime().After(cutoff) {
				ev.RecentArtifacts = append(ev.RecentArtifacts, d.Path)
			}
		}
	}
	return ev, nil
}

// refuseIfLiveWriter refuses the operation when there is evidence of an active
// legacy writer anywhere under the managed root.
func (m *Manager) refuseIfLiveWriter(ctx context.Context) error {
	ev, err := m.collectLiveWriterEvidence(ctx)
	if err != nil {
		return err
	}
	return liveWriterRefusal(ev)
}

// refuseIfLiveWriterIn refuses the operation when there is evidence of an active
// writer in one store.
func (m *Manager) refuseIfLiveWriterIn(dir string) error {
	ev, err := m.liveWriterEvidenceIn(dir)
	if err != nil {
		return err
	}
	return liveWriterRefusal(ev)
}

// liveWriterRefusal turns evidence into a REFUSAL, or nil when there is none.
//
// TWO classes of evidence refuse, and they differ in what they prove:
//
//   - A Git LOCK file is PROOF. Git creates it for the duration of a write and
//     removes it afterwards, so one that exists now is held by a writer now.
//   - A `tmp_pack_*` file modified inside the freshness window is OBVIOUS
//     EVIDENCE. It cannot be distinguished from an active repack by this
//     process, and the plan's instruction for this case is to refuse.
//
// Refusing on the second class is conservative by choice, and its cost is
// bounded: the window is minutes, so waiting out a false positive costs the
// operator a retry, while acting on a true positive would mean deleting a live
// repack's work. The window is a manager option
// (WithLiveWriterWindow) precisely so this trade can be exercised rather than
// assumed, and a non-positive window disables only the freshness heuristic —
// the lock-file check is never disabled, because it needs no heuristic.
func liveWriterRefusal(ev liveWriterEvidence) error {
	if len(ev.LockFiles) > 0 {
		return storeErrorf(ReasonLegacyRecoveryRequired,
			"a Git lock file exists inside the snapshot store (%s), which means a writer is active right now; "+
				"stop it and try again", strings.Join(ev.LockFiles, ", "))
	}
	if len(ev.RecentArtifacts) > 0 {
		return storeErrorf(ReasonLegacyRecoveryRequired,
			"a temporary pack file was modified within the last %s (%s), which is what an active repack "+
				"looks like; if no repack is running, wait %s and try again",
			ev.Window, strings.Join(ev.RecentArtifacts, ", "), ev.Window)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

// RemovedArtifact records one removed path and its measured size.
type RemovedArtifact struct {
	// Workspace is the workspace hash the artifact belonged to.
	Workspace string
	// Path is the artifact's path inside the managed root.
	Path string
	// Kind names the artifact class.
	Kind string
	// Bytes is the artifact's measured size before removal.
	Bytes int64
}

// CleanupReport is what one cleanup pass did.
type CleanupReport struct {
	// Reconciled lists the versioned workspaces reconciliation finished work in.
	Reconciled []string
	// StagingRemoved counts the capture scratch directories the automatic v2
	// reconciliation removed.
	StagingRemoved int
	// GenerationsRemoved lists the versioned generations removed by automatic
	// reconciliation and retention.
	GenerationsRemoved []string
	// TempArtifactsRemoved lists the recognized legacy temporary artifacts that
	// were removed, with their sizes.
	TempArtifactsRemoved []RemovedArtifact
	// TempBytes is the measured size of those artifacts.
	TempBytes int64
	// RemnantsRemoved lists the accounted-deleting remnants removed.
	RemnantsRemoved []RemovedArtifact
	// RemnantBytes is the measured size of those remnants.
	RemnantBytes int64
	// LegacyUntouched lists the legacy stores that were inspected and left
	// exactly as they were.
	LegacyUntouched []string
	// Confirmed reports whether the offline acknowledgement was obtained, and so
	// whether legacy mutation was permitted at all.
	Confirmed bool
	// Warnings are the structured problems found.
	Warnings []MaintenanceWarning
}

// CleanupOptions configures one cleanup pass.
type CleanupOptions struct {
	// Confirmer is the controlling terminal used to collect the offline
	// acknowledgement. A nil or non-terminal Confirmer leaves every legacy
	// temporary artifact in place.
	Confirmer Confirmer
	// RetentionDays is passed to the automatic v2 retention pass exactly as it
	// is everywhere else: negative disables expiry.
	RetentionDays int
}

// Cleanup reconciles the VERSIONED store automatically, and removes legacy
// temporary artifacts ONLY under the offline acknowledgement.
//
// The split is the design's, and it is not a detail:
//
//   - VERSIONED reconciliation needs no acknowledgement. A v2 store's
//     interrupted work is described durably by its own manifest, and v2 writers
//     honour the store lock this process holds — so finishing their work is safe
//     by construction rather than by assertion.
//   - LEGACY mutation needs the acknowledgement, because nothing here can prove
//     an old writer is absent.
//
// A cleanup with no terminal is therefore still USEFUL: it reconciles v2 and
// reports what legacy work is outstanding. It simply does not touch legacy.
func (m *Manager) Cleanup(ctx context.Context, opts CleanupOptions) (*CleanupReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	if !m.Owned() {
		return nil, storeErrorf(ReasonNotOwned,
			"cleanup requires exclusive ownership of the snapshot store")
	}
	report := &CleanupReport{}

	// 1. Automatic reconciliation and retention of the VERSIONED store. This is
	//    the same bounded, whole-generation work the runtime performs; calling it
	//    here means `snapshots cleanup` gets the same repair a session start
	//    gets, without having to start a session.
	reconciled, err := m.Reconcile(ctx)
	if err != nil {
		return report, err
	}
	for _, ws := range reconciled.Workspaces {
		report.Reconciled = append(report.Reconciled, ws.Workspace)
		report.StagingRemoved += len(ws.StagingRemoved)
		report.GenerationsRemoved = append(report.GenerationsRemoved, ws.GenerationsRemoved...)
	}
	report.LegacyUntouched = append(report.LegacyUntouched, reconciled.LegacyUntouched...)
	report.Warnings = append(report.Warnings, reconciled.Warnings...)

	if opts.RetentionDays >= 0 {
		expired, err := m.ReclaimExpired(ctx, opts.RetentionDays)
		if err != nil {
			return report, err
		}
		if expired != nil {
			for _, r := range expired.Reclaimed {
				if r.Error == nil {
					report.GenerationsRemoved = append(report.GenerationsRemoved, r.ID)
				}
			}
			report.Warnings = append(report.Warnings, expired.Warnings...)
		}
	}

	// 2. The legacy survey, which is read-only and always runs: the report has to
	//    say what is outstanding even when nothing may be removed.
	survey, err := m.SurveyLegacy(ctx)
	if err != nil {
		return report, err
	}
	report.Warnings = append(report.Warnings, survey.Warnings...)

	// 3. Remnants of an interrupted reset are removed WITHOUT a further prompt.
	//    They are Marshal's own accounted state: the confirmation that authorised
	//    destroying that history was already obtained, and the store was already
	//    renamed out of the lookup namespace, so leaving the bytes behind forever
	//    would be a leak rather than a safety property.
	if err := m.removeLegacyRemnants(ctx, survey, report); err != nil {
		return report, err
	}

	// 4. Legacy temporary artifacts, only under the offline acknowledgement.
	if legacyMutationPending(survey) {
		if err := m.removeLegacyTemporaries(ctx, survey, opts, report); err != nil {
			return report, err
		}
	}
	return report, nil
}

// legacyMutationPending reports whether a cleanup has legacy work that would
// need the offline acknowledgement.
func legacyMutationPending(survey *LegacySurvey) bool {
	if survey == nil {
		return false
	}
	for _, l := range survey.Stores {
		if l.DisposableBytes > 0 {
			return true
		}
	}
	return false
}

// removeLegacyTemporaries removes the recognized temporary artifacts of every
// legacy store that has any, under one offline acknowledgement.
//
// The acknowledgement is collected ONCE and covers the pass, because it is a
// statement about the operator's machine (no old Marshal instance is running)
// rather than about one file.
func (m *Manager) removeLegacyTemporaries(ctx context.Context, survey *LegacySurvey, opts CleanupOptions, report *CleanupReport) error {
	// Live-writer evidence is checked FIRST, before the operator is asked
	// anything: there is no point obtaining an acknowledgement to mutate a store
	// this process has already observed a writer inside.
	if err := m.refuseIfLiveWriter(ctx); err != nil {
		return err
	}
	if err := requireOfflineAcknowledgement(opts.Confirmer, "cleanup of abandoned temporary files"); err != nil {
		// A refused acknowledgement is not a failed cleanup: the v2 work above
		// already happened, and the legacy stores are reported as untouched.
		report.Warnings = append(report.Warnings, MaintenanceWarning{
			Reason:  ReasonLegacyRecoveryRequired,
			Message: "legacy temporary files were not removed: " + err.Error(),
		})
		return nil
	}
	report.Confirmed = true

	for _, l := range survey.Stores {
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.DisposableBytes == 0 {
			continue
		}
		// Re-check immediately before removing. The acknowledgement was
		// collected while this process held the lock, but a legacy writer ignores
		// the lock entirely, so the state observed a moment ago is not proof
		// about the state now.
		if err := m.refuseIfLiveWriterIn(l.Path); err != nil {
			report.Warnings = append(report.Warnings, MaintenanceWarning{
				Reason:    ReasonOf(err),
				Workspace: l.Workspace,
				Path:      l.Path,
				Message:   "legacy temporary files were left in place: " + err.Error(),
			})
			continue
		}
		disposables, _, err := m.legacyPackArtifacts(l.Path)
		if err != nil {
			return err
		}
		for _, d := range disposables {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := m.removeRecognizedArtifact(d.Path, d.Name); err != nil {
				return err
			}
			report.TempArtifactsRemoved = append(report.TempArtifactsRemoved,
				RemovedArtifact{Workspace: l.Workspace, Path: d.Path, Kind: d.Kind, Bytes: d.Bytes})
			if report.TempBytes, err = addInt64(report.TempBytes, d.Bytes); err != nil {
				return err
			}
		}
	}
	return nil
}

// hasTempSuffix reports whether name carries a recognized temporary suffix.
func hasTempSuffix(name string) bool {
	for _, suffix := range gitTempSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// removeRecognizedArtifact unlinks one recognized disposable artifact, proving
// first that the path is still inside the managed root, that it still carries
// the recognized name, and that it is not a directory.
//
// A symlink is unlinked as a link; it is never followed.
func (m *Manager) removeRecognizedArtifact(path, name string) error {
	if err := m.proveInsideRoot(path); err != nil {
		return err
	}
	if filepath.Base(path) != name {
		return m.storeErrorf(ReasonSymlinkEscape, path, "artifact name changed under us; refusing to remove it")
	}
	if !strings.HasPrefix(name, gitTempPackPrefix) && !hasTempSuffix(name) {
		return m.storeErrorf(ReasonInternal, path,
			"refusing to remove %q: its name is not a recognized temporary artifact", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return m.storeError(ReasonUnreadableFile, path, err)
	}
	if info.IsDir() {
		return m.storeErrorf(ReasonUnreadableFile, path,
			"recognized temporary artifact is a directory; refusing to remove it")
	}
	if err := os.Remove(path); err != nil {
		return m.storeError(ReasonUnreadableFile, path, err)
	}
	return nil
}

// removeLegacyRemnants finishes the deletion of stores an earlier reset renamed
// aside.
func (m *Manager) removeLegacyRemnants(ctx context.Context, survey *LegacySurvey, report *CleanupReport) error {
	for _, r := range survey.Remnants {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.proveInsideRoot(r.Path); err != nil {
			report.Warnings = append(report.Warnings, MaintenanceWarning{
				Reason:    ReasonOf(err),
				Workspace: r.Workspace,
				Path:      r.Path,
				Message:   "an interrupted-reset remnant was left in place: " + err.Error(),
			})
			continue
		}
		if _, ok := parseLegacyRemnantName(filepath.Base(r.Path)); !ok {
			continue
		}
		if err := removeDirectoryNoFollow(r.Path); err != nil {
			return m.storeError(ReasonUnreadableFile, r.Path, err)
		}
		report.RemnantsRemoved = append(report.RemnantsRemoved,
			RemovedArtifact{Workspace: r.Workspace, Path: r.Path, Kind: "deleting_remnant", Bytes: r.Bytes})
		total, addErr := addInt64(report.RemnantBytes, r.Bytes)
		if addErr != nil {
			return addErr
		}
		report.RemnantBytes = total
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared recovery plumbing
// ---------------------------------------------------------------------------

// RecoveryOptions configures one destructive legacy recovery operation.
type RecoveryOptions struct {
	// Confirmer is the controlling terminal that collects BOTH the offline
	// acknowledgement and the typed identity. It is required for every mutating
	// operation; a nil or non-terminal Confirmer refuses.
	Confirmer Confirmer
}

// promptWithReleasedLock drops store ownership for the duration of the
// confirmation, then ALWAYS retakes it — including when the confirmation was
// refused.
//
// Both halves matter. Releasing means an interactive prompt never holds the
// cross-process store lock for as long as a human takes to read it. Retaking it
// unconditionally is what keeps the CALLER's ownership invariant true: a caller
// that acquired the store owns it until it releases it, and a refused prompt
// must not silently hand the store to another process while the caller still
// believes it holds the lock.
//
// A failure to retake is reported rather than swallowed, because the caller's
// next action would otherwise run with no ownership at all.
func (m *Manager) promptWithReleasedLock(ctx context.Context, fn func() error) error {
	if err := m.releaseForInput(); err != nil {
		return err
	}
	promptErr := fn()
	if _, reacquireErr := m.Acquire(ctx); reacquireErr != nil && promptErr == nil {
		return storeError(ReasonUnreadableFile,
			fmt.Errorf("retake store ownership after confirmation: %w", reacquireErr))
	}
	return promptErr
}

// releaseForInput drops store ownership while the operator is being asked a
// question.
//
// Releasing is safe here because the mutation has not started: nothing has been
// written, and the pre-mutation fingerprint is compared against a fresh one
// after ownership is retaken.
func (m *Manager) releaseForInput() error {
	if !m.Owned() {
		return nil
	}
	if err := m.Release(); err != nil {
		return storeError(ReasonUnreadableFile,
			fmt.Errorf("release store ownership while awaiting confirmation: %w", err))
	}
	return nil
}

// revalidateTarget re-fingerprints a legacy store and refuses if it is no longer
// the store the operator confirmed.
//
// A fingerprint that no longer matches means the thing the operator confirmed is
// not the thing that would be destroyed — an old Marshal instance could have
// appended history during the prompt, and the lock was never proof against it.
// That requires RENEWED confirmation, so the operation refuses rather than
// proceeding on a stale decision.
//
// Ownership must already be held; see promptWithReleasedLock.
func (m *Manager) revalidateTarget(ctx context.Context, before *TargetFingerprint) error {
	after, err := m.fingerprintLegacy(ctx, before.Workspace)
	if err != nil {
		return err
	}
	if !before.Same(after) {
		return storeErrorf(ReasonLegacyRecoveryRequired,
			"workspace %s changed between the confirmation and the operation (fingerprint %s then %s); "+
				"nothing was changed — re-run to confirm the new state",
			before.Workspace, before.Digest, after.Digest)
	}
	return nil
}

// ensureLegacyManifest loads the workspace's manifest, creating an EMPTY one
// when none exists.
//
// It is the legacy counterpart of ensureManifest and differs from it in exactly
// one way: it does not require a recorded workspace root. A pre-v2 store records
// only a workspace hash, so a migration has no root to write, and inventing one
// would fabricate provenance the store never carried. The manifest's Root is
// therefore left empty.
func (c *Catalog) ensureLegacyManifest() (*Manifest, error) {
	man, err := c.Load()
	if err != nil {
		return nil, err
	}
	if man != nil {
		return man, nil
	}
	if err := c.requireOwned(); err != nil {
		return nil, err
	}
	man = &Manifest{}
	if err := c.Save(man); err != nil {
		return nil, err
	}
	return man, nil
}

// createLegacyGeneration creates the destination of a migration: a generation
// record that is `creating`, legacy-origin, and PROTECTED, followed by its bare
// repository.
//
// The record is marked legacy-origin and protected BEFORE a single object is
// copied, and that ordering is the whole safety story of an interrupted
// migration:
//
//   - A crash before any ref is published leaves a `creating`, protected,
//     legacy-origin record. Reconciliation refuses to remove a protected
//     generation, so even a partial copy is preserved for inspection rather than
//     silently deleted.
//   - A crash after refs are published leaves the same record with refs, and
//     reconciliation seals it — the durable published ref wins, as everywhere
//     else.
//
// The generation is never activated, and that is deliberate: migrated legacy
// history is a SEALED archive, not the workspace's next capture target.
func (m *Manager) createLegacyGeneration(ctx context.Context, cat *Catalog) (*GenerationRecord, error) {
	man, err := cat.ensureLegacyManifest()
	if err != nil {
		return nil, err
	}
	if len(man.Generations) >= MaxGenerationsPerWorkspace {
		return nil, cat.errf(ReasonBudgetExhausted,
			"manifest already lists %d generations, at the %d bound", len(man.Generations), MaxGenerationsPerWorkspace)
	}
	suffix, err := randomSuffix(generationIDSuffixLen)
	if err != nil {
		return nil, cat.err(ReasonInternal, err)
	}
	rec := GenerationRecord{
		ID:        formatGenerationID(man.NextSeq, suffix),
		State:     GenerationCreating,
		Origin:    OriginLegacy,
		Protected: true,
		// Root is deliberately empty: a legacy store records no root, and
		// inventing one would misstate where the history came from.
		CreatedAt: m.Now().UTC(),
	}
	man.NextSeq++
	man.Generations = append(man.Generations, rec)
	if err := cat.Save(man); err != nil {
		return nil, err
	}
	if err := m.runHook(m.hooks.Recovery.AfterLegacyGenerationRecord); err != nil {
		return nil, err
	}
	if err := cat.InitGenerationRepo(ctx, rec.ID); err != nil {
		return nil, err
	}
	return &rec, nil
}

// runRecoveryHook invokes a recovery hook when it is installed.
func (m *Manager) runRecoveryHook(fn func() error) error { return m.runHook(fn) }

// ---------------------------------------------------------------------------
// Migration
// ---------------------------------------------------------------------------

// MigrationReport is the outcome of one legacy migration.
type MigrationReport struct {
	// Workspace is the workspace hash migrated.
	Workspace string
	// GenerationID is the sealed legacy-origin generation that now holds the
	// migrated history.
	GenerationID string
	// Objects is the number of objects copied.
	Objects int64
	// Bytes is the conservative size of the copied closure.
	Bytes int64
	// SnapshotHashes lists the lookup-able hashes preserved.
	SnapshotHashes []string
	// BranchHashes lists the branch tips that were published as snapshot refs
	// because they named valid snapshots.
	BranchHashes []string
	// DuplicateRemoved reports whether the original legacy store was removed
	// after the swap was verified.
	DuplicateRemoved bool
	// RemnantPath is where the original store was renamed to when its removal did
	// not complete. `marshal snapshots cleanup` finishes it.
	RemnantPath string
	// Warnings are the problems found.
	Warnings []MaintenanceWarning
}

// MigrateWorkspace migrates one pre-v2 store into a protected, sealed,
// legacy-origin generation, preserving every lookup-able hash.
//
// The sequence, and why each step is where it is:
//
//  1. Refuse unless the store exists, is a repository, and its refs and closure
//     can be ENUMERATED. A closure that cannot be described cannot be preserved,
//     and "unknown" must never be treated as "nothing".
//  2. Refuse on live-writer evidence, then require the offline acknowledgement.
//  3. Check the closure fits: the SOURCE and the DESTINATION are accounted
//     CONCURRENTLY, because the original duplicate still exists until step 7.
//     A closure that cannot fit REFUSES — this code never silently migrates only
//     the recent history.
//  4. Copy the whole reachable closure, byte-for-byte hash-preserving, through
//     the manager's own budgeted loose-object writer. Every copied object's hash
//     is re-derived and required to match the source's.
//  5. Publish a snapshot ref for every legacy snapshot hash AND for every branch
//     tip that names a valid snapshot, so each one remains rollback-able.
//  6. Seal the generation (an atomic manifest replace). The v2 generations are
//     consulted before the legacy fallback, so this IS the lookup swap, and it
//     happens BEFORE the duplicate is removed.
//  7. Verify every preserved hash resolves to a non-legacy location, then rename
//     the original store into the accounted deleting state and remove it.
func (m *Manager) MigrateWorkspace(ctx context.Context, workspace string, opts RecoveryOptions) (*MigrationReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	if !m.Owned() {
		return nil, storeErrorf(ReasonNotOwned,
			"migration requires exclusive ownership of the snapshot store")
	}
	report := &MigrationReport{Workspace: workspace}

	path, err := m.containedLegacyStorePath(workspace)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, storeErrorf(ReasonLegacyRecoveryRequired,
				"workspace %s has no old-format store at %s, so there is nothing to migrate", workspace, path)
		}
		return nil, m.storeError(ReasonUnreadableFile, path, err)
	}
	if !info.IsDir() {
		return nil, m.storeErrorf(ReasonLegacyRecoveryRequired, path,
			"old-format store path is not a directory")
	}
	if !gitDirExists(path) {
		return nil, m.storeErrorf(ReasonLegacyRecoveryRequired, path,
			"old-format store directory is not a Git repository, so its history cannot be enumerated")
	}

	// 1. Enumerate before asking anything: an enumeration failure is a fact about
	//    the store, not a question for the operator.
	legacy, err := m.inspectLegacy(ctx, workspace, path, info)
	if err != nil {
		return nil, err
	}
	report.Warnings = append(report.Warnings, legacy.Warnings...)
	if legacy.RefsUnreadable {
		return nil, storeErrorf(ReasonUnreadableFile, path,
			"old-format snapshot refs could not be read, so a hash-preserving migration cannot be verified")
	}
	if legacy.ClosureError != nil {
		return nil, m.storeError(ReasonUnreadableFile, path,
			fmt.Errorf("old-format object closure could not be enumerated: %w", legacy.ClosureError))
	}
	if !legacy.RetainsHistory() {
		report.Warnings = append(report.Warnings, MaintenanceWarning{
			Reason:    ReasonLegacyRecoveryRequired,
			Workspace: workspace,
			Path:      path,
			Message:   "old-format store retains no snapshots and no branches, so there is nothing to migrate",
		})
		return report, nil
	}

	// 2. Live-writer evidence, then the offline acknowledgement.
	if err := m.refuseIfLiveWriterIn(path); err != nil {
		return nil, err
	}
	cat, err := m.Catalog(workspace)
	if err != nil {
		return nil, err
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}
	allowance, err := migrationAllowance(legacy.ParentsNeeded, limits)
	if err != nil {
		return nil, err
	}
	refusal, err := m.checkMigrationFits(ctx, cat, path, legacy, allowance, limits)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return nil, refusal
	}

	// Tell the operator exactly what is about to happen, then collect the
	// prerequisite from the CONTROLLING TERMINAL. Ownership is dropped first so
	// the prompt never holds the cross-process lock.
	source, err := m.fingerprintLegacy(ctx, workspace)
	if err != nil {
		return nil, err
	}
	notice := migrationNotice(workspace, path, legacy, limits, allowance)
	if err := m.promptWithReleasedLock(ctx, func() error {
		return requireOfflineAcknowledgement(opts.Confirmer, "migration of old-format snapshot history", notice)
	}); err != nil {
		return nil, err
	}
	// The store state is revalidated AFTER ownership is retaken, because a
	// legacy writer ignores the lock entirely.
	if err := m.revalidateTarget(ctx, source); err != nil {
		return nil, err
	}
	if err := m.refuseIfLiveWriterIn(path); err != nil {
		return nil, err
	}

	// 3. Copy, publish, seal, verify, then remove the duplicate.
	rec, err := m.createLegacyGeneration(ctx, cat)
	if err != nil {
		return nil, err
	}
	report.GenerationID = rec.ID
	genDir, err := cat.GenerationDir(rec.ID)
	if err != nil {
		return nil, err
	}

	budget, err := newObjectBudget(limits, allowance)
	if err != nil {
		return nil, err
	}
	writer, err := newObjectWriter(ctx, m, genDir, budget)
	if err != nil {
		return nil, err
	}
	defer writer.cleanup()

	copied, err := m.copyLegacyClosure(ctx, path, genDir, writer)
	if err != nil {
		return report, err
	}
	report.Objects = copied
	report.Bytes = writer.bytesWritten()
	if err := m.runRecoveryHook(m.hooks.Recovery.AfterLegacyObjectsCopied); err != nil {
		return report, err
	}

	preserved, branchHashes, err := m.publishMigratedRefs(ctx, cat, rec.ID, legacy)
	if err != nil {
		return report, err
	}
	report.SnapshotHashes = preserved
	report.BranchHashes = branchHashes
	if err := m.runRecoveryHook(m.hooks.Recovery.AfterLegacyRefsPublished); err != nil {
		return report, err
	}

	// 6. The lookup swap: sealing publishes the generation as usable in one
	//    atomic manifest replace, before any duplicate is touched.
	if err := cat.SealGeneration(rec.ID); err != nil {
		return report, err
	}
	if err := m.runRecoveryHook(m.hooks.Recovery.AfterLegacyGenerationSealed); err != nil {
		return report, err
	}

	// 7. Verify the swap, and only then remove the duplicate.
	hashes := append([]string(nil), preserved...)
	hashes = append(hashes, branchHashes...)
	if err := m.verifyMigratedHashes(ctx, cat, hashes); err != nil {
		return report, err
	}
	removed, remnant, err := m.removeLegacyDuplicate(ctx, workspace, path)
	if err != nil {
		return report, err
	}
	report.DuplicateRemoved = removed
	report.RemnantPath = remnant
	if !removed && remnant != "" {
		report.Warnings = append(report.Warnings, MaintenanceWarning{
			Reason:    ReasonLegacyRecoveryRequired,
			Workspace: workspace,
			Path:      remnant,
			Message: "the migrated history is safe in the new store, but the original duplicate could not be " +
				"fully removed; run 'marshal snapshots cleanup' to finish it",
		})
	}
	return report, nil
}

// migrationAllowance is the destination writer's reservation: the enumerated
// closure, plus a generous allowance for the object directories the writer
// charges once each.
func migrationAllowance(closure int64, limits Limits) (int64, error) {
	unit, err := limits.RoundUp(0)
	if err != nil {
		return 0, err
	}
	// At most 256 possible two-hex object directories, plus the writer's incoming
	// directory and its own metadata. One unit each, with slack.
	dirs, err := mulDiv(320, unit, 1)
	if err != nil {
		return 0, err
	}
	return addInt64(closure, dirs)
}

// checkMigrationFits compares the migration's total cost against every ceiling,
// accounting the SOURCE and the DESTINATION at the same time.
//
// The two failure kinds are deliberately distinguishable:
//
//   - a returned *StoreError is a REFUSAL: the migration does not fit, and the
//     store is left exactly as it was;
//   - a returned error is an inability to decide, because the store could not be
//     MEASURED. An under-measured destination is how a migration would run the
//     filesystem out of room half way through, so unknown accounting is a hard
//     failure rather than a refusal.
func (m *Manager) checkMigrationFits(ctx context.Context, cat *Catalog, path string, legacy *LegacyInfo,
	allowance int64, limits Limits) (refusal *StoreError, err error) {

	usage, err := m.UsageContext(ctx)
	if err != nil {
		return nil, m.storeError(ReasonUnreadableFile, m.Root(),
			fmt.Errorf("the snapshot store could not be measured, so a migration cannot be admitted: %w", err))
	}
	gens := usage.Generations[cat.Workspace()]
	var v2Allocated int64
	if gens != nil {
		v2Allocated = gens.Allocated
	}

	// Destination: the copy plus what the workspace's versioned store already
	// holds. Source: the legacy store's own bytes, which are NOT free — the
	// original duplicate stays on disk until the swap is verified — and which the
	// global total below already includes.
	destProjected, err := addInt64(v2Allocated, allowance)
	if err != nil {
		return nil, err
	}
	globalProjected, err := addInt64(usage.GlobalAllocated, allowance)
	if err != nil {
		return nil, err
	}
	if destProjected > limits.WorkspaceMaxBytes {
		return m.storeErr(ReasonBudgetExhausted, path,
			"migration would need %d bytes in workspace %s (the new generation must hold the %d byte closure "+
				"on top of the %d bytes already there) but the workspace budget is %d bytes; the migration is "+
				"refused rather than copying only part of the history",
			destProjected, cat.Workspace(), allowance, v2Allocated, limits.WorkspaceMaxBytes), nil
	}
	if globalProjected > limits.GlobalMaxBytes {
		return m.storeErr(ReasonBudgetExhausted, m.Root(),
			"migration would need %d bytes across every managed store (the destination's %d byte closure is "+
				"charged while the %d byte original still exists) but the global budget is %d bytes; the "+
				"migration is refused rather than copying only part of the history",
			globalProjected, allowance, legacy.Bytes, limits.GlobalMaxBytes), nil
	}
	if err := m.CheckFreeSpace(allowance); err != nil {
		var se *StoreError
		if errors.As(err, &se) {
			return se, nil
		}
		return nil, err
	}
	return nil, nil
}

// copyLegacyClosure copies every object of the reachable closure from source to
// the destination generation, hash-preserving.
//
// Blobs are STREAMED from a read-only `git cat-file blob` through the manager's
// own budgeted writer, so a multi-gigabyte blob is never held in memory. Trees,
// commits, and tags are small and bounded, so they are read into memory and
// installed directly.
//
// The hash of every copied object is re-derived from the content that was
// written and required to equal the source's object name. That check is the
// migration's central promise: "every lookup-able hash survives" means the
// OBJECT is the same object, so its name must be unchanged.
func (m *Manager) copyLegacyClosure(ctx context.Context, srcDir, genDir string, w *objectWriter) (int64, error) {
	var copied int64
	err := m.walkLegacyClosure(ctx, srcDir, func(hash, objType string, size int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch objType {
		case objTypeBlob:
			src, err := m.newLegacyObjectSource(ctx, srcDir, objType, hash, size)
			if err != nil {
				return err
			}
			written, err := w.writeBlob(src)
			closeErr := src.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if written != hash {
				return m.storeErrorf(ReasonUnverifiedObject, genDir,
					"copied blob hashes to %s but the source named %s", written, hash)
			}
		default:
			raw, err := m.gitOutput(ctx, srcDir, "cat-file", objType, hash)
			if err != nil {
				return err
			}
			obj, err := newGitObject(objType, raw)
			if err != nil {
				return err
			}
			if obj.Hash != hash {
				return m.storeErrorf(ReasonUnverifiedObject, srcDir,
					"object %s read back as %s; refusing to copy a renamed object", hash, obj.Hash)
			}
			if _, err := w.installObject(obj); err != nil {
				return err
			}
		}
		copied++
		return nil
	})
	if err != nil {
		return copied, err
	}
	// The copied objects must be readable back through Git before anything is
	// published: a closure that cannot be resolved is not a preserved closure.
	if err := m.verifyClosureReadable(ctx, genDir); err != nil {
		return copied, err
	}
	return copied, nil
}

// verifyClosureReadable requires the destination repository to be a usable Git
// repository whose object store is intact.
func (m *Manager) verifyClosureReadable(ctx context.Context, genDir string) error {
	if !repositoryLooksInitialised(genDir) {
		return m.storeErrorf(ReasonUnreadableFile, genDir,
			"migrated generation is not an initialised bare repository")
	}
	// --batch-check with no input still proves the object database can be opened
	// and that no pack machinery is involved; the per-object proof is the hash
	// check the copy already performed.
	if _, err := m.gitOutput(ctx, genDir, "count-objects", "-v"); err != nil {
		return err
	}
	return nil
}

// publishMigratedRefs publishes a snapshot ref for every legacy snapshot hash
// and for every branch tip that names a valid snapshot.
//
// Branch tips are published deliberately. A branch in a pre-v2 store was a
// SECOND RETENTION ROOT — that is precisely why pruning the snapshot refs freed
// nothing — and the commit it named was a valid snapshot of the workspace. Its
// object closure is being copied anyway, so publishing a ref for it preserves the
// rollback point instead of quietly discarding it.
//
// Duplicates between the two sets collapse into one ref, because a ref is
// derived from the hash.
func (m *Manager) publishMigratedRefs(ctx context.Context, cat *Catalog, generationID string, legacy *LegacyInfo) (snapshots, branches []string, err error) {
	seen := make(map[string]bool, legacy.SnapshotRefs+len(legacy.Branches))
	for _, hash := range legacy.SnapshotHashes {
		if seen[hash] {
			continue
		}
		seen[hash] = true
		if _, err := cat.PublishSnapshotRef(ctx, generationID, hash); err != nil {
			return nil, nil, err
		}
		snapshots = append(snapshots, hash)
	}
	for _, b := range legacy.Branches {
		hash := b.Hash
		if seen[hash] {
			continue
		}
		seen[hash] = true
		if _, err := cat.PublishSnapshotRef(ctx, generationID, hash); err != nil {
			return nil, nil, err
		}
		branches = append(branches, hash)
	}
	sort.Strings(snapshots)
	sort.Strings(branches)
	return snapshots, branches, nil
}

// verifyMigratedHashes proves every preserved hash resolves to a NON-legacy
// location, and that the location's repository can still serve it.
//
// This is the check that gates removing the duplicate. If any hash resolves to
// the legacy store, or cannot be resolved at all, the migration is not verified
// and the original is left exactly where it is.
func (m *Manager) verifyMigratedHashes(ctx context.Context, cat *Catalog, hashes []string) error {
	for _, hash := range hashes {
		if err := ctx.Err(); err != nil {
			return err
		}
		loc, err := lookupSnapshotOwned(ctx, m, cat, hash)
		if err != nil {
			return m.storeError(ReasonUnverifiedObject, cat.Root(),
				fmt.Errorf("snapshot %s is not retained by the migrated generation: %w", hash, err))
		}
		if loc.Legacy {
			return m.storeErrorf(ReasonUnverifiedObject, cat.Root(),
				"snapshot %s still resolves to the old-format store; the migration is incomplete, "+
					"so the original is left in place", hash)
		}
		if !loc.Protected {
			// Defence in depth: a migrated hash living in an UNPROTECTED
			// generation could be reclaimed by ordinary retention, silently
			// discarding history the migration promised to preserve.
			return m.storeErrorf(ReasonUnverifiedObject, loc.GitDir,
				"snapshot %s was migrated into an unprotected generation %s; refusing to remove the "+
					"original until the migrated history is protected", hash, loc.GenerationID)
		}
	}
	return nil
}

// removeLegacyDuplicate renames the original legacy store into the accounted
// deleting state and then removes it.
//
// The rename is what makes an interruption recoverable, and it happens only
// after the migration has been verified. If the removal then fails, the remnant
// stays — accounted, named, and finishable by `marshal snapshots cleanup` —
// rather than leaving an unaccounted directory behind.
func (m *Manager) removeLegacyDuplicate(ctx context.Context, workspace, path string) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	suffix, err := randomSuffix(stagingIDRandomLen)
	if err != nil {
		return false, "", storeError(ReasonInternal, err)
	}
	remnant := filepath.Join(m.root, formatLegacyDeletingName(workspace, suffix))
	if err := m.proveInsideRoot(remnant); err != nil {
		return false, "", err
	}
	if err := m.runRecoveryHook(m.hooks.Recovery.BeforeLegacyOriginalRenamed); err != nil {
		return false, "", err
	}
	if err := os.Rename(path, remnant); err != nil {
		return false, "", m.storeError(ReasonUnreadableFile, path,
			fmt.Errorf("rename the migrated original aside: %w", err))
	}
	if err := syncDir(m.root); err != nil {
		// The rename landed; the directory entry is not yet flushed. Reporting it
		// is right, but the remnant is recoverable either way.
		return false, remnant, m.storeError(ReasonUnreadableFile, m.root, err)
	}
	if err := m.runRecoveryHook(m.hooks.Recovery.AfterLegacyOriginalRenamed); err != nil {
		return false, remnant, err
	}
	if err := removeDirectoryNoFollow(remnant); err != nil {
		return false, remnant, nil
	}
	return true, "", nil
}

// ---------------------------------------------------------------------------
// The migrated-object source
// ---------------------------------------------------------------------------

// legacyObjectSource streams one object's content out of a legacy repository
// through a read-only `git cat-file`.
//
// It exists because the closure must be copied without ever holding a large blob
// in memory. The size is known up front (it came from `cat-file --batch-check`
// during the closure walk), which is what lets the object writer's exact-read
// protocol apply to a subprocess exactly as it applies to a file.
//
// The process runs in the manager's own process tree, so releasing store
// ownership drains it: a cancelled migration cannot leave a git process writing
// after the lock is handed on.
type legacyObjectSource struct {
	manager *Manager
	ctx     context.Context
	cancel  context.CancelFunc
	dir     string
	hash    string
	objType string
	size    int64

	tree    *processTree
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	stderr  *limitedBuffer
	tracked bool
	done    bool
}

// newLegacyObjectSource starts the read-only reader for one object.
func (m *Manager) newLegacyObjectSource(ctx context.Context, dir, objType, hash string, size int64) (*legacyObjectSource, error) {
	if !validObjectHash(hash) {
		return nil, storeErrorf(ReasonInvalidObjectHash, "object %q is not a hexadecimal object id", hash)
	}
	runCtx := ctx
	cancel := context.CancelFunc(func() {})
	if deadline := m.deadlineFor(ctx); deadline > 0 {
		runCtx, cancel = context.WithTimeout(ctx, deadline)
	}
	cmd := newGitCmd(runCtx, m.gitBinary, dir, "", true, "cat-file", objType, hash)
	tree, err := newProcessTree(cmd)
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, m.storeError(ReasonUnreadableFile, dir, err)
	}
	errBuf := &limitedBuffer{max: normalizeMaxOutput(m.maxOutputBytes)}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, m.storeError(ReasonUnreadableFile, dir, err)
	}
	if err := tree.afterStart(cmd.Process.Pid); err != nil {
		_ = tree.kill()
		_ = cmd.Wait()
		cancel()
		return nil, err
	}
	src := &legacyObjectSource{
		manager: m, ctx: runCtx, cancel: cancel, dir: dir, hash: hash, objType: objType,
		size: size, tree: tree, cmd: cmd, stdout: stdout, stderr: errBuf,
	}
	if m.trackTreeIfOwned(tree) {
		src.tracked = true
	}
	return src, nil
}

// Size is the content length the Git object header will declare.
func (s *legacyObjectSource) Size() int64 { return s.size }

// Description names the object for diagnostics.
func (s *legacyObjectSource) Description() string {
	return fmt.Sprintf("%s:%s %s", s.dir, s.objType, s.hash)
}

// ReadChunk reads the next content bytes.
func (s *legacyObjectSource) ReadChunk(p []byte) (int, error) { return s.stdout.Read(p) }

// Finalize waits for the reader and requires it to have succeeded. The object
// writer calls it after every declared byte has been read, so an interrupted
// read is reported rather than silently accepted.
func (s *legacyObjectSource) Finalize() error {
	if s.done {
		return nil
	}
	s.done = true
	// The writer has already probed for EOF, but draining makes Wait() safe even
	// when Finalize is reached through a different path.
	_, _ = io.Copy(io.Discard, s.stdout)
	waitErr := s.cmd.Wait()
	closeErr := s.stdout.Close()
	s.cancel()
	if s.tracked {
		s.manager.untrackTree(s.tree)
		s.tracked = false
	}
	if waitErr != nil {
		return s.manager.storeError(ReasonUnreadableFile, s.dir,
			fmt.Errorf("read object %s from the old store: %w%s", s.hash, waitErr,
				gitOutputSuffix(s.stderr.Bytes())))
	}
	// cmd.Wait already closes the StdoutPipe it created, so a second Close
	// returns os.ErrClosed. That is not a failure of the read; it is the reader
	// having finished, which is what reaching here means.
	if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		return s.manager.storeError(ReasonUnreadableFile, s.dir, closeErr)
	}
	return nil
}

// Close releases the reader without requiring success. It is idempotent, so the
// error paths can call it after Finalize.
func (s *legacyObjectSource) Close() error {
	if s == nil {
		return nil
	}
	if s.done {
		return nil
	}
	_ = s.tree.kill()
	_, _ = io.Copy(io.Discard, s.stdout)
	_ = s.cmd.Wait()
	_ = s.stdout.Close()
	if s.tracked {
		s.manager.untrackTree(s.tree)
		s.tracked = false
	}
	s.done = true
	s.cancel()
	return nil
}

// bytesWritten reports how many bytes the writer charged for this migration.
func (w *objectWriter) bytesWritten() int64 { return w.budget.Spent() }

// ---------------------------------------------------------------------------
// Reset
// ---------------------------------------------------------------------------

// ResetReport is the outcome of one reset.
type ResetReport struct {
	// Workspace is the workspace hash reset.
	Workspace string
	// Path was the store path before the reset.
	Path string
	// Bytes is the measured size of the removed store.
	Bytes int64
	// SnapshotHashes lists the rollback history that was discarded.
	SnapshotHashes []string
	// Branches lists the branch refs that were discarded.
	Branches []string
	// RemnantPath is where the store was renamed to when its removal did not
	// complete.
	RemnantPath string
	// Removed reports whether the store was fully removed.
	Removed bool
}

// ResetWorkspace discards one old-format shadow store, and nothing else.
//
// The confirmations are the point of the operation, so they come before any
// mutation and they cannot be bypassed:
//
//   - A CONTROLLING TERMINAL is required. Ordinary stdin cannot authorise this:
//     an agent-launched command, a pipeline, or a here-document all supply stdin
//     without a human, and `printf '<id>' | marshal snapshots reset ...` must not
//     be indistinguishable from a person answering.
//   - The offline prerequisite must be acknowledged, in the full wording.
//   - The FULL workspace ID must be typed at that terminal.
//   - There is NO --yes, no environment variable, and no non-interactive path.
//   - EOF, a refusal, a missing terminal, and a changed target are all entirely
//     non-destructive.
//
// What is NEVER touched: the project's working tree and the project's own `.git`.
// The operation's scope is exactly `<snapshots>/<workspace-hash>`, resolved from
// the validated identifier inside the manager's own root — a user-supplied
// filesystem path is never resolved for deletion.
func (m *Manager) ResetWorkspace(ctx context.Context, workspace string, opts RecoveryOptions) (*ResetReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	if !m.Owned() {
		return nil, storeErrorf(ReasonNotOwned,
			"reset requires exclusive ownership of the snapshot store")
	}
	report := &ResetReport{Workspace: workspace}

	path, err := m.containedLegacyStorePath(workspace)
	if err != nil {
		return nil, err
	}
	report.Path = path
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		// Nothing to remove at the shadow-store path. A remnant of an earlier
		// attempt is finished by cleanup, and is reported rather than silently
		// ignored.
		remnant, ok := m.legacyRemnantFor(workspace)
		if ok {
			return nil, storeErrorf(ReasonLegacyRecoveryRequired, remnant,
				"workspace %s has no store at %s, but an interrupted reset left the remnant %s; "+
					"run 'marshal snapshots cleanup' to finish it", workspace, path, remnant)
		}
		return nil, storeErrorf(ReasonLegacyRecoveryRequired,
			"workspace %s has no old-format store at %s, so there is nothing to reset", workspace, path)
	}

	legacy, err := m.inspectLegacy(ctx, workspace, path, info)
	if err != nil {
		return nil, err
	}
	report.Bytes = legacy.Bytes
	report.SnapshotHashes = append([]string(nil), legacy.SnapshotHashes...)
	report.Branches = legacy.Heads()

	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}

	// The target fingerprint is taken BEFORE the lock is released, and compared
	// again after it is retaken. An old Marshal instance writing during the
	// prompt would change the refs or the measured size, and that is exactly what
	// renewed confirmation is for.
	before, err := m.fingerprintLegacy(ctx, workspace)
	if err != nil {
		return nil, err
	}

	notice := resetNotice(workspace, path, legacy, limits)
	if err := m.promptWithReleasedLock(ctx, func() error {
		if err := requireOfflineAcknowledgement(opts.Confirmer, "reset of an old-format snapshot store", notice); err != nil {
			return err
		}
		return requireTypedIdentity(opts.Confirmer, "workspace ID", workspace, "reset")
	}); err != nil {
		return nil, err
	}
	// The target is revalidated AFTER ownership is retaken: a legacy writer
	// ignores the lock entirely, so a change during the prompt must be caught
	// here rather than assumed impossible.
	if err := m.revalidateTarget(ctx, before); err != nil {
		return nil, err
	}
	if err := m.refuseIfLiveWriterIn(path); err != nil {
		return nil, err
	}

	if err := m.runRecoveryHook(m.hooks.Recovery.BeforeLegacyResetRename); err != nil {
		return nil, err
	}
	suffix, err := randomSuffix(stagingIDRandomLen)
	if err != nil {
		return nil, storeError(ReasonInternal, err)
	}
	remnant := filepath.Join(m.root, formatLegacyDeletingName(workspace, suffix))
	if err := m.proveInsideRoot(remnant); err != nil {
		return nil, err
	}
	if err := os.Rename(path, remnant); err != nil {
		return nil, m.storeError(ReasonUnreadableFile, path,
			fmt.Errorf("rename the store into the accounted deleting state: %w", err))
	}
	if err := syncDir(m.root); err != nil {
		report.RemnantPath = remnant
		return report, m.storeError(ReasonUnreadableFile, m.root, err)
	}
	report.RemnantPath = remnant
	if err := m.runRecoveryHook(m.hooks.Recovery.BeforeLegacyResetRemove); err != nil {
		return report, err
	}
	if err := removeDirectoryNoFollow(remnant); err != nil {
		// The removal did not finish. The remnant is accounted state and cleanup
		// finishes it, so this is reported rather than treated as success.
		return report, nil
	}
	report.Removed = true
	report.RemnantPath = ""
	return report, nil
}

// DiscardReport is the outcome of discarding a protected generation.
type DiscardReport struct {
	// Workspace is the workspace hash.
	Workspace string
	// GenerationID is the generation whose protection and history were discarded.
	GenerationID string
	// Bytes is the measured size of the generation before removal.
	Bytes int64
	// Hashes lists the snapshot hashes that were discarded.
	Hashes []string
	// Removed reports whether the generation directory was removed.
	Removed bool
}

// DiscardProtectedGeneration removes a PROTECTED generation — a migrated
// legacy-origin archive, or any generation protected some other way — and it
// applies exactly the same confirmation rules a reset does:
//
//   - a controlling terminal,
//   - the offline prerequisite acknowledged in full,
//   - the FULL generation ID typed at that terminal,
//   - a fingerprint revalidated after the lock is retaken,
//   - no --yes and no non-interactive path.
//
// Migration deliberately makes protection persist, so discarding the only
// surviving copy of pre-v2 rollback history requires its own explicit act. This
// is that act, and it is the only place protection is lifted.
func (m *Manager) DiscardProtectedGeneration(ctx context.Context, workspace, generationID string, opts RecoveryOptions) (*DiscardReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	if !m.Owned() {
		return nil, storeErrorf(ReasonNotOwned,
			"discarding a protected generation requires exclusive ownership of the snapshot store")
	}
	if !validWorkspaceID(workspace) {
		return nil, storeErrorf(ReasonInvalidObjectHash,
			"workspace %q is not a 12-character hexadecimal workspace hash", workspace)
	}
	if !validGenerationID(generationID) {
		return nil, storeErrorf(ReasonInternal,
			"generation identifier %q is not a safe path element", generationID)
	}
	report := &DiscardReport{Workspace: workspace, GenerationID: generationID}
	cat, err := m.Catalog(workspace)
	if err != nil {
		return nil, err
	}
	man, err := cat.Load()
	if err != nil {
		return nil, err
	}
	if man == nil {
		return nil, cat.errf(ReasonInternal, "workspace %s has no manifest", workspace)
	}
	rec := man.Generation(generationID)
	if rec == nil {
		return nil, cat.errf(ReasonInternal, "generation %s is not listed in the manifest", generationID)
	}
	if rec.State == GenerationDeleting {
		return nil, cat.errf(ReasonInterruptedCapture,
			"generation %s is already being deleted; run 'marshal snapshots cleanup' to finish it", generationID)
	}
	if !rec.IsProtected() {
		return nil, cat.errf(ReasonInternal,
			"generation %s (%s origin) is not protected, so it is removed by ordinary retention rather than "+
				"by an explicit history-discard confirmation", generationID, rec.Origin)
	}
	hashes, err := m.generationRefs(ctx, cat, generationID)
	if err != nil {
		return nil, err
	}
	report.Hashes = hashes
	genDir, err := cat.GenerationDir(generationID)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(genDir); err == nil {
		if report.Bytes, err = m.measureTreeAllocated(genDir, info); err != nil {
			return nil, err
		}
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}

	fingerprintBefore := generationFingerprint(workspace, generationID, hashes, report.Bytes)
	notice := discardNotice(workspace, generationID, report, rec, limits)

	if err := m.promptWithReleasedLock(ctx, func() error {
		if err := requireOfflineAcknowledgement(opts.Confirmer, "discard of protected snapshot history", notice); err != nil {
			return err
		}
		return requireTypedIdentity(opts.Confirmer, "generation ID", generationID, "discard")
	}); err != nil {
		return nil, err
	}
	// Revalidate: the generation's refs and size must be what the operator
	// confirmed.
	after, err := m.generationRefs(ctx, cat, generationID)
	if err != nil {
		return nil, err
	}
	var afterBytes int64
	if info, err := os.Lstat(genDir); err == nil {
		if afterBytes, err = m.measureTreeAllocated(genDir, info); err != nil {
			return nil, err
		}
	}
	if generationFingerprint(workspace, generationID, after, afterBytes) != fingerprintBefore {
		return nil, cat.errf(ReasonLegacyRecoveryRequired,
			"generation %s changed between the confirmation and the operation; nothing was changed — "+
				"re-run to confirm the new state", generationID)
	}

	// The protection is lifted EXPLICITLY and durably before the removal, so the
	// removal goes through the same intent-first sequence every other generation
	// removal uses.
	man, err = cat.Load()
	if err != nil {
		return nil, err
	}
	rec = man.Generation(generationID)
	if rec == nil {
		return nil, cat.errf(ReasonInternal, "generation %s vanished from the manifest", generationID)
	}
	rec.Protected = false
	rec.ProtectionReleased = true
	if err := cat.Save(man); err != nil {
		return nil, err
	}
	if err := m.runRecoveryHook(m.hooks.Recovery.AfterProtectionReleased); err != nil {
		return nil, err
	}
	if _, err := cat.MarkDeleting(generationID); err != nil {
		return nil, err
	}
	if err := cat.RemoveGenerationDirectory(generationID); err != nil {
		return nil, err
	}
	if err := cat.ForgetGeneration(generationID); err != nil {
		return nil, err
	}
	report.Removed = true
	return report, nil
}

// generationRefs lists a generation's snapshot hashes, sorted.
func (m *Manager) generationRefs(ctx context.Context, cat *Catalog, generationID string) ([]string, error) {
	dir, err := cat.GenerationDir(generationID)
	if err != nil {
		return nil, err
	}
	if !repositoryLooksInitialised(dir) {
		return nil, nil
	}
	_, hashes, err := cat.refsInDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	sort.Strings(hashes)
	return hashes, nil
}

// generationFingerprint is a stable identity for one generation, used to detect
// a change between a confirmation and the act.
func generationFingerprint(workspace, generationID string, hashes []string, bytes int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "workspace=%s\ngeneration=%s\nbytes=%d\n", workspace, generationID, bytes)
	for _, hash := range hashes {
		fmt.Fprintf(h, "ref=%s\n", hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Operator-facing notices
// ---------------------------------------------------------------------------

// migrationNotice describes exactly what a migration will copy.
func migrationNotice(workspace, path string, legacy *LegacyInfo, limits Limits, allowance int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nAbout to MIGRATE old-format snapshot history into the new store.\n\n")
	fmt.Fprintf(&b, "Workspace:        %s\n", workspace)
	fmt.Fprintf(&b, "Workspace root:   unknown (the old layout records only the workspace hash)\n")
	fmt.Fprintf(&b, "Old store:        %s\n", path)
	fmt.Fprintf(&b, "Store size:       %s\n", formatBytes(legacy.Bytes))
	fmt.Fprintf(&b, "Snapshot refs:    %d\n", legacy.SnapshotRefs)
	fmt.Fprintf(&b, "Branch refs:      %d%s\n", len(legacy.Branches), branchSuffix(legacy))
	fmt.Fprintf(&b, "Objects to copy:  %d (%s of reachable history)\n", legacy.Objects, formatBytes(legacy.ParentsNeeded))
	fmt.Fprintf(&b, "Destination cost: %s\n", formatBytes(allowance))
	fmt.Fprintf(&b, "Budgets:          workspace %s, global %s\n",
		formatBytes(limits.WorkspaceMaxBytes), formatBytes(limits.GlobalMaxBytes))
	fmt.Fprintf(&b, "\nEvery snapshot hash above stays rollback-able. The copy is sealed and PROTECTED, so no\n")
	fmt.Fprintf(&b, "budget pressure will ever reclaim it. The original store is removed only after every\n")
	fmt.Fprintf(&b, "hash is verified to resolve in the new store.\n")
	return b.String()
}

// branchSuffix annotates the branch count with the retention warning.
func branchSuffix(legacy *LegacyInfo) string {
	if legacy == nil || len(legacy.Branches) == 0 {
		return ""
	}
	return " (branches are retention roots; their tips are published as snapshots too)"
}

// resetNotice describes exactly what a reset will destroy.
func resetNotice(workspace, path string, legacy *LegacyInfo, limits Limits) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nAbout to DELETE an old-format snapshot store.\n\n")
	fmt.Fprintf(&b, "Workspace:        %s\n", workspace)
	fmt.Fprintf(&b, "Exact target:     %s\n", path)
	fmt.Fprintf(&b, "Bytes to free:    %s\n", formatBytes(legacy.Bytes))
	fmt.Fprintf(&b, "Snapshots lost:   %d (%s)\n", legacy.SnapshotRefs, hashList(legacy.SnapshotHashes))
	fmt.Fprintf(&b, "Branches lost:    %d (%s)\n", len(legacy.Branches), strings.Join(legacy.Heads(), ", "))
	fmt.Fprintf(&b, "Budgets:          workspace %s, global %s\n",
		formatBytes(limits.WorkspaceMaxBytes), formatBytes(limits.GlobalMaxBytes))
	fmt.Fprintf(&b, "\nNOT touched: your project's files, and the project's own .git directory.\n")
	fmt.Fprintf(&b, "This operation removes ONLY the snapshot shadow store above. Every rollback point\n")
	fmt.Fprintf(&b, "listed here is destroyed, and it cannot be recovered afterwards.\n")
	return b.String()
}

// discardNotice describes exactly what a history discard will destroy.
func discardNotice(workspace, generationID string, report *DiscardReport, rec *GenerationRecord, limits Limits) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nAbout to DISCARD protected snapshot history.\n\n")
	fmt.Fprintf(&b, "Workspace:        %s\n", workspace)
	fmt.Fprintf(&b, "Generation:       %s\n", generationID)
	fmt.Fprintf(&b, "Origin:           %s (protected, so nothing reclaims it automatically)\n", rec.Origin)
	fmt.Fprintf(&b, "Bytes to free:    %s\n", formatBytes(report.Bytes))
	fmt.Fprintf(&b, "Snapshots lost:   %d (%s)\n", len(report.Hashes), hashList(report.Hashes))
	fmt.Fprintf(&b, "Budgets:          workspace %s, global %s\n",
		formatBytes(limits.WorkspaceMaxBytes), formatBytes(limits.GlobalMaxBytes))
	fmt.Fprintf(&b, "\nNOT touched: your project's files, and the project's own .git directory.\n")
	fmt.Fprintf(&b, "This is the only operation that lifts a migrated generation's protection.\n")
	return b.String()
}

// terminalWriter returns a usable writer for a Confirmer, or a discard sink when
// no terminal is available (in which case the confirmation will refuse anyway,
// and there is no point failing on the notice).
func terminalWriter(c Confirmer) io.Writer {
	if c == nil || !c.IsTerminal() {
		return io.Discard
	}
	return c
}

// hashList renders a bounded list of hashes for a notice.
func hashList(hashes []string) string {
	const max = 5
	if len(hashes) == 0 {
		return "none"
	}
	out := append([]string(nil), hashes...)
	if len(out) > max {
		out = append(out[:max], fmt.Sprintf("… and %d more", len(hashes)-max))
	}
	return shortHashes(out)
}

// shortHashes abbreviates hashes for readability.
func shortHashes(hashes []string) string {
	out := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if len(h) > 12 {
			h = h[:12]
		}
		out = append(out, h)
	}
	return strings.Join(out, ", ")
}

// formatBytes renders a byte count the way the settings UI does.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(b)
	for _, u := range units {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PB", v)
}
