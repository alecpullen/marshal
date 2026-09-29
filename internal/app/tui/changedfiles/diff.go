package changedfiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxPreviewBytes bounds an untracked file preview, and bounds any patch
// ReadDiff returns. A patch longer than this is cut and flagged with
// Truncated — never silently.
const MaxPreviewBytes = 1 << 20

// Diff is one file's patch.
type Diff struct {
	Path      string
	Patch     string
	Kind      FileKind
	Binary    bool
	Deleted   bool
	Renamed   bool
	OldPath   string
	Untracked bool
	// Truncated reports that Patch is a bounded prefix of the real diff.
	Truncated      bool
	Added, Removed int
	CountsKnown    bool
	Status         Status
	Err            error
}

// ReadDiff reads one file's patch out of snap.
//
// It never panics and always returns a Diff; failures are reported through
// Status and Err, so a caller can tell "this file has no patch" from "the
// read failed" — the distinction the old implementation erased everywhere.
//
// The patch is fetched with a literal pathspec and terminating options:
//
//	git -C <root> diff --no-ext-diff --no-textconv --no-color --unified=3 <baseOID> -- <path>
//
// The path is passed as an ARGUMENT, never interpolated into a shell string,
// and `--` terminates option parsing so a path beginning with `-` is a path
// and not a flag. --no-ext-diff and --no-textconv keep a repository's
// configured diff.external / textconv helpers out of a read-only inspection:
// those helpers are arbitrary programs, and running one to render a diff the
// user only asked to look at would be a surprising side effect.
func ReadDiff(ctx context.Context, snap Snapshot, path string) Diff {
	d := Diff{Path: path, Status: StatusOK}

	if err := ctx.Err(); err != nil {
		d.Status = classify(ctx, err)
		d.Err = err
		return d
	}
	if snap.Status != StatusOK {
		// The snapshot failed, so there is nothing to diff against. Propagate
		// the snapshot's own status rather than inventing a fresh failure.
		d.Status = snap.Status
		d.Err = snap.Err
		return d
	}
	if path == "" {
		d.Status = StatusFailed
		d.Err = errors.New("changedfiles: empty path")
		return d
	}

	// The snapshot is the authority on what happened to the path: it is what
	// the caller is looking at, and re-deriving the classification here would
	// let the list and the detail disagree.
	if f, ok := snap.file(path); ok {
		d.Kind = f.Kind
		d.OldPath = f.OldPath
		d.Added, d.Removed, d.CountsKnown = f.Added, f.Removed, f.CountsKnown
		d.Binary = f.Kind == FileBinary
		d.Deleted = f.Kind == FileDeleted
		d.Renamed = f.Kind == FileRenamed
		d.Untracked = f.Kind == FileUntracked
	}

	if d.Untracked {
		return readUntrackedPreview(snap, d)
	}
	return readTrackedPatch(ctx, snap, d)
}

// file returns the snapshot's entry for path.
func (s Snapshot) file(path string) (File, bool) {
	for _, f := range s.Files {
		if f.Path == path {
			return f, true
		}
	}
	return File{}, false
}

// readTrackedPatch fetches a tracked path's patch from git.
func readTrackedPatch(ctx context.Context, snap Snapshot, d Diff) Diff {
	// A rename needs BOTH paths in the pathspec. Naming only the new path
	// makes git report the file as a brand-new addition (it cannot pair the
	// sides), which would tell the user the file was created rather than
	// moved. Both paths are still literal arguments and `--` still terminates
	// options.
	paths := []string{d.Path}
	if d.Renamed && d.OldPath != "" {
		paths = []string{d.OldPath, d.Path}
	}

	out, err := gitRun(ctx, trackedDiffArgv(snap.Root, snap.BaseOID, paths...))
	if err != nil {
		d.Status = classify(ctx, err)
		d.Err = err
		return d
	}

	d.Patch, d.Truncated = boundPatch(string(out))
	return d
}

// trackedDiffArgv builds the argv for one tracked path's patch. It is a
// separate function so the tests can assert on the exact argument list the
// implementation uses, rather than on a copy of it.
func trackedDiffArgv(root, baseOID string, paths ...string) []string {
	argv := []string{
		"git", "-C", root, "diff",
		"--no-ext-diff", "--no-textconv", "--no-color", "--unified=3",
		baseOID, "--",
	}
	return append(argv, paths...)
}

// readUntrackedPreview renders an untracked path as an addition without
// staging it.
//
// The patch is composed here rather than fetched with `git diff --no-index`
// for three reasons: --no-index exits 1 on a difference (indistinguishable
// from a failure without special-casing), it quotes awkward paths in its
// headers, and it would read the whole file with no bound. Composing the
// patch lets the read be bounded and lets a symlink be reported as metadata
// instead of followed.
func readUntrackedPreview(snap Snapshot, d Diff) Diff {
	abs, err := resolveInRoot(snap.Root, d.Path)
	if err != nil {
		d.Status = StatusFailed
		d.Err = err
		return d
	}

	// Lstat, never Stat: a symlink must be reported as a symlink. Following it
	// would read content the user never asked to inspect, and a link pointing
	// outside the repository would leak a file from outside the tree.
	info, err := os.Lstat(abs)
	if err != nil {
		d.Status = StatusFailed
		d.Err = err
		return d
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, lerr := os.Readlink(abs)
		if lerr != nil {
			d.Status = StatusFailed
			d.Err = lerr
			return d
		}
		// Metadata only. The link's target is a string; its content is never
		// read, so a link to /etc/passwd or to a path outside the repository
		// contributes nothing but the target text.
		d.Patch = fmt.Sprintf("symlink %s -> %s\n", d.Path, target)
		d.CountsKnown = false
		return d
	case !info.Mode().IsRegular():
		d.Patch = fmt.Sprintf("%s (%s)\n", d.Path, info.Mode().Type())
		d.CountsKnown = false
		return d
	}

	// Read one byte past the cap so a file exactly at the cap is not reported
	// as truncated.
	f, err := os.Open(abs)
	if err != nil {
		d.Status = StatusFailed
		d.Err = err
		return d
	}
	defer f.Close()

	buf := make([]byte, MaxPreviewBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		d.Status = StatusFailed
		d.Err = err
		return d
	}
	data := buf[:n]
	overCap := n > MaxPreviewBytes
	if overCap {
		data = data[:MaxPreviewBytes]
	}

	if bytes.IndexByte(data, 0) >= 0 {
		// A NUL byte means the file is not text. Report metadata, never the
		// bytes: a preview of a binary blob is noise at best and a terminal
		// escape injection at worst.
		d.Binary = true
		d.Patch = fmt.Sprintf("Binary file %s (%d bytes)\n", d.Path, info.Size())
		d.CountsKnown = false
		return d
	}

	lines := splitPreviewLines(data)
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", d.Path, d.Path)
	b.WriteString("new file mode 100644\n")
	b.WriteString("--- /dev/null\n")
	fmt.Fprintf(&b, "+++ b/%s\n", d.Path)
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, ln := range lines {
		b.WriteString("+")
		b.WriteString(ln)
		b.WriteString("\n")
	}

	d.Patch, d.Truncated = boundPatch(b.String())
	d.Truncated = d.Truncated || overCap
	// The line count is real — it is the number of lines in the preview — but
	// git never reported it, so CountsKnown stays false. That is the whole
	// point of the flag: the old code wrote `Added: 1` for an untracked file,
	// inventing a number git had never produced. The plan bans it: "Report
	// unknown counts explicitly rather than inventing Added: 1 for untracked
	// files."
	d.Added = len(lines)
	d.Removed = 0
	d.CountsKnown = false
	return d
}

// splitPreviewLines splits preview content into lines without a trailing
// empty element for a final newline, so the reported addition count matches
// what a reader would count.
func splitPreviewLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// boundPatch cuts patch to MaxPreviewBytes at a rune boundary and reports
// whether it had to. Truncation is always flagged: a caller must never be
// able to mistake a prefix for the whole patch.
func boundPatch(patch string) (string, bool) {
	if len(patch) <= MaxPreviewBytes {
		return patch, false
	}
	return truncateUTF8(patch, MaxPreviewBytes), true
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune, so the
// result is always valid UTF-8.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// resolveInRoot joins rel onto root and refuses a path that escapes it. A
// snapshot's paths come from git and are repository-relative, but a caller
// can hand ReadDiff any string, and joining an unchecked "../.." onto the
// root would read a file outside the repository.
func resolveInRoot(root, rel string) (string, error) {
	if root == "" {
		return "", errors.New("changedfiles: snapshot has no repository root")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || filepath.IsAbs(clean) ||
		clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("changedfiles: path %q escapes the repository root", rel)
	}
	return filepath.Join(root, clean), nil
}
