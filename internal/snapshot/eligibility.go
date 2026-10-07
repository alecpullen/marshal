package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file answers exactly one question: which workspace paths may a capture
// select? It answers it with Git's own ignore machinery rather than by
// approximating it.
//
// The approximate replacement this file exists for read only the root
// .gitignore, trimmed every line (silently rewriting patterns that legitimately
// begin with whitespace), skipped comment lines a user may have meant literally,
// and reimplemented glob matching. It also swallowed traversal errors, so a
// file it could not read was treated as "not excluded" — which is how
// multi-gigabyte files entered history despite a 2 MB per-file cap.
//
// Everything here fails closed. A listing that cannot be trusted is an error,
// never a shorter list.

// Selection bounds for one capture. They are deliberately expressed as absolute
// numbers rather than as products of one another so a caller can shrink any one
// of them in a test without changing the others.
const (
	// defaultCaptureMaxEntries bounds how many entries one capture may select.
	defaultCaptureMaxEntries = 100_000
	// defaultCaptureMaxPathBytes bounds the encoded byte length of one path.
	defaultCaptureMaxPathBytes = 4096
	// defaultCaptureMaxTotalPathBytes bounds the sum of every encoded path in
	// one capture, which is what keeps a tree of many long paths from turning
	// into an unbounded tree payload.
	defaultCaptureMaxTotalPathBytes int64 = 16 << 20
)

// captureBounds carries the per-capture selection bounds. A zero field means
// "use the default"; it does not disable the bound.
type captureBounds struct {
	// MaxEntries is the most entries one capture may select. Exceeding it
	// aborts the capture visibly. A statically oversized or otherwise skipped
	// candidate does not count: the bound is on what the snapshot would carry.
	MaxEntries int
	// MaxPathBytes is the most encoded bytes one path may occupy. It is checked
	// against every candidate, not only the selected ones, because it also
	// bounds what the listing output can contain.
	MaxPathBytes int
	// MaxTotalPathBytes is the most encoded path bytes one capture may consider
	// in total.
	MaxTotalPathBytes int64
}

// defaultCaptureBounds returns the production selection bounds.
func defaultCaptureBounds() captureBounds {
	return captureBounds{
		MaxEntries:        defaultCaptureMaxEntries,
		MaxPathBytes:      defaultCaptureMaxPathBytes,
		MaxTotalPathBytes: defaultCaptureMaxTotalPathBytes,
	}
}

// normalized fills unset fields with their defaults and rejects nonsense.
// A negative bound is refused rather than treated as unlimited: a bound that
// silently disappears is how unbounded work gets admitted.
func (b captureBounds) normalized() (captureBounds, error) {
	if b.MaxEntries < 0 {
		return captureBounds{}, storeErrorf(ReasonInvalidLimits, "capture entry bound must not be negative: %d", b.MaxEntries)
	}
	if b.MaxPathBytes < 0 {
		return captureBounds{}, storeErrorf(ReasonInvalidLimits, "capture path byte bound must not be negative: %d", b.MaxPathBytes)
	}
	if b.MaxTotalPathBytes < 0 {
		return captureBounds{}, storeErrorf(ReasonInvalidLimits, "capture total path byte bound must not be negative: %d", b.MaxTotalPathBytes)
	}
	if b.MaxEntries == 0 {
		b.MaxEntries = defaultCaptureMaxEntries
	}
	if b.MaxPathBytes == 0 {
		b.MaxPathBytes = defaultCaptureMaxPathBytes
	}
	if b.MaxTotalPathBytes == 0 {
		b.MaxTotalPathBytes = defaultCaptureMaxTotalPathBytes
	}
	return b, nil
}

// capturedPathKind is the type of a selected path. Only these two types are
// captured; anything else aborts the capture.
type capturedPathKind int

const (
	// capturedRegular is a regular file.
	capturedRegular capturedPathKind = iota
	// capturedSymlink is a symbolic link. Its target text is the blob content
	// and the link is never followed.
	capturedSymlink
)

func (k capturedPathKind) String() string {
	switch k {
	case capturedRegular:
		return "regular file"
	case capturedSymlink:
		return "symlink"
	}
	return "unknown"
}

// eligibleEntry is one path the capture selected, as recorded in the snapshot's
// tree. Path is workspace-relative and slash-separated, which is the form a Git
// tree entry is named in on every platform.
type eligibleEntry struct {
	// Path is the slash-separated workspace-relative path.
	Path string
	// Kind is the entry type.
	Kind capturedPathKind
	// Mode is the mode observed when the path was classified. It is advisory:
	// the mode recorded in the tree is the one observed while the content is
	// read, so a mode change between classification and reading cannot produce
	// a tree entry that disagrees with the bytes.
	Mode os.FileMode
	// Size is the entry's initial length: the file's size, or a symlink's
	// target text length. It is advisory for the same reason.
	Size int64
}

// slashPath is the workspace-relative slash-separated form of a path, which is
// the only form a Git tree entry may be named in.
//
// The conversion happens with the platform separator only. Git's own listing
// output is already slash-separated, and a workspace path is converted once so
// the tree payload never depends on the separator.
func slashPath(rel string) string {
	if filepath.Separator == '/' {
		return rel
	}
	return strings.ReplaceAll(rel, string(filepath.Separator), "/")
}

// escapeGitignorePattern escapes a slash-separated relative path so that a
// gitignore rule matches it literally. The escape is character-by-character
// rather than "trim the dangerous bits", because a trimmed pattern matches
// something other than the path it names — the failure mode this file exists to
// remove.
func escapeGitignorePattern(rel string) string {
	var b strings.Builder
	b.Grow(len(rel))
	for i := 0; i < len(rel); i++ {
		c := rel[i]
		switch c {
		case '\\', '*', '?', '[', ']', ' ', '\t':
			// A trailing space or tab is ignored by git unless escaped; every
			// one is escaped so the rule cannot depend on position.
			b.WriteByte('\\')
			b.WriteByte(c)
		case '#', '!':
			// Only special in the first column, but escaping everywhere is
			// still a literal match.
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// storeExclusionRule returns the gitignore rule that keeps the managed store
// out of its own snapshot, or ("", nil) when the store is not inside the
// workspace.
//
// This is not a nicety. When the workspace is the user's home directory the
// managed store lives inside it, and a capture that selects the store
// re-captures snapshot objects into themselves — each capture doubling what the
// next one has to copy.
func (m *Manager) storeExclusionRule(workTree string) (string, error) {
	rel, err := filepath.Rel(workTree, m.root)
	if err != nil {
		// A store on another volume cannot be inside the workspace at all, so
		// there is nothing to exclude.
		return "", nil
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		// The workspace IS the store directory. Capturing it is meaningless;
		// excluding the whole tree makes the capture empty rather than
		// recursive.
		return "/", nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", nil
	}
	return "/" + escapeGitignorePattern(slashPath(rel)) + "/", nil
}

// eligibilityScratch is the read-only Git scaffolding an eligibility listing
// needs. It lives OUTSIDE the store, because nothing an eligibility listing
// needs may be written under the managed root: a store that must be usable on
// a read-only-by-policy volume, and an accounting that must not be perturbed by
// scratch state, both depend on that.
type eligibilityScratch struct {
	dir       string
	gitDir    string
	indexPath string
}

// remove discards the scratch directory. It is best-effort because the listing
// it supported has already returned, and a leftover temp directory is not a
// store artifact: it is outside the managed root and the next capture creates a
// fresh one.
func (s *eligibilityScratch) remove() {
	if s == nil || s.dir == "" {
		return
	}
	_ = os.RemoveAll(s.dir)
}

// scratchGitConfig is the configuration of the scratch repository. It is
// separate from generationConfig even though the two agree today, because this
// repository must never grow a section that could make Git apply a filter, run
// a hook, or write an index: those are the three ways an eligibility listing
// could execute project code or mutate state it is only supposed to inspect.
const scratchGitConfig = `[core]
	repositoryformatversion = 0
	bare = true
	logallrefupdates = false
[gc]
	auto = 0
	autoDetach = false
[maintenance]
	auto = false
`

// newEligibilityScratch builds the scratch repository and its info/exclude file.
//
// info/exclude is where the Marshal-configured ignore rules live. They are
// written verbatim — no comment stripping, no trimming — because a rule the
// user configured is exactly what git should be asked about, and
// `--exclude-standard` reads this file.
func (m *Manager) newEligibilityScratch(excludeRules []string) (*eligibilityScratch, error) {
	dir, err := os.MkdirTemp("", "marshal-eligibility-")
	if err != nil {
		return nil, m.storeError(ReasonUnreadableFile, "", fmt.Errorf("create eligibility scratch directory: %w", err))
	}
	s := &eligibilityScratch{
		dir:       dir,
		gitDir:    filepath.Join(dir, "repo"),
		indexPath: filepath.Join(dir, "index"),
	}
	fail := func(err error) (*eligibilityScratch, error) {
		s.remove()
		return nil, m.storeError(ReasonUnreadableFile, dir, err)
	}
	for _, sub := range []string{"objects", "refs", filepath.Join("info")} {
		if err := os.MkdirAll(filepath.Join(s.gitDir, sub), 0o755); err != nil {
			return fail(err)
		}
	}
	if err := writeFileSynced(filepath.Join(s.gitDir, "HEAD"), []byte("ref: "+generationHeadTarget+"\n"), 0o644); err != nil {
		return fail(err)
	}
	if err := writeFileSynced(filepath.Join(s.gitDir, "config"), []byte(scratchGitConfig), 0o644); err != nil {
		return fail(err)
	}
	exclude := ""
	for _, rule := range excludeRules {
		exclude += rule + "\n"
	}
	if err := writeFileSynced(filepath.Join(s.gitDir, "info", "exclude"), []byte(exclude), 0o644); err != nil {
		return fail(err)
	}
	return s, nil
}

// listUntrackedPaths asks Git which workspace paths are untracked and not
// ignored, as a NUL-delimited list.
//
// Two properties matter and both are load-bearing:
//
//   - The index is a scratch path that is deliberately ABSENT and lives outside
//     the store. Git treats a missing index file as empty, so "everything is
//     untracked" is decided from the workspace itself rather than from the
//     project's index — which is what stops a path that was captured by an
//     earlier run and then excluded from surviving through a stale index.
//   - The project's ignore configuration is never read. The scratch repository
//     supplies info/exclude, and --exclude-standard adds every .gitignore in the
//     work tree, which is Git's own answer to "would you ignore this?".
func (m *Manager) listUntrackedPaths(ctx context.Context, s *eligibilityScratch, workTree string, maxOut int) ([]string, error) {
	out, errOut, truncated, err := m.gitRunEnv(ctx, s.gitDir, workTree, nil,
		[]string{"GIT_INDEX_FILE=" + s.indexPath}, maxOut,
		"ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, m.storeErrorf(ReasonUnreadableFile, workTree,
			"git ls-files output exceeded the %d byte bound; the eligible path list cannot be trusted", maxOut)
	}
	// Git writes to stderr only to report that it could not enumerate or read
	// something. Treating that as an empty list is exactly how an ignored
	// directory holding a multi-gigabyte file used to slip into a snapshot
	// unchallenged, so any diagnostic aborts the capture.
	if msg := strings.TrimSpace(string(errOut)); msg != "" {
		return nil, m.storeErrorf(ReasonUnreadableFile, workTree,
			"git ls-files could not enumerate the workspace: %s", strings.ReplaceAll(msg, "\n", "; "))
	}
	return parseNULPaths(out)
}

// parseNULPaths splits a NUL-delimited path list, refusing anything that is not
// a single well-formed relative path. A malformed element means the listing
// cannot be interpreted, which is an error rather than a path to skip.
func parseNULPaths(out []byte) ([]string, error) {
	if len(out) == 0 {
		return nil, nil
	}
	raw := string(out)
	if !strings.HasSuffix(raw, "\x00") {
		return nil, storeErrorf(ReasonUnreadableFile, "git path list is not NUL-terminated")
	}
	parts := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	for _, p := range parts {
		if p == "" {
			return nil, storeErrorf(ReasonUnreadableFile, "git path list contains an empty path")
		}
		if filepath.IsAbs(p) || p == "." || p == ".." ||
			strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
			return nil, storeErrorf(ReasonUnreadableFile, "git path list contains the unsafe path %q", p)
		}
		if strings.ContainsRune(p, 0) {
			return nil, storeErrorf(ReasonUnreadableFile, "git path list contains an embedded NUL")
		}
	}
	return parts, nil
}

// discoverEligiblePaths lists the workspace with Git's ignore semantics and
// classifies every candidate.
//
// Candidates that are directories are skipped: a nested repository is reported
// by Git as a single directory entry (its administration and contents are not
// enumerated), and capturing it would mean capturing another repository's
// internals or duplicating a submodule. Candidates that are statically larger
// than the per-file cap are skipped, matching the established "excluded because
// too big" behaviour. Everything else that goes wrong is an error.
func (m *Manager) discoverEligiblePaths(ctx context.Context, workTree string, ignore []string, bounds captureBounds, maxFileBytes int64) ([]eligibleEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, interruptedCapture(ctx.Err())
	}
	if workTree == "" {
		return nil, storeErrorf(ReasonInternal, "capture workspace root must not be empty")
	}
	bounds, err := bounds.normalized()
	if err != nil {
		return nil, err
	}
	if maxFileBytes < 0 {
		return nil, storeErrorf(ReasonInvalidLimits, "per-file capture cap must not be negative: %d", maxFileBytes)
	}

	// Additional Marshal-configured rules plus the store exclusion go into the
	// scratch repository's info/exclude, which --exclude-standard honours.
	rules := make([]string, 0, len(ignore)+1)
	rules = append(rules, ignore...)
	storeRule, err := m.storeExclusionRule(workTree)
	if err != nil {
		return nil, err
	}
	if storeRule != "" {
		rules = append(rules, storeRule)
	}

	scratch, err := m.newEligibilityScratch(rules)
	if err != nil {
		return nil, err
	}
	defer scratch.remove()

	// The retained listing must be able to hold every path the total bound
	// allows, or a legitimately large workspace would abort as "truncated".
	maxOut, err := addInt64(bounds.MaxTotalPathBytes, 1<<20)
	if err != nil {
		return nil, err
	}
	if maxOut > int64(maxInt) {
		maxOut = int64(maxInt)
	}
	candidates, err := m.listUntrackedPaths(ctx, scratch, workTree, int(maxOut))
	if err != nil {
		return nil, err
	}

	entries := make([]eligibleEntry, 0, len(candidates))
	var totalPathBytes int64
	for _, rel := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, interruptedCapture(ctx.Err())
		}
		// The path bounds are applied to every candidate, so the work of
		// interpreting the listing is bounded even when nothing is selected.
		totalPathBytes, err = m.checkPathBounds(workTree, rel, bounds, totalPathBytes)
		if err != nil {
			return nil, err
		}

		entry, include, err := m.classifyPath(workTree, rel, maxFileBytes)
		if err != nil {
			return nil, err
		}
		if !include {
			continue
		}
		if len(entries)+1 > bounds.MaxEntries {
			return nil, m.storeErrorf(ReasonBudgetExhausted, workTree,
				"capture would select more than the %d entry bound", bounds.MaxEntries)
		}
		entries = append(entries, entry)
	}

	// Git does not report every path a directory contains. A named pipe,
	// socket, or device is simply not an untracked file as far as Git is
	// concerned, so a capture that trusted the listing alone would omit one
	// silently — and a snapshot that omits a path it never mentioned claims a
	// rollback point it does not have. The directories the capture selected
	// from are therefore inspected directly.
	if err := m.rejectUnsupportedEntries(workTree, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// rejectUnsupportedEntries aborts when a directory the capture selected from
// holds an entry whose type cannot be captured.
//
// The scan is scoped to the directories a selected path lives in, rather than
// to the whole tree, on purpose: eligibility is Git's decision, and walking the
// whole workspace would reintroduce the unbounded traversal this design
// removed. A directory nothing was selected from contributes nothing to the
// snapshot, so there is nothing in it to abort over.
func (m *Manager) rejectUnsupportedEntries(workTree string, entries []eligibleEntry) error {
	dirs := map[string]bool{"": true}
	for _, e := range entries {
		dir, _ := splitPath(e.Path)
		for dir != "" {
			dirs[dir] = true
			dir, _ = splitPath(dir)
		}
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Strings(ordered)

	for _, dir := range ordered {
		full := filepath.Join(workTree, filepath.FromSlash(dir))
		entriesRead, err := os.ReadDir(full)
		if err != nil {
			return m.storeError(ReasonUnreadableFile, full,
				fmt.Errorf("read selected directory: %w", err))
		}
		for _, de := range entriesRead {
			// DirEntry.Info uses Lstat semantics, so a symlink is reported as a
			// symlink and never followed.
			info, err := de.Info()
			if err != nil {
				return m.storeError(ReasonUnreadableFile, filepath.Join(full, de.Name()), err)
			}
			mode := info.Mode()
			if mode.IsRegular() || mode.IsDir() || mode&os.ModeSymlink != 0 {
				continue
			}
			return m.storeErrorf(ReasonUnsupportedFileType, filepath.Join(full, de.Name()),
				"a directory this capture selected from contains a %s, which cannot be captured",
				describeMode(mode))
		}
	}
	return nil
}

// checkPathBounds applies the per-path and total-path bounds to one candidate
// and returns the running total.
//
// It is a separate function so the boundary arithmetic can be verified directly.
// A path at the 4096-byte production bound cannot be materialised on a
// filesystem whose PATH_MAX is 4096, so a test that could only build real files
// would never exercise the number this code is guarding.
func (m *Manager) checkPathBounds(workTree, rel string, bounds captureBounds, running int64) (int64, error) {
	if len(rel) > bounds.MaxPathBytes {
		return running, m.storeErrorf(ReasonBudgetExhausted, workTree,
			"path of %d bytes exceeds the %d byte per-path bound", len(rel), bounds.MaxPathBytes)
	}
	total, err := addInt64(running, int64(len(rel)))
	if err != nil {
		return running, err
	}
	if total > bounds.MaxTotalPathBytes {
		return running, m.storeErrorf(ReasonBudgetExhausted, workTree,
			"eligible paths total %d bytes, over the %d byte bound", total, bounds.MaxTotalPathBytes)
	}
	return total, nil
}

// classifyPath decides whether one candidate path may be captured. It returns
// (entry, true, nil) for a selected path, (zero, false, nil) for a path that is
// deliberately left out, and an error for anything that must abort.
//
// "Deliberately left out" is only ever one of two things: a directory (a nested
// repository, which Git reports as a directory entry) or a path already larger
// than the per-file cap at classification time. Every other outcome — an
// unreadable path, an unsupported type, a link whose target cannot be read — is
// an error, because a snapshot that silently omits what it could not read
// claims a rollback point it does not have.
func (m *Manager) classifyPath(workTree, rel string, maxFileBytes int64) (eligibleEntry, bool, error) {
	full := filepath.Join(workTree, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil {
		return eligibleEntry{}, false, m.storeError(ReasonUnreadableFile, full,
			fmt.Errorf("stat eligible path: %w", err))
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		// A nested repository (or any directory Git reports) is skipped: its
		// contents are not part of this workspace's history.
		return eligibleEntry{}, false, nil
	case mode.IsRegular():
		if maxFileBytes > 0 && info.Size() > maxFileBytes {
			return eligibleEntry{}, false, nil
		}
		return eligibleEntry{Path: rel, Kind: capturedRegular, Mode: mode, Size: info.Size()}, true, nil
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return eligibleEntry{}, false, m.storeError(ReasonUnreadableFile, full,
				fmt.Errorf("read symlink target: %w", err))
		}
		size := int64(len(target))
		if maxFileBytes > 0 && size > maxFileBytes {
			return eligibleEntry{}, false, nil
		}
		return eligibleEntry{Path: rel, Kind: capturedSymlink, Mode: mode, Size: size}, true, nil
	default:
		return eligibleEntry{}, false, m.storeErrorf(ReasonUnsupportedFileType, full,
			"eligible path is a %s, which cannot be captured", describeMode(mode))
	}
}

// describeMode names a file type for a diagnostic. The message is what a user
// sees when a capture is refused, so it names the type rather than printing a
// mode bitmask.
func describeMode(mode os.FileMode) string {
	switch {
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeDevice != 0 && mode&os.ModeCharDevice != 0:
		return "character device"
	case mode&os.ModeDevice != 0:
		return "block device"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "directory"
	default:
		return mode.Type().String()
	}
}

// interruptedCapture wraps a context error as a structured capture failure
// while keeping errors.Is(err, context.Canceled) true for callers that branch
// on cancellation.
func interruptedCapture(err error) error {
	return storeError(ReasonInterruptedCapture, fmt.Errorf("capture interrupted: %w", err))
}

// maxInt is the largest int value. addInt64 works in int64; the output bound is
// an int, so a computed bound is clamped to keep the conversion defined.
const maxInt = int(^uint(0) >> 1)
