// Package changedfiles reads the working tree's diff against a base ref for
// the side panel's changed-files section and for the conversation
// inspector's Changes view.
//
// The package is telemetry: it must never break a turn or block a render.
// Unlike the implementation it replaces, it does not erase the difference
// between "nothing changed" and "the read failed" — every read returns a
// Snapshot whose Status says which of the two happened, and every failure
// carries the error that caused it.
package changedfiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status classifies the outcome of a snapshot or diff read.
type Status string

const (
	StatusOK          Status = "ok"
	StatusMissingBase Status = "missing-base"
	StatusNotARepo    Status = "not-a-repo"
	StatusTimeout     Status = "timeout"
	StatusCancelled   Status = "cancelled"
	StatusFailed      Status = "failed"
)

// FileKind classifies what happened to one path.
type FileKind int

const (
	FileModified FileKind = iota
	FileAdded
	FileDeleted
	FileRenamed
	FileUntracked
	FileBinary
)

// String renders the kind for diagnostics and test failures.
func (k FileKind) String() string {
	switch k {
	case FileModified:
		return "modified"
	case FileAdded:
		return "added"
	case FileDeleted:
		return "deleted"
	case FileRenamed:
		return "renamed"
	case FileUntracked:
		return "untracked"
	case FileBinary:
		return "binary"
	default:
		return "unknown"
	}
}

// File is one changed path.
type File struct {
	Path string
	// OldPath is set for renames and copies.
	OldPath string
	Kind    FileKind
	// Status is git's own status letter ('A','M','D','R','C','T'), kept
	// alongside Kind because Kind is a lossy classification and the letter is
	// what git actually said.
	Status  rune
	Added   int
	Removed int
	// CountsKnown is false when git did not report counts (a binary file
	// reports "-", and an untracked file has no diff at all). A false value
	// means "unknown", NOT zero — which is exactly the distinction the old
	// code erased by writing Added: 1.
	CountsKnown bool
}

// Snapshot is one reading of the working tree against a base ref.
type Snapshot struct {
	// BaseRef is the ref as requested.
	BaseRef string
	// BaseOID is the single resolved commit the comparison used, so a caller
	// can label what it compared against instead of showing a moving ref.
	BaseOID string
	// Root is the repository's working-tree root, empty when not a repo.
	Root       string
	Files      []File
	CapturedAt time.Time
	Status     Status
	Err        error
}

// IsEmpty reports whether the read succeeded and found nothing.
func (s Snapshot) IsEmpty() bool { return s.Status == StatusOK && len(s.Files) == 0 }

// Clean reports whether the tree is clean — a successful read that found
// nothing. It is deliberately distinct from a failure: the old Read returned
// nil for both, so a caller could not tell "no changes" from "git blew up".
func (s Snapshot) Clean() bool { return s.Status == StatusOK && len(s.Files) == 0 }

// argvHook, when non-nil, receives the exact argv of every git command this
// package executes. It is a test seam: the fixture tests assert on the real
// invocation (option ordering, `--` placement, the literal pathspec) while
// still running real git, rather than mocking git's output.
var (
	argvMu   sync.Mutex
	argvHook func(argv []string)
)

// recordArgv reports one command's argv to the test seam, if installed.
func recordArgv(argv []string) {
	argvMu.Lock()
	h := argvHook
	argvMu.Unlock()
	if h != nil {
		h(argv)
	}
}

// gitError is a failed git invocation, carrying the argv and git's own
// stderr so a caller can show why the read failed rather than a bare exit
// status.
type gitError struct {
	argv   []string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	cmd := strings.Join(e.argv, " ")
	if e.stderr == "" {
		return fmt.Sprintf("%s: %v", cmd, e.err)
	}
	return fmt.Sprintf("%s: %v: %s", cmd, e.err, e.stderr)
}

func (e *gitError) Unwrap() error { return e.err }

// gitRun runs one git command and returns its stdout.
func gitRun(ctx context.Context, argv []string) ([]byte, error) {
	recordArgv(argv)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &gitError{argv: argv, stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return stdout.Bytes(), nil
}

// classify maps a command failure onto a Status. The context is checked
// first: exec.CommandContext kills the child when the context ends, so a
// deadline or a cancel surfaces as an ordinary command failure and would
// otherwise be misreported as StatusFailed.
func classify(ctx context.Context, err error) Status {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return StatusTimeout
	case context.Canceled:
		return StatusCancelled
	}
	if err == nil {
		return StatusOK
	}
	return StatusFailed
}

// ReadSnapshot reads the working tree against baseRef. It never panics and
// always returns a Snapshot; failures are reported through Status and Err.
//
// The read is three passes against ONE resolved commit:
//
//	git diff --numstat -z -M <oid> --      counts
//	git diff --name-status -z -M <oid> --  status letters, rename pairs
//	git ls-files -z --others --exclude-standard --  untracked paths
//
// There is deliberately no `--cached` pass. The old implementation merged one
// in and let it overwrite the worktree counts, so a file whose index and
// worktree differed was reported with the index's numbers instead of what is
// actually on disk.
func ReadSnapshot(ctx context.Context, workingDir, baseRef string) Snapshot {
	snap := Snapshot{BaseRef: baseRef, CapturedAt: time.Now()}

	if err := ctx.Err(); err != nil {
		snap.Status = classify(ctx, err)
		snap.Err = err
		return snap
	}
	if workingDir == "" {
		snap.Status = StatusFailed
		snap.Err = errors.New("changedfiles: empty working directory")
		return snap
	}
	if baseRef == "" {
		snap.Status = StatusMissingBase
		snap.Err = errors.New("changedfiles: empty base ref")
		return snap
	}

	// One repository root, resolved once. Everything below is relative to it,
	// so a linked worktree reports the worktree's own root and never the main
	// checkout's.
	rootOut, err := gitRun(ctx, []string{"git", "-C", workingDir, "rev-parse", "--show-toplevel"})
	if err != nil {
		snap.Status = classify(ctx, err)
		if snap.Status == StatusFailed {
			snap.Status = StatusNotARepo
		}
		snap.Err = err
		return snap
	}
	root := strings.TrimSuffix(string(rootOut), "\n")
	if root == "" {
		snap.Status = StatusNotARepo
		snap.Err = errors.New("changedfiles: git reported no working-tree root")
		return snap
	}
	snap.Root = root

	// One resolved commit. Recording the OID rather than the ref means a
	// caller can label what it compared against instead of showing a ref that
	// may have moved since the read.
	oidOut, err := gitRun(ctx, []string{
		"git", "-C", root, "rev-parse", "--verify", "--end-of-options", baseRef + "^{commit}",
	})
	if err != nil {
		snap.Status = classify(ctx, err)
		if snap.Status == StatusFailed {
			snap.Status = StatusMissingBase
		}
		snap.Err = err
		return snap
	}
	snap.BaseOID = strings.TrimSpace(string(oidOut))
	if snap.BaseOID == "" {
		snap.Status = StatusMissingBase
		snap.Err = fmt.Errorf("changedfiles: base ref %q did not resolve to a commit", baseRef)
		return snap
	}

	// -M is explicit rather than relying on the user's diff.renames: a
	// telemetry read must classify a rename the same way on every machine.
	numstatOut, err := gitRun(ctx, []string{
		"git", "-C", root, "diff", "--numstat", "-z", "-M",
		"--no-ext-diff", "--no-textconv", "--no-color",
		snap.BaseOID, "--",
	})
	if err != nil {
		snap.Status = classify(ctx, err)
		snap.Err = err
		return snap
	}

	nameStatusOut, err := gitRun(ctx, []string{
		"git", "-C", root, "diff", "--name-status", "-z", "-M",
		"--no-ext-diff", "--no-textconv", "--no-color",
		snap.BaseOID, "--",
	})
	if err != nil {
		snap.Status = classify(ctx, err)
		snap.Err = err
		return snap
	}

	// Untracked-and-unstaged new files. None of the diff passes above report
	// these (they only see tracked content), so list them explicitly. Respect
	// .gitignore via --exclude-standard so ignored files never surface.
	untrackedOut, err := gitRun(ctx, []string{
		"git", "-C", root, "ls-files", "-z", "--others", "--exclude-standard", "--",
	})
	if err != nil {
		snap.Status = classify(ctx, err)
		snap.Err = err
		return snap
	}

	snap.Files = mergeFiles(
		parseNameStatusZ(nameStatusOut),
		parseNumstatZ(numstatOut),
		parseLsFilesZ(untrackedOut),
	)
	snap.Status = StatusOK
	return snap
}

// mergeFiles joins the three passes into one ordered list. name-status is
// authoritative for the status letter and for rename/copy pairs; numstat is
// authoritative for the counts; ls-files supplies the untracked paths that no
// diff pass can see.
func mergeFiles(status, counts []File, untracked []string) []File {
	byPath := make(map[string]File, len(status)+len(counts)+len(untracked))
	order := make([]string, 0, len(status)+len(counts)+len(untracked))

	for _, f := range status {
		if _, ok := byPath[f.Path]; !ok {
			order = append(order, f.Path)
		}
		byPath[f.Path] = f
	}

	for _, c := range counts {
		f, known := byPath[c.Path]
		if !known {
			order = append(order, c.Path)
			f = File{Path: c.Path, OldPath: c.OldPath}
		}
		f.Added, f.Removed, f.CountsKnown = c.Added, c.Removed, c.CountsKnown
		if c.OldPath != "" {
			f.OldPath = c.OldPath
		}
		switch {
		case !c.CountsKnown:
			// git reported "-" for both counts: the blob is binary.
			f.Kind = FileBinary
		case !known:
			// No name-status entry (should not happen, but do not invent a
			// letter): fall back to what the counts imply.
			f.Kind = kindFromCounts(c)
			f.Status = statusLetterFromCounts(c)
		}
		byPath[c.Path] = f
	}

	for _, p := range untracked {
		if _, ok := byPath[p]; ok {
			continue
		}
		order = append(order, p)
		byPath[p] = File{
			Path: p,
			Kind: FileUntracked,
			// git's diff passes never mention an untracked path, so there is
			// no status letter to report. 'A' is what the rail has always
			// shown for a new file and it stays truthful: the path is an
			// addition to the tree.
			Status: 'A',
			// CountsKnown stays false. The old implementation wrote
			// `Added: 1` here, inventing a line count git never reported.
			// The plan bans that: "Report unknown counts explicitly rather
			// than inventing Added: 1 for untracked files."
			CountsKnown: false,
		}
	}

	sort.Strings(order)
	out := make([]File, 0, len(order))
	for _, p := range order {
		out = append(out, byPath[p])
	}
	return out
}

// kindFromCounts guesses a kind from numstat alone, for the case where
// name-status produced no entry for the path.
func kindFromCounts(c File) FileKind {
	switch {
	case c.Added > 0 && c.Removed == 0:
		return FileAdded
	case c.Added == 0 && c.Removed > 0:
		return FileDeleted
	default:
		return FileModified
	}
}

// statusLetterFromCounts is the letter matching kindFromCounts.
func statusLetterFromCounts(c File) rune {
	switch kindFromCounts(c) {
	case FileAdded:
		return 'A'
	case FileDeleted:
		return 'D'
	default:
		return 'M'
	}
}

// kindFromStatus maps git's status letter onto a FileKind. 'T' (type change),
// 'U' (unmerged), and anything git adds later all read as "the path changed".
func kindFromStatus(r rune) FileKind {
	switch r {
	case 'A':
		return FileAdded
	case 'D':
		return FileDeleted
	case 'R', 'C':
		return FileRenamed
	default:
		return FileModified
	}
}

// statusLetter takes the leading byte of a name-status status field ("R100",
// "M", "A") as the letter.
func statusLetter(s string) rune {
	if s == "" {
		return 0
	}
	return rune(s[0])
}

// fieldUpTo returns the bytes from start up to (not including) the first
// occurrence of sep, plus the index just past sep. It reports false when sep
// does not occur, which is how a truncated record ends the parse.
func fieldUpTo(b []byte, start int, sep byte) (string, int, bool) {
	if start < 0 || start > len(b) {
		return "", start, false
	}
	idx := bytes.IndexByte(b[start:], sep)
	if idx < 0 {
		return "", start, false
	}
	return string(b[start : start+idx]), start + idx + 1, true
}

// parseNumstatZ parses `git diff --numstat -z` output.
//
// The -z format is byte-oriented and NUL-terminated, which is the whole point:
// a path may contain a space, a tab, or a newline, and every newline- or
// whitespace-splitting parser mangles it. The old code used strings.Fields and
// newline splitting; this parses BYTES. A record is
//
//	<added> TAB <removed> TAB <path> NUL
//
// and, for a rename or copy, the path field is empty and two more
// NUL-terminated fields follow:
//
//	<added> TAB <removed> TAB NUL <old> NUL <new> NUL
//
// A binary file reports "-" for both counts, recorded as CountsKnown == false
// rather than as zero.
func parseNumstatZ(b []byte) []File {
	var files []File
	for i := 0; i < len(b); {
		added, next, ok := fieldUpTo(b, i, '\t')
		if !ok {
			break
		}
		removed, next2, ok := fieldUpTo(b, next, '\t')
		if !ok {
			break
		}
		path, next3, ok := fieldUpTo(b, next2, 0)
		if !ok {
			break
		}
		i = next3

		f := File{}
		if path == "" {
			old, n4, ok := fieldUpTo(b, i, 0)
			if !ok {
				break
			}
			newPath, n5, ok := fieldUpTo(b, n4, 0)
			if !ok {
				break
			}
			f.OldPath, f.Path, i = old, newPath, n5
		} else {
			f.Path = path
		}

		if added == "-" || removed == "-" {
			f.CountsKnown = false
			f.Kind = FileBinary
		} else {
			a, errA := strconv.Atoi(added)
			r, errR := strconv.Atoi(removed)
			if errA != nil || errR != nil {
				continue
			}
			f.Added, f.Removed, f.CountsKnown = a, r, true
		}
		files = append(files, f)
	}
	return files
}

// parseNameStatusZ parses `git diff --name-status -z` output:
//
//	<status> NUL <path> NUL
//	<status> NUL <old> NUL <new> NUL   (rename/copy)
//
// Like parseNumstatZ it is byte-exact, so a path containing a tab or a newline
// survives.
func parseNameStatusZ(b []byte) []File {
	var files []File
	for i := 0; i < len(b); {
		status, next, ok := fieldUpTo(b, i, 0)
		if !ok {
			break
		}
		first, next2, ok := fieldUpTo(b, next, 0)
		if !ok {
			break
		}
		i = next2

		f := File{Status: statusLetter(status)}
		if len(status) > 0 && (status[0] == 'R' || status[0] == 'C') {
			second, next3, ok := fieldUpTo(b, i, 0)
			if !ok {
				break
			}
			f.OldPath, f.Path, i = first, second, next3
		} else {
			f.Path = first
		}
		f.Kind = kindFromStatus(f.Status)
		files = append(files, f)
	}
	return files
}

// parseLsFilesZ parses `git ls-files -z` output: a NUL-separated list of
// paths. A path cannot contain NUL, so splitting on NUL is exact.
func parseLsFilesZ(b []byte) []string {
	var out []string
	for _, rec := range bytes.Split(b, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		out = append(out, string(rec))
	}
	return out
}
