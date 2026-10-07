package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file turns a set of eligible paths into a snapshot: blobs, Git trees
// built by hand, one PARENTLESS commit, and a published ref.
//
// Everything here is a deliberate refusal of what `git add`/`git commit` do:
//
//   - No persistent index. Eligibility is decided from an empty scratch index
//     that lives outside the store, so a path excluded today cannot survive
//     through an index entry recorded yesterday.
//   - No parent. A commit with no parent line is reachable exactly when a ref
//     names it, so deleting a generation releases its history instead of
//     leaving it reachable through HEAD or a branch.
//   - No unbounded subprocess write. Trees, the commit, and the ref are built
//     here and written through the budgeted object writer.
//
// Capture is the last step: it is only ever reached with an admitted allowance,
// and every failure returns an EMPTY hash. A capture that cannot prove it
// published must never hand back something that looks usable.

// DefaultMaintenanceAllowanceBytes is the per-workspace maintenance allowance
// that is reserved ON TOP of a capture's write allowance. Maintenance work
// (reconciling an interrupted capture, sealing a generation, removing a stage
// directory) needs a small amount of headroom that must not be raidable by the
// capture itself. It is a named constant rather than a hidden literal so a test
// can shrink it, and it is never charged to the capture's own budget.
const DefaultMaintenanceAllowanceBytes int64 = 4 << 20 // 4 MiB

// CaptureRequest describes one capture. It is a value rather than a long
// argument list because every field is a decision someone must make
// deliberately: the workspace, the ignore rules, the per-file cap, and the
// admitted allowance.
type CaptureRequest struct {
	// WorkspaceRoot is the workspace to capture. It must be an absolute path.
	WorkspaceRoot string
	// Ignore carries the Marshal-configured ignore rules. They are written
	// verbatim into the scratch repository's info/exclude, so Git applies them
	// with its own semantics rather than an approximation of them.
	Ignore []string
	// MaxFileBytes is the per-file cap. A file larger than this at
	// classification time is left out of the snapshot, matching the established
	// behaviour. A file that grows past the cap after classification still
	// aborts: the cap is not a licence to record different bytes than were
	// hashed.
	MaxFileBytes int64
	// Bounds carries the per-capture selection bounds.
	Bounds captureBounds
	// Allowance is the admitted write allowance. A capture never spends more
	// than this, and a plan's own estimate is an upper bound on what it needs.
	Allowance int64
	// Message is the commit message body. An empty message uses the default.
	Message string
	// Now fixes the commit timestamp. A zero value uses the manager's clock.
	Now time.Time
	// Author and Committer are the identities recorded on the commit. Empty
	// values use the store's own identity, which is deliberately not the user's
	// Git identity: the snapshot is Marshal's, and a user's gitconfig must not
	// be able to change whether a capture reproduces.
	Author    string
	Committer string

	// beforePublish, when set, runs AFTER every object is durable and BEFORE
	// the publication ref is written. It is the publication-intent hook: the
	// intent has to be recorded before the ref exists, because a crash between
	// the ref landing and the manifest catching up must leave a record saying
	// which ref to look for. Reconciliation cannot distinguish "published" from
	// "never published" without it.
	beforePublish func(hash string) error
}

// defaultCaptureIdentity is the fixed author/committer identity. It matches the
// legacy shadow repository's identity so a snapshot's provenance does not
// change just because capture moved.
const defaultCaptureIdentity = "marshal <marshal@local>"

// snapshotCommitMessage is the default commit message body. It is stable so two
// captures of identical content produce the identical commit hash.
const snapshotCommitMessage = "marshal snapshot\n"

// capturePlan is the result of the estimate phase: the paths a capture selected
// and a conservative upper bound on the bytes writing them will consume.
//
// The bound is conservative in one direction only, and deliberately so: it
// assumes every object is new. An object that already exists spends nothing, so
// a plan can never under-reserve, and a capture admitted against it can never
// exceed it.
type capturePlan struct {
	// Entries are the selected paths, ordered by path bytes so a capture is
	// deterministic and so two identical trees produce identical objects.
	Entries []eligibleEntry
	// Allowance is the total write allowance the plan needs, including the
	// commit, the manifest replacement, and the ref and staging records.
	Allowance int64
	// Maintenance is the separate per-workspace maintenance allowance that must
	// be reserved on top of Allowance.
	Maintenance int64

	// Breakdown is the same total, itemized. It exists so a test can assert a
	// specific component rather than only its sum.
	Breakdown captureCostBreakdown
}

// captureCostBreakdown itemizes a plan's estimate.
type captureCostBreakdown struct {
	BlobBytes     int64
	TreeBytes     int64
	CommitBytes   int64
	DirBytes      int64
	ManifestBytes int64
	RefBytes      int64
	StagingBytes  int64
	Entries       int
	Trees         int
}

// Total is the sum of the itemized components.
func (b captureCostBreakdown) Total() int64 {
	return b.BlobBytes + b.TreeBytes + b.CommitBytes + b.DirBytes +
		b.ManifestBytes + b.RefBytes + b.StagingBytes
}

// captureResult reports what a capture wrote.
type captureResult struct {
	// Hash is the snapshot commit hash. It is empty on every failure.
	Hash string
	// Entries, Blobs, and Trees count what was selected and written.
	Entries int
	Blobs   int
	Trees   int
	// Deduped counts objects that already existed and were verified.
	Deduped int
	// Verified counts objects whose hash Git independently confirmed.
	Verified int
	// Spent is the budget's charged total, which can never exceed the
	// allowance the capture was admitted with.
	Spent int64
	// Allowance is the admitted allowance.
	Allowance int64
}

// planCapture discovers the eligible paths and computes a conservative write
// estimate. It performs no writes: discovering and estimating are separate from
// capturing so admission (Task 5) can decide on the estimate before any byte of
// the store is touched.
func (m *Manager) planCapture(ctx context.Context, cat *Catalog, req CaptureRequest) (*capturePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, interruptedCapture(err)
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}
	entries, err := m.discoverEligiblePaths(ctx, req.WorkspaceRoot, req.Ignore, req.Bounds, req.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	breakdown, err := m.estimateCaptureCosts(cat, entries)
	if err != nil {
		return nil, err
	}
	total := breakdown.Total()
	maintenance, err := m.maintenanceAllowance(limits)
	if err != nil {
		return nil, err
	}
	return &capturePlan{
		Entries:     entries,
		Allowance:   total,
		Maintenance: maintenance,
		Breakdown:   breakdown,
	}, nil
}

// maintenanceAllowance is the reserved per-workspace maintenance headroom,
// rounded up to allocation units so it can actually hold the directory entries
// maintenance creates.
func (m *Manager) maintenanceAllowance(limits Limits) (int64, error) {
	n := m.maintenanceBytes
	if n <= 0 {
		n = DefaultMaintenanceAllowanceBytes
	}
	return limits.RoundUp(n)
}

// estimateCaptureCosts sums the worst-case cost of writing one plan.
//
// Every component is an upper bound:
//
//   - Each blob is bounded by the zlib representation of its full object.
//   - Each tree is bounded by its immediate children's name bytes plus 32 bytes
//     per child (20 for the object id and 12 for the mode, spacers, and NUL),
//     then bounded by its zlib representation in turn.
//   - The commit is bounded by the maximum commit payload, then by its zlib
//     representation.
//   - Object directories are bounded by one allocation unit per possible
//     two-hex-character prefix, capped by the number of objects.
//   - The manifest replacement, the ref record, and the staging record are each
//     rounded up to allocation units.
//
// Deduplication is not subtracted. The estimate assumes every object is new
// because that is the only assumption that cannot under-reserve: a plan that
// assumed a dedup would have to prove it before the capture that produces it.
func (m *Manager) estimateCaptureCosts(cat *Catalog, entries []eligibleEntry) (captureCostBreakdown, error) {
	limits, err := m.EffectiveLimits()
	if err != nil {
		return captureCostBreakdown{}, err
	}
	var out captureCostBreakdown
	out.Entries = len(entries)

	for _, e := range entries {
		header := objectHeader(objTypeBlob, e.Size)
		rawLen, err := addInt64(int64(len(header)), e.Size)
		if err != nil {
			return out, err
		}
		bound, err := zlibBound(rawLen)
		if err != nil {
			return out, err
		}
		rounded, err := limits.RoundUp(bound)
		if err != nil {
			return out, err
		}
		if out.BlobBytes, err = addInt64(out.BlobBytes, rounded); err != nil {
			return out, err
		}
	}

	// Tree payload bounds. Each directory contributes one tree, and its payload
	// is bounded by the sum of its immediate children's name bytes plus 32
	// bytes per child.
	treePayloads := estimateTreePayloads(entries)
	out.Trees = len(treePayloads)
	for _, payload := range treePayloads {
		bound, err := zlibBound(payload)
		if err != nil {
			return out, err
		}
		rounded, err := limits.RoundUp(bound)
		if err != nil {
			return out, err
		}
		if out.TreeBytes, err = addInt64(out.TreeBytes, rounded); err != nil {
			return out, err
		}
	}

	// The commit. Its payload is bounded by the fixed maximum below rather than
	// by the message length, because a caller-supplied message must not be able
	// to make the reservation smaller than what is written.
	commitBound, err := zlibBound(maxCommitPayloadBytes)
	if err != nil {
		return out, err
	}
	if out.CommitBytes, err = limits.RoundUp(commitBound); err != nil {
		return out, err
	}

	// Object directories: one allocation unit per distinct two-hex prefix, and
	// a directory can hold at least one object, so the count of objects bounds
	// the count of directories from above. The staging directory is one more.
	objects := int64(out.Entries) + int64(out.Trees) + 1
	dirs := min64(objects, 256) + 1
	unit, err := limits.RoundUp(0)
	if err != nil {
		return out, err
	}
	dirProduct, err := mulDiv(dirs, unit, 1)
	if err != nil {
		return out, err
	}
	out.DirBytes = dirProduct

	// The manifest replacement: an atomic replace holds the old and the new
	// manifest at the same time, and each is bounded by ManifestMaxBytes.
	manifest, err := cat.ManifestReplacementBytes()
	if err != nil {
		return out, err
	}
	out.ManifestBytes = manifest

	// The publication ref and the staging record, each rounded to units.
	refBytes, err := limits.RoundUp(int64(len(snapshotRefPrefix) + 40 + 1))
	if err != nil {
		return out, err
	}
	out.RefBytes = refBytes
	stagingRecord, err := limits.RoundUp(int64(len(stagingIDPrefix) + stagingIDRandomLen + 8))
	if err != nil {
		return out, err
	}
	out.StagingBytes = stagingRecord
	return out, nil
}

// maxCommitPayloadBytes bounds a commit object's body. A commit this store
// writes holds one tree line, two identity lines, and the message, so the bound
// is generous enough that a legitimately long message still fits while still
// being a bound.
const maxCommitPayloadBytes int64 = 4096

// estimateTreePayloads returns the payload-size bound of every tree a capture
// of entries would build, derived from the same trie the capture itself walks.
//
// Deriving both from one structure is deliberate: an estimate built from a
// separate walk would eventually disagree with the capture it is supposed to
// bound, and a bound that disagrees with its work is not a bound.
func estimateTreePayloads(entries []eligibleEntry) []int64 {
	root := newTreeTrie(entries)
	var out []int64
	var walk func(n *treeTrie)
	walk = func(n *treeTrie) {
		var payload int64
		for name, child := range n.children {
			payload += int64(len(name)) + 32
			_ = child
		}
		out = append(out, payload)
		for _, child := range n.children {
			walk(child)
		}
	}
	walk(root)
	return out
}

// treeTrie is a directory tree built from selected paths. Children are either
// files (a leaf with a blob hash and mode) or subtrees.
type treeTrie struct {
	name     string
	children map[string]*treeTrie
	// file is set on a leaf that names a blob.
	file *capturedFile
}

// newTreeTrie builds a trie from selected paths. Only the path structure is
// needed for the estimate; the capture fills in hashes as it writes.
func newTreeTrie(entries []eligibleEntry) *treeTrie {
	root := &treeTrie{children: map[string]*treeTrie{}}
	for _, e := range entries {
		node := root
		parts := strings.Split(e.Path, "/")
		for i, part := range parts {
			child, ok := node.children[part]
			if !ok {
				child = &treeTrie{name: part, children: map[string]*treeTrie{}}
				node.children[part] = child
			}
			if i == len(parts)-1 {
				// The leaf records the path itself so the capture can find the
				// blob hash it wrote for that path.
				file := &capturedFile{Path: e.Path}
				child.file = file
			}
			node = child
		}
	}
	return root
}

// captureToGeneration writes a plan into a generation and publishes its ref.
//
// The order is load-bearing: every object is verified and durable BEFORE the
// ref that retains it is written, and the ref is the last thing written. An
// interrupted capture therefore leaves unreachable objects and no ref, which is
// invisible to rollback and reclaimable as a whole generation — never a ref
// naming objects that are not there.
func (m *Manager) captureToGeneration(ctx context.Context, cat *Catalog, generationID string, plan *capturePlan, req CaptureRequest) (*captureResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, interruptedCapture(err)
	}
	if plan == nil {
		return nil, storeErrorf(ReasonInternal, "capture needs a plan")
	}
	if err := cat.requireOwned(); err != nil {
		return nil, err
	}
	limits, err := m.EffectiveLimits()
	if err != nil {
		return nil, err
	}
	gitDir, err := cat.GenerationDir(generationID)
	if err != nil {
		return nil, err
	}
	if !gitDirExists(gitDir) {
		return nil, cat.errf(ReasonUnreadableFile, "generation %s repository is not initialised", generationID)
	}

	budget, err := newObjectBudget(limits, req.Allowance)
	if err != nil {
		return nil, err
	}
	writer, err := newObjectWriter(ctx, m, gitDir, budget)
	if err != nil {
		return nil, err
	}
	defer writer.cleanup()

	result := &captureResult{Allowance: req.Allowance, Entries: len(plan.Entries)}

	// Write the blobs first: a tree entry names a blob, so blobs must exist
	// before the tree that references them is written.
	trie := newTreeTrie(plan.Entries)
	blobHashes := make(map[string]string, len(plan.Entries))
	for _, entry := range plan.Entries {
		if err := ctx.Err(); err != nil {
			return nil, interruptedCapture(err)
		}
		hash, err := writer.writeEntry(req.WorkspaceRoot, entry)
		if err != nil {
			return nil, err
		}
		blobHashes[entry.Path] = hash
		// The mode recorded in the tree is the one observed while the content
		// was read, not the one observed when the path was classified, so a
		// mode change between the two cannot produce a tree entry that
		// disagrees with the bytes it names.
		if leaf := trie.find(entry.Path); leaf != nil && leaf.file != nil {
			leaf.file.Mode = actualGitMode(req.WorkspaceRoot, entry)
		}
		result.Blobs++
	}

	// Build and write the trees bottom-up.
	rootTree, trees, err := writer.buildTrees(trie, blobHashes)
	if err != nil {
		return nil, err
	}
	result.Trees = trees

	// The parentless commit.
	commitHash, err := writer.writeCommit(rootTree, req, m.Now())
	if err != nil {
		return nil, err
	}

	// Crash boundary: every object is durable and nothing is published yet.
	if err := m.runHook(m.hooks.BeforePublishRef); err != nil {
		return nil, err
	}

	// Record the publication INTENT before the ref exists. This is the only
	// point at which it can be recorded: after the ref is written, a crash would
	// leave a published ref with no durable statement that the capture intended
	// it, and reconciliation would then have to guess whether the snapshot was
	// meant to exist.
	//
	// The intent names the ref and hash exactly as they will be published, and
	// a failure to record it aborts the capture BEFORE the ref — so a capture
	// that could not say what it was about to publish publishes nothing.
	if req.beforePublish != nil {
		if err := req.beforePublish(commitHash); err != nil {
			return nil, err
		}
	}

	// Publish the ref only now, with every object durable.
	ref, err := snapshotRefFor(commitHash)
	if err != nil {
		return nil, err
	}
	if err := m.writeRefAtomically(gitDir, ref, commitHash); err != nil {
		return nil, err
	}
	// Write boundary: the publication ref is durable, so the snapshot's
	// objects are now retained rather than merely present.
	m.observeWrite(WriteEvent{
		Kind:      "ref",
		Path:      ref,
		Allowance: req.Allowance,
	})

	verified, deduped := writer.Stats()
	result.Hash = commitHash
	result.Verified = verified
	result.Deduped = deduped
	result.Spent = budget.Spent()
	return result, nil
}

// CaptureSnapshot plans and captures one workspace into a generation under the
// request's allowance, and returns the published snapshot hash.
//
// It is the single entry point a caller needs, and it exists so that
// "a capture that failed" and "a capture that published" cannot be confused: a
// failure returns the EMPTY string together with a structured error, never a
// hash that looks usable. A caller that records a snapshot row must therefore
// record one only when the hash is non-empty and the error is nil.
//
// Nothing is activated at runtime by this task: the existing Service.Track path
// is untouched until bounded admission lands, and this method is reachable only
// from tests and from that eventual switch.
func (m *Manager) CaptureSnapshot(ctx context.Context, cat *Catalog, generationID string, req CaptureRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", interruptedCapture(err)
	}
	if cat == nil {
		return "", storeErrorf(ReasonInternal, "capture needs a catalog")
	}
	plan, err := m.planCapture(ctx, cat, req)
	if err != nil {
		return "", err
	}
	// The request's own allowance is used as given: this method does not widen
	// it from the plan, so a caller that passes a too-small allowance gets the
	// budget error rather than silently succeeding with more than it admitted.
	result, err := m.captureToGeneration(ctx, cat, generationID, plan, req)
	if err != nil {
		return "", err
	}
	if result == nil || result.Hash == "" {
		// A capture that produced no hash must never be reported as a success.
		return "", storeErrorf(ReasonInternal, "capture completed without a published snapshot")
	}
	return result.Hash, nil
}

// capturedFile is one blob already written, ready to be named by a tree entry.
type capturedFile struct {
	// Path is the slash-separated workspace-relative path.
	Path string
	// Mode is the Git mode string the tree entry records.
	Mode string
}

// find returns the trie node for a selected path, or nil.
func (t *treeTrie) find(path string) *treeTrie {
	node := t
	for _, part := range strings.Split(path, "/") {
		next, ok := node.children[part]
		if !ok {
			return nil
		}
		node = next
	}
	return node
}

// actualGitMode returns the Git mode for a path as observed NOW, by re-stating
// the path. It is called from writeEntry's caller rather than from the tree
// builder so the mode recorded is the one that was in force while the content
// was read.
//
// A path that vanished between being read and being re-stated is reported as a
// non-executable regular file; its bytes are already in the object store, and
// the only thing a vanished path can no longer change is its mode.
func actualGitMode(workTree string, entry eligibleEntry) string {
	if entry.Kind == capturedSymlink {
		return "120000"
	}
	full := filepath.Join(workTree, filepath.FromSlash(entry.Path))
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return "100644"
	}
	if info.Mode()&0o111 != 0 {
		return "100755"
	}
	return "100644"
}

// treeChild is one immediate child of a tree being built.
type treeChild struct {
	name   string
	hash   string
	mode   string
	isTree bool
}

// sortKey is the byte sequence Git orders tree entries by: the name, with a
// tree's name compared as if it had a trailing slash. Getting this wrong
// produces a tree whose *content hash* disagrees with what Git computes for the
// same entries, which is exactly the kind of silent corruption this store must
// not have.
func (c treeChild) sortKey() string {
	if c.isTree {
		return c.name + "/"
	}
	return c.name
}

// buildTreepayload renders a tree's content bytes.
//
// The entry format is `mode SP name NUL <20-byte object id>` with no separator
// between entries. The object id is binary, not hexadecimal: a tree holding
// hexadecimal ids would hash to something Git does not recognise.
func buildTreePayload(children []treeChild) ([]byte, error) {
	var out []byte
	for _, c := range children {
		raw, err := decodeObjectHash(c.hash)
		if err != nil {
			return nil, err
		}
		out = append(out, c.mode...)
		out = append(out, ' ')
		out = append(out, c.name...)
		out = append(out, 0)
		out = append(out, raw...)
	}
	return out, nil
}

// decodeObjectHash turns a validated hexadecimal object id into its raw bytes.
func decodeObjectHash(hash string) ([]byte, error) {
	if !validObjectHash(hash) {
		return nil, storeErrorf(ReasonInternal, "object hash %q is not a hexadecimal object id", hash)
	}
	return hexDecode(strings.ToLower(hash))
}

// buildTrees writes every tree a capture needs, bottom-up, and returns the root
// tree's hash and the number of trees written.
//
// The trees are built from the same trie the payload estimate walks, so the
// estimate is derived from the structure that is actually written rather than
// from a second, independently drifting model of it.
func (w *objectWriter) buildTrees(root *treeTrie, hashes map[string]string) (string, int, error) {
	if root == nil || len(root.children) == 0 {
		// An empty capture is refused rather than producing an empty tree: a
		// snapshot that records nothing would look like a successful rollback
		// point for a workspace it never inspected.
		return "", 0, w.manager.storeErrorf(ReasonInternal, w.gitDir,
			"refusing to capture a workspace with no eligible paths")
	}
	written := 0
	hash, err := w.buildTreeNode(root, hashes, &written)
	if err != nil {
		return "", written, err
	}
	return hash, written, nil
}

// buildTreeNode writes one directory's tree, recursively writing its subtrees
// first so every child hash is known before the parent's payload is built.
func (w *objectWriter) buildTreeNode(n *treeTrie, hashes map[string]string, written *int) (string, error) {
	children := make([]treeChild, 0, len(n.children))
	for name, child := range n.children {
		if child.file != nil {
			hash, ok := hashes[child.file.Path]
			if !ok {
				return "", w.manager.storeErrorf(ReasonInternal, w.gitDir,
					"no blob was written for selected path %q", child.file.Path)
			}
			children = append(children, treeChild{name: name, hash: hash, mode: child.file.Mode})
			continue
		}
		hash, err := w.buildTreeNode(child, hashes, written)
		if err != nil {
			return "", err
		}
		children = append(children, treeChild{name: name, hash: hash, mode: "40000", isTree: true})
	}
	// Git's canonical ordering: entries sorted by name bytes, with a tree's
	// name compared as if it had a trailing slash.
	sort.Slice(children, func(i, j int) bool { return children[i].sortKey() < children[j].sortKey() })
	if err := rejectDuplicateTreeNames(children, n.name); err != nil {
		return "", w.manager.storeError(ReasonInternal, w.gitDir, err)
	}
	payload, err := buildTreePayload(children)
	if err != nil {
		return "", w.manager.storeError(ReasonInternal, w.gitDir, err)
	}
	obj, err := newGitObject(objTypeTree, payload)
	if err != nil {
		return "", err
	}
	if _, err := w.installObject(obj); err != nil {
		return "", err
	}
	*written++
	return obj.Hash, nil
}

// rejectDuplicateTreeNames refuses a tree with two children of the same
// canonical name. Git cannot represent such a tree, and silently dropping one
// of the entries would produce a snapshot that omits a path it claimed to
// capture.
func rejectDuplicateTreeNames(children []treeChild, dir string) error {
	seen := make(map[string]bool, len(children))
	for _, c := range children {
		if seen[c.name] {
			return fmt.Errorf("directory %q has two entries named %q", dir, c.name)
		}
		seen[c.name] = true
	}
	return nil
}

// splitPath splits a slash-separated path into its directory and final name.
// The root directory is "".
func splitPath(p string) (dir, name string) {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

// writeCommit builds and writes the parentless commit object.
//
// No parent line is written, ever. A parent would make the previous snapshot
// reachable from this one, and a chain of parents is how a store kept thousands
// of "deleted" snapshots alive through a single surviving ref. Parentless
// commits also mean the commit hash depends only on the tree, the identity, the
// timestamp, and the message — which is what makes a repeated capture of
// identical content reproducible.
func (w *objectWriter) writeCommit(treeHash string, req CaptureRequest, now time.Time) (string, error) {
	if !validObjectHash(treeHash) {
		return "", w.manager.storeErrorf(ReasonInternal, w.gitDir, "tree hash %q is not a hexadecimal object id", treeHash)
	}
	author := req.Author
	if author == "" {
		author = defaultCaptureIdentity
	}
	committer := req.Committer
	if committer == "" {
		committer = defaultCaptureIdentity
	}
	message := req.Message
	if message == "" {
		message = snapshotCommitMessage
	}
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	at := req.Now
	if at.IsZero() {
		at = now
	}
	// The timestamp is recorded in UTC with an explicit zero offset, so the
	// commit's bytes do not depend on the machine's timezone.
	seconds := at.UTC().Unix()
	if seconds < 0 {
		return "", w.manager.storeErrorf(ReasonInternal, w.gitDir, "commit time %d is before the epoch", seconds)
	}

	var b strings.Builder
	b.WriteString("tree ")
	b.WriteString(strings.ToLower(treeHash))
	b.WriteString("\n")
	b.WriteString("author ")
	b.WriteString(author)
	b.WriteString(fmt.Sprintf(" %d +0000\n", seconds))
	b.WriteString("committer ")
	b.WriteString(committer)
	b.WriteString(fmt.Sprintf(" %d +0000\n", seconds))
	b.WriteString("\n")
	b.WriteString(message)

	obj, err := newGitObject(objTypeCommit, []byte(b.String()))
	if err != nil {
		return "", err
	}
	if _, err := w.installObject(obj); err != nil {
		return "", err
	}
	return obj.Hash, nil
}

// writeEntry reads one selected path and writes its blob.
//
// A regular file goes through the exact-read protocol. A symbolic link
// contributes its target TEXT and is never followed: a link pointing outside
// the workspace records a path string, never the content it points at.
func (w *objectWriter) writeEntry(workTree string, entry eligibleEntry) (string, error) {
	full := filepath.Join(workTree, filepath.FromSlash(entry.Path))
	switch entry.Kind {
	case capturedSymlink:
		target, err := openWorkspaceSymlink(full)
		if err != nil {
			return "", w.manager.storeError(ReasonUnreadableFile, full,
				fmt.Errorf("read symlink target: %w", err))
		}
		return w.writeBlob(&bytesBlobSource{desc: full, data: []byte(target)})
	case capturedRegular:
		src, err := newFileBlobSource(full)
		if err != nil {
			return "", w.classifyOpenError(full, err)
		}
		defer src.Close()
		return w.writeBlob(src)
	default:
		return "", w.manager.storeErrorf(ReasonUnsupportedFileType, full,
			"selected path has unsupported kind %s", entry.Kind)
	}
}

// classifyOpenError turns the no-follow open's failures into the structured
// reason each one deserves. A path that changed type between classification and
// opening is not "unsupported": it is a capture that can no longer describe
// what it selected, and it aborts either way.
func (w *objectWriter) classifyOpenError(path string, err error) error {
	var unsupported *unsupportedTypeError
	switch {
	case errors.Is(err, errFinalSymlink):
		return w.manager.storeError(ReasonUnreadableFile, path,
			fmt.Errorf("path became a symlink after it was classified: %w", err))
	case errors.As(err, &unsupported):
		return w.manager.storeError(ReasonUnsupportedFileType, path, unsupported)
	default:
		return w.manager.storeError(ReasonUnreadableFile, path, err)
	}
}

// errFinalSymlink reports that a path opened as a symbolic link when a regular
// file was expected. It is not a capture failure on its own; how it is handled
// depends on whether the path was classified as a link.
var errFinalSymlink = errors.New("path is a symbolic link")

// unsupportedTypeError reports a path whose type cannot be captured.
type unsupportedTypeError struct {
	Path        string
	Description string
}

func (e *unsupportedTypeError) Error() string {
	return fmt.Sprintf("%s is a %s, which cannot be captured", e.Path, e.Description)
}

// ---------------------------------------------------------------------------
// Ref publication
// ---------------------------------------------------------------------------

// writeRefAtomically publishes a snapshot ref as a manager-owned bounded file
// write.
//
// `git update-ref` is not used and must never be: it takes a lock, writes an
// unbounded reflog entry, and can create a packed-refs file, none of which this
// store accounts for. A ref file is one line of text, and writing it by hand
// makes it exactly one accounted allocation.
//
// The temp file lives beside the final ref so the rename is atomic within one
// filesystem, and both directories are flushed so the name survives a crash.
func (m *Manager) writeRefAtomically(gitDir, ref, hash string) error {
	if !strings.HasPrefix(ref, snapshotRefPrefix) {
		return m.storeErrorf(ReasonInternal, gitDir, "ref %q is not a snapshot ref", ref)
	}
	if !validObjectHash(hash) {
		return m.storeErrorf(ReasonInternal, gitDir, "ref target %q is not a hexadecimal object id", hash)
	}
	refPath := filepath.Join(gitDir, filepath.FromSlash(ref))
	refDir := filepath.Dir(refPath)
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		return m.storeError(ReasonUnreadableFile, refDir, err)
	}
	tmp, err := os.CreateTemp(refDir, ".tmp-ref-*")
	if err != nil {
		return m.storeError(ReasonUnreadableFile, refDir, err)
	}
	tmpName := tmp.Name()
	discard := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		discard()
		return m.storeError(ReasonUnreadableFile, tmpName, err)
	}
	if _, err := tmp.WriteString(strings.ToLower(hash) + "\n"); err != nil {
		_ = tmp.Close()
		discard()
		return m.storeError(ReasonUnreadableFile, tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		discard()
		return m.storeError(ReasonUnreadableFile, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		discard()
		return m.storeError(ReasonUnreadableFile, tmpName, err)
	}
	if err := os.Rename(tmpName, refPath); err != nil {
		discard()
		return m.storeError(ReasonUnreadableFile, refPath, err)
	}
	if err := syncDir(refDir); err != nil {
		return m.storeError(ReasonUnreadableFile, refDir, err)
	}
	// The refs directory entry itself is flushed too, so a ref directory created
	// by this write is durable rather than merely present.
	if parent := filepath.Dir(refDir); parent != gitDir {
		if err := syncDir(parent); err != nil {
			return m.storeError(ReasonUnreadableFile, parent, err)
		}
	}
	return nil
}

// hexDecode decodes a hexadecimal string to bytes without pulling in a decoder
// that allocates on every call. The input is validated by validObjectHash
// before this is reached.
func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("hexadecimal string %q has an odd length", s)
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, ok := hexNibble(s[i*2])
		if !ok {
			return nil, fmt.Errorf("hexadecimal string %q has an invalid digit", s)
		}
		lo, ok := hexNibble(s[i*2+1])
		if !ok {
			return nil, fmt.Errorf("hexadecimal string %q has an invalid digit", s)
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
