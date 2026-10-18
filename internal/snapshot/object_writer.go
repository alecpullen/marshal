package snapshot

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// This file is the only place Marshal writes snapshot objects, and it writes
// them without letting a subprocess touch the store.
//
// Three rules are enforced here, and none of them is advisory:
//
//  1. NOTHING WRITES THROUGH GIT. Git's own writers create packs, indexes,
//     refs, and temporary files with no reservation, and an interrupted pack
//     leaves exactly the abandoned temporary packs this work exists to remove.
//     Git is therefore used for one thing only — computing the canonical hash
//     of bytes, read-only, with filters disabled — and every byte that reaches
//     the store is written by the budgeted writer below.
//  2. THE BOUND IS RESERVED BEFORE THE WRITE, NOT MEASURED AFTER IT. A writer
//     that checks afterwards has already written the bytes; the disk was
//     already filled. Every allocation, including each object directory, is
//     charged against the allowance before it is created.
//  3. THE HASH THAT NAMES AN OBJECT IS COMPUTED FROM THE BYTES THAT WERE
//     STORED. Git's hash is required to agree with the in-process hash, the
//     zlib stream uses stored blocks whose size is bounded in advance, and the
//     object's declared header and content length are re-verified by
//     decompressing what actually landed on disk before it is given its name.

// Git object type names, as they appear in a loose-object header.
const (
	objTypeBlob   = "blob"
	objTypeTree   = "tree"
	objTypeCommit = "commit"
)

// objectTempPrefix marks a partially written object inside the generation's
// objects directory. The prefix is deliberately neither a valid object
// directory name (two hex characters) nor inside objects/pack, so neither Git
// nor the store's own accounting can mistake a half-written object for a real
// one.
const objectTempPrefix = "tmp-marshal-object-"

// incomingDirName is the directory inside objects/ where a partially written
// object is staged before it is renamed to its content-addressed name. It is
// charged as a directory entry like any other allocation.
const incomingDirName = "incoming"

// Loose-object encoding constants. The zlib representation uses stored
// (uncompressed) deflate blocks so its size is bounded by the payload length
// rather than by the content: incompressible content must never be assumed to
// shrink, because a bound derived from "compression usually helps" is not a
// bound at all.
const (
	// zlibBlockSize is the largest payload one deflate stored block carries.
	zlibBlockSize int64 = 65535
	// storedBlockHeaderBytes is a stored block's overhead: a 1-byte block
	// header plus the 2-byte length and its one's complement.
	storedBlockHeaderBytes int64 = 5
	// zlibWrapperBytes is the zlib wrapper around the deflate stream: a 2-byte
	// header and a 4-byte Adler-32 trailer.
	zlibWrapperBytes int64 = 6
	// storedBlockSlack allows for the payload's remainder block and for the
	// final empty stored block the Go flate writer always emits. Two is what
	// makes the bound hold for every length, not only the ones a test tries.
	storedBlockSlack int64 = 2
)

// storedBlockOverhead returns the zlib bytes a stored-block stream adds on top
// of an n-byte payload.
func storedBlockOverhead(n int64) (int64, error) {
	if n < 0 {
		return 0, storeErrorf(ReasonInvalidLimits, "cannot bound a negative length: %d", n)
	}
	blocks := n / zlibBlockSize
	if n%zlibBlockSize != 0 {
		blocks++
	}
	blocks, err := addInt64(blocks, storedBlockSlack)
	if err != nil {
		return 0, err
	}
	return addInt64(storedBlockHeaderBytes*blocks, zlibWrapperBytes)
}

// zlibBound returns an upper bound on the bytes the zlib representation of an
// n-byte payload occupies. Callers apply it to the FULL Git object — header
// included — because that is what a loose object actually compresses.
func zlibBound(n int64) (int64, error) {
	overhead, err := storedBlockOverhead(n)
	if err != nil {
		return 0, err
	}
	return addInt64(n, overhead)
}

// ---------------------------------------------------------------------------
// Canonical object construction
// ---------------------------------------------------------------------------

// gitObject is a raw Git object: its type, its content, and the canonical hash
// that names it.
type gitObject struct {
	// Type is "blob", "tree", or "commit".
	Type string
	// Content is the object body, without the header.
	Content []byte
	// Hash is the canonical Git object name of header+content.
	Hash string
}

// objectHeader renders the loose-object header of a body of the given type and
// length. The header is part of both the object's hash and its stored bytes,
// which is why it is built by one function rather than formatted at each use.
func objectHeader(objType string, contentLen int64) []byte {
	return []byte(objType + " " + strconv.FormatInt(contentLen, 10) + "\x00")
}

// objectID computes the canonical Git object hash of an object body: the SHA-1
// of "<type> <length>\0<content>".
//
// This is Git's own SHA-1 object format. The store keeps Git's encoding rather
// than replacing it, and the value computed here is required to agree with
// `git hash-object` before any object is installed.
func objectID(objType string, content []byte) (string, error) {
	h := sha1.New()
	if _, err := h.Write(objectHeader(objType, int64(len(content)))); err != nil {
		return "", storeError(ReasonInternal, err)
	}
	if _, err := h.Write(content); err != nil {
		return "", storeError(ReasonInternal, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// newGitObject hashes an object body in-process.
func newGitObject(objType string, content []byte) (gitObject, error) {
	hash, err := objectID(objType, content)
	if err != nil {
		return gitObject{}, err
	}
	return gitObject{Type: objType, Content: content, Hash: hash}, nil
}

// objectBytes returns the uncompressed loose-object bytes: header plus content.
func objectBytes(obj gitObject) []byte {
	header := objectHeader(obj.Type, int64(len(obj.Content)))
	out := make([]byte, 0, len(header)+len(obj.Content))
	out = append(out, header...)
	return append(out, obj.Content...)
}

// ---------------------------------------------------------------------------
// Budget
// ---------------------------------------------------------------------------

// objectBudget is a manager-owned allowance. Every allocation a capture makes
// is charged against it BEFORE the allocation happens, so the admitted figure
// bounds bytes on disk rather than predicting them.
//
// Spending is one-way. Nothing is ever refunded: a write that failed may
// already have consumed the bytes it spent, and a budget that refunds on
// failure is a budget that can be spent twice. The estimate that produced the
// allowance already assumed every object was new, so a deduplicated object
// simply does not spend — that is where deduplication is "counted".
type objectBudget struct {
	limits Limits
	// allowance is the number of bytes the operation was admitted for.
	allowance int64
	// spent is the charged total.
	spent int64
	// chargedPaths records directory entries already charged, so a repeated
	// MkdirAll on an existing directory is not charged twice.
	chargedPaths map[string]int64
}

// newObjectBudget returns a budget that may spend at most allowance bytes.
func newObjectBudget(limits Limits, allowance int64) (*objectBudget, error) {
	if allowance < 0 {
		return nil, storeErrorf(ReasonInvalidLimits, "capture allowance must not be negative: %d", allowance)
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &objectBudget{
		limits:       limits,
		allowance:    allowance,
		chargedPaths: make(map[string]int64),
	}, nil
}

// Allowance is the admitted write allowance.
func (b *objectBudget) Allowance() int64 { return b.allowance }

// Spent is the charged total.
func (b *objectBudget) Spent() int64 { return b.spent }

// Remaining is what the budget may still spend.
func (b *objectBudget) Remaining() int64 { return b.allowance - b.spent }

// Charge refuses unless the budget can pay units bytes, then charges them. The
// refusal happens before the caller acts on the answer, so a refused charge
// leaves nothing written.
func (b *objectBudget) Charge(what string, units int64, path string) error {
	if units < 0 {
		return storeErrorf(ReasonInvalidLimits, "cannot charge a negative amount for %s: %d", what, units)
	}
	next, err := addInt64(b.spent, units)
	if err != nil {
		return err
	}
	if next > b.allowance {
		return storeErrorf(ReasonBudgetExhausted, path,
			"%s needs %d bytes but only %d of the %d byte capture allowance remain",
			what, units, b.Remaining(), b.allowance)
	}
	b.spent = next
	return nil
}

// chargeUnitsFor returns the allocation units an n-byte file occupies, with a
// floor of one unit.
func (b *objectBudget) chargeUnitsFor(n int64) (int64, error) {
	if b.limits.Normalize().AllocationUnitBytes <= 0 {
		return DefaultLimits().RoundUp(n)
	}
	return b.limits.RoundUp(n)
}

// chargeDirOnce charges one directory entry the first time it is needed. A
// directory is an allocation whether or not it holds anything yet, so charging
// it is what stops "make a directory per object prefix" from being free.
func (b *objectBudget) chargeDirOnce(path string) error {
	if _, ok := b.chargedPaths[path]; ok {
		return nil
	}
	units, err := b.chargeUnitsFor(0)
	if err != nil {
		return err
	}
	if err := b.Charge("object directory", units, path); err != nil {
		return err
	}
	b.chargedPaths[path] = units
	return nil
}

// BudgetedWriter enforces a budget on an io.Writer: the charge happens before
// the write, and a write that cannot be paid for lands nothing. It is the only
// writer permitted to touch the store.
type BudgetedWriter struct {
	budget *objectBudget
	path   string
	// written counts the bytes actually written.
	written int64
	// failed records the first failure so a later call cannot appear to succeed
	// after one write was refused.
	failed error
}

// NewBudgetedWriter returns a writer bounded by the budget's remaining
// allowance. path names the destination for diagnostics.
func NewBudgetedWriter(budget *objectBudget, path string) (*BudgetedWriter, error) {
	if budget == nil {
		return nil, storeErrorf(ReasonInternal, "budgeted writer needs a budget")
	}
	return &BudgetedWriter{budget: budget, path: path}, nil
}

// Written is the number of bytes this writer has written.
func (w *BudgetedWriter) Written() int64 { return w.written }

// Write charges the byte count and then writes. A charge that would exceed the
// allowance fails and writes nothing.
func (w *BudgetedWriter) Write(p []byte) (int, error) {
	if w.failed != nil {
		return 0, w.failed
	}
	if err := w.budget.Charge("object bytes", int64(len(p)), w.path); err != nil {
		w.failed = err
		return 0, err
	}
	w.written += int64(len(p))
	return len(p), nil
}

var _ io.Writer = (*BudgetedWriter)(nil)

// cappedWriter is the second line of defence around one object: it refuses any
// byte beyond the reservation that object already charged. The budget bounds
// the total; this bounds each individual object, so a mistake in one object's
// reservation cannot be paid for out of another object's allowance.
type cappedWriter struct {
	dst   io.Writer
	limit int64
	n     int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	next, err := addInt64(c.n, int64(len(p)))
	if err != nil {
		return 0, err
	}
	if next > c.limit {
		return 0, storeErrorf(ReasonBudgetExhausted,
			"object encoding wrote %d bytes, over its %d byte reservation", next, c.limit)
	}
	n, err := c.dst.Write(p)
	c.n += int64(n)
	if err != nil {
		return n, err
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Blob sources
// ---------------------------------------------------------------------------

// blobSource supplies the content of one blob under the exact-read protocol.
//
// The length is fixed BEFORE any read, because a Git object is length-prefixed:
// the header declares how many bytes follow, and a header that describes
// different bytes than the payload is corruption, not a tolerable race. A
// source therefore reports its initial length once and then yields exactly that
// many bytes — no more, no fewer.
type blobSource interface {
	// Size is the initial content length, observed before reading.
	Size() int64
	// ReadChunk reads up to len(p) content bytes. io.EOF means the content
	// ended; ending before the declared size is truncation and aborts.
	ReadChunk(p []byte) (int, error)
	// Description names the source for diagnostics.
	Description() string
}

// finalizable is implemented by a source that can re-check the identity of what
// it read. A file that changed size, or was replaced, between the initial stat
// and the end of the read must not be recorded as if it had been stable.
type finalizable interface {
	Finalize() error
}

// fileBlobSource reads one regular file through an already-open, no-follow
// handle. It never re-opens by path, so a path swapped mid-read cannot redirect
// what is captured.
type fileBlobSource struct {
	f       *os.File
	path    string
	size    int64
	initial fileID
	// eofSeen records whether the underlying file reported EOF.
	eofSeen bool
	closed  bool
}

// newFileBlobSource opens a workspace path without following a final symlink
// and captures the identity observed at that moment.
func newFileBlobSource(path string) (*fileBlobSource, error) {
	f, id, err := openWorkspaceFile(path)
	if err != nil {
		return nil, err
	}
	return &fileBlobSource{f: f, path: path, size: id.size, initial: id}, nil
}

// Size is the initial length the Git blob header will declare.
func (s *fileBlobSource) Size() int64 { return s.size }

// Description names the file for diagnostics.
func (s *fileBlobSource) Description() string { return s.path }

// Close releases the handle.
func (s *fileBlobSource) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.f.Close()
}

// ReadChunk reads the next content bytes.
func (s *fileBlobSource) ReadChunk(p []byte) (int, error) {
	if s.eofSeen {
		return 0, io.EOF
	}
	n, err := s.f.Read(p)
	if errors.Is(err, io.EOF) {
		s.eofSeen = true
	}
	return n, err
}

// Finalize re-checks the file's identity and length after the read. A file that
// grew while it was being read still has a different size, so growth that the
// read itself absorbed — because the reader stopped at the declared length —
// is still an abort.
func (s *fileBlobSource) Finalize() error {
	id, err := fileIDFromHandle(s.f)
	if err != nil {
		return err
	}
	if !id.sameContentAs(s.initial) {
		return storeErrorf(ReasonUnreadableFile, s.path,
			"file changed while it was being captured (size %d then %d)", s.initial.size, id.size)
	}
	return nil
}

// bytesBlobSource supplies content already in memory. It is how a symbolic
// link's target text becomes a blob: the link is read once, and the link itself
// is never followed.
type bytesBlobSource struct {
	desc string
	data []byte
	pos  int
}

func (s *bytesBlobSource) Size() int64         { return int64(len(s.data)) }
func (s *bytesBlobSource) Description() string { return s.desc }

func (s *bytesBlobSource) ReadChunk(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += n
	return n, nil
}

// ---------------------------------------------------------------------------
// Writer
// ---------------------------------------------------------------------------

// objectWriter installs loose objects into one generation repository under a
// fixed allowance.
type objectWriter struct {
	manager    *Manager
	ctx        context.Context
	gitDir     string
	objectsDir string
	budget     *objectBudget

	// gitHashObjects computes the canonical hash Git gives each file in paths,
	// read as an object of the given type, in order. It is a field so a test can
	// inject a disagreement and prove that a mismatch aborts instead of
	// installing an object, and count how many Git processes a write costs.
	gitHashObjects func(ctx context.Context, objType string, paths []string) ([]string, error)

	// temps tracks partially written object files so a failed capture removes
	// them instead of leaving unaccounted bytes behind.
	temps []string

	// deferGit batches Git agreement. A new object is fully written and checked
	// in process, then queued in pending instead of being verified by Git and
	// published one at a time; flushPending verifies the queue with one Git
	// process per object type and bounded chunk, and only then publishes. A
	// capture of thousands of new objects otherwise starts thousands of Git
	// processes, which dominated capture time. Off by default, so a caller that
	// expects every write to be published on return keeps that behaviour.
	deferGit      bool
	pending       []pendingObject
	pendingByHash map[string]bool

	// In deferGit mode durability is batched too: queued temps and object
	// directories get a writeout-only sync, and flushPending issues one full
	// barrier after publishing them and before reporting them durable (see
	// sync_darwin.go). Fields so a test can count and order them.
	writeout    func(*os.File) error
	writeoutDir func(string) error
	barrier     func(string) error

	hashVerified int
	deduped      int
}

// pendingObject is a written, in-process-verified object awaiting Git
// agreement and publication.
type pendingObject struct {
	tmp        string
	dst        string
	objType    string
	header     []byte
	contentLen int64
	hash       string
}

const (
	// maxBufferedBlobBytes bounds the blobs read into memory so their hash is
	// known BEFORE anything is written. That is what lets an unchanged file —
	// the overwhelming majority in a repeat capture — be recognised as an
	// existing object without writing, syncing, or verifying a temp copy. A
	// larger blob takes the streaming path, which hashes while it writes.
	maxBufferedBlobBytes = 8 << 20
	// verifyBatchMaxObjects and verifyBatchMaxBytes bound one Git agreement
	// batch: the scratch copies it needs on disk and the work one Git process
	// does inside its command deadline.
	verifyBatchMaxObjects = 256
	verifyBatchMaxBytes   = 64 << 20
)

// newObjectWriter builds a writer for one generation repository.
func newObjectWriter(ctx context.Context, m *Manager, gitDir string, budget *objectBudget) (*objectWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, interruptedCapture(err)
	}
	if m == nil {
		return nil, storeErrorf(ReasonInternal, "object writer needs a manager")
	}
	if budget == nil {
		return nil, storeErrorf(ReasonInternal, "object writer needs a budget")
	}
	if !gitDirExists(gitDir) {
		return nil, m.storeErrorf(ReasonUnreadableFile, gitDir, "generation repository is not initialised")
	}
	w := &objectWriter{
		manager:    m,
		ctx:        ctx,
		gitDir:     gitDir,
		objectsDir: filepath.Join(gitDir, "objects"),
		budget:     budget,
	}
	w.gitHashObjects = w.hashObjectsViaGit
	w.writeout = writeoutSync
	w.writeoutDir = writeoutSyncDir
	w.barrier = fullBarrier
	return w, nil
}

// knownObject reports whether an object with this hash already exists, either
// installed in the store or queued in this writer. An installed object counts
// only after it has been VERIFIED to hold exactly these bytes: the name is
// derived from the content, so a hit must be proven rather than assumed. A
// present-but-different object is corruption and is returned as an error.
func (w *objectWriter) knownObject(hash, dst string, header []byte, contentLen int64) (bool, error) {
	if w.pendingByHash[hash] {
		w.deduped++
		return true, nil
	}
	existing, err := storedObjectMatches(dst, hash, header, contentLen)
	if err != nil {
		return false, err
	}
	if existing {
		w.deduped++
	}
	return existing, nil
}

// queue records a written object for the next flushPending.
func (w *objectWriter) queue(obj pendingObject) {
	if w.pendingByHash == nil {
		w.pendingByHash = make(map[string]bool)
	}
	w.pending = append(w.pending, obj)
	w.pendingByHash[obj.hash] = true
}

// Stats reports how many objects Git confirmed and how many already existed.
func (w *objectWriter) Stats() (verified, deduped int) { return w.hashVerified, w.deduped }

// cleanup removes every partially written object file. A failure must not leave
// more than the admitted allowance behind, and an unaccounted temp file is
// exactly that.
func (w *objectWriter) cleanup() {
	for _, name := range w.temps {
		_ = os.Remove(name)
	}
	w.temps = nil
}

// objectPath returns the loose-object path for a hash, validating the hash
// first so no unvalidated text can steer a write.
func (w *objectWriter) objectPath(hash string) (string, error) {
	if !validObjectHash(hash) {
		return "", w.manager.storeErrorf(ReasonInternal, w.gitDir,
			"object hash %q is not a hexadecimal object id", hash)
	}
	hash = strings.ToLower(hash)
	return filepath.Join(w.objectsDir, hash[:2], hash[2:]), nil
}

// newTemp creates a partially written object file inside the store, charging
// its directory entry first.
func (w *objectWriter) newTemp() (*os.File, error) {
	incoming := filepath.Join(w.objectsDir, incomingDirName)
	if err := w.budget.chargeDirOnce(incoming); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(incoming, 0o755); err != nil {
		return nil, w.manager.storeError(ReasonUnreadableFile, incoming, err)
	}
	f, err := os.CreateTemp(incoming, objectTempPrefix+"*")
	if err != nil {
		return nil, w.manager.storeError(ReasonUnreadableFile, incoming, err)
	}
	w.temps = append(w.temps, f.Name())
	return f, nil
}

// forgetTemp stops tracking a temp name once it has been installed or removed.
func (w *objectWriter) forgetTemp(name string) {
	for i, t := range w.temps {
		if t == name {
			w.temps = append(w.temps[:i], w.temps[i+1:]...)
			return
		}
	}
}

// installObject writes an in-memory object (a tree or a commit), verifying its
// declared header, its content length, and Git's own hash of the stored bytes
// before it is given its name.
//
// An object that already exists is used only after it has been VERIFIED to hold
// exactly these bytes. Nothing is refunded for it: the reservation that made
// the capture admissible assumed every object was new, and a verified existing
// object simply spends nothing.
func (w *objectWriter) installObject(obj gitObject) (bool, error) {
	if err := w.ctx.Err(); err != nil {
		return false, interruptedCapture(err)
	}
	if len(obj.Content) == 0 {
		// Only an empty tree is legal, and a capture never produces one: a
		// directory with no capturable content is not reported by Git at all.
		// Accepting an empty object would let a builder bug look like a valid
		// snapshot.
		return false, w.manager.storeErrorf(ReasonInternal, w.gitDir,
			"refusing to install an empty %s object", obj.Type)
	}
	dst, err := w.objectPath(obj.Hash)
	if err != nil {
		return false, err
	}
	header := objectHeader(obj.Type, int64(len(obj.Content)))
	raw := objectBytes(obj)

	// Identical subtrees inside one capture share a name; the queued copy will
	// be verified and published like any other.
	if w.pendingByHash[obj.Hash] {
		w.deduped++
		return true, nil
	}
	existing, err := verifyExistingObject(dst, raw)
	if err != nil {
		return false, err
	}
	if existing {
		w.deduped++
		return true, nil
	}

	reservation, err := w.reserveEncoded(raw, dst)
	if err != nil {
		return false, err
	}
	if err := w.budget.chargeDirOnce(filepath.Dir(dst)); err != nil {
		return false, err
	}

	installed, err := w.writeLooseObject(raw, reservation, dst)
	if err != nil {
		return false, err
	}
	if err := w.verifyStoredObject(installed, header, int64(len(obj.Content))); err != nil {
		_ = os.Remove(installed)
		return false, err
	}
	pending := pendingObject{
		tmp: installed, dst: dst, objType: obj.Type,
		header: header, contentLen: int64(len(obj.Content)), hash: obj.Hash,
	}
	if w.deferGit {
		w.queue(pending)
		return false, nil
	}
	if err := w.verifyWithGit(obj.Type, []pendingObject{pending}); err != nil {
		_ = os.Remove(installed)
		return false, err
	}
	if err := w.publishObject(installed, dst); err != nil {
		return false, err
	}
	return false, nil
}

// writeBlob streams one blob through the exact-read protocol and installs it.
//
// The protocol: fix the length, emit the header for exactly that length, read
// exactly that many bytes, probe for one more, and re-check the identity.
// Growth, truncation, an identity change, and an unsupported type each abort —
// growth even when it would still fit under the per-file size cap, because the
// cap is not the point: a snapshot that records different bytes than it hashed
// claims a rollback point it does not have.
//
// A blob small enough to buffer is read and hashed FIRST. If that object is
// already installed (and verified to hold these bytes) or queued, nothing is
// written: no temp copy, no sync, no Git process. The buffered bytes are what a
// new object is then written from, so the source is read exactly once either
// way and every source check above still applies to that one read.
func (w *objectWriter) writeBlob(src blobSource) (string, error) {
	if err := w.ctx.Err(); err != nil {
		return "", interruptedCapture(err)
	}
	size := src.Size()
	if size < 0 {
		return "", w.manager.storeErrorf(ReasonUnreadableFile, src.Description(),
			"negative blob length %d", size)
	}
	header := objectHeader(objTypeBlob, size)
	if size <= maxBufferedBlobBytes {
		data, hash, err := w.readBlob(src, header)
		if err != nil {
			return "", err
		}
		dst, err := w.objectPath(hash)
		if err != nil {
			return "", err
		}
		known, err := w.knownObject(hash, dst, header, size)
		if err != nil {
			return "", err
		}
		if known {
			return hash, nil
		}
		src = &bytesBlobSource{desc: src.Description(), data: data}
	}
	rawLen, err := addInt64(int64(len(header)), size)
	if err != nil {
		return "", err
	}
	// The object's name is unknown until its content has been read, so the
	// reservation is made against this blob's maximum encoded size and the
	// content-addressed directory is charged once the hash is known.
	reservation, err := w.reserveBlobEncoded(rawLen, src.Description())
	if err != nil {
		return "", err
	}

	hasher := sha1.New()
	if _, err := hasher.Write(header); err != nil {
		return "", storeError(ReasonInternal, err)
	}
	installed, err := w.writeBlobStream(src, header, hasher, reservation)
	if err != nil {
		return "", err
	}

	hash := hex.EncodeToString(hasher.Sum(nil))
	dst, err := w.objectPath(hash)
	if err != nil {
		_ = os.Remove(installed)
		return "", err
	}
	if err := w.budget.chargeDirOnce(filepath.Dir(dst)); err != nil {
		_ = os.Remove(installed)
		return "", err
	}
	if err := w.verifyStoredObject(installed, header, size); err != nil {
		_ = os.Remove(installed)
		return "", err
	}
	// Checked again after the write: a streamed blob's name was unknown until
	// now, and another writer may have installed this object meanwhile.
	existing, err := w.knownObject(hash, dst, header, size)
	if err != nil {
		_ = os.Remove(installed)
		return "", err
	}
	if existing {
		_ = os.Remove(installed)
		w.forgetTemp(installed)
		return hash, nil
	}
	pending := pendingObject{
		tmp: installed, dst: dst, objType: objTypeBlob,
		header: header, contentLen: size, hash: hash,
	}
	if w.deferGit {
		w.queue(pending)
		return hash, nil
	}
	if err := w.verifyWithGit(objTypeBlob, []pendingObject{pending}); err != nil {
		_ = os.Remove(installed)
		return "", err
	}
	if err := w.publishObject(installed, dst); err != nil {
		return "", err
	}
	return hash, nil
}

// readBlob reads a source's declared length into memory through the same
// exact-read protocol the streaming path uses — short reads, growth past the
// declared length, and an identity change each abort — and returns the bytes
// and their object hash.
func (w *objectWriter) readBlob(src blobSource, header []byte) ([]byte, string, error) {
	declared := src.Size()
	data := make([]byte, declared)
	var read int64
	for read < declared {
		if err := w.ctx.Err(); err != nil {
			return nil, "", interruptedCapture(err)
		}
		chunk := data[read:min64(declared, read+64*1024)]
		n, err := src.ReadChunk(chunk)
		if n > 0 {
			read += int64(n)
			continue
		}
		if err := w.shortRead(src, read, declared, err); err != nil {
			return nil, "", err
		}
	}
	if err := w.probeEOF(src, declared); err != nil {
		return nil, "", err
	}
	if fin, ok := src.(finalizable); ok {
		if err := fin.Finalize(); err != nil {
			return nil, "", err
		}
	}
	hasher := sha1.New()
	if _, err := hasher.Write(header); err != nil {
		return nil, "", storeError(ReasonInternal, err)
	}
	if _, err := hasher.Write(data); err != nil {
		return nil, "", storeError(ReasonInternal, err)
	}
	return data, hex.EncodeToString(hasher.Sum(nil)), nil
}

// writeBlobStream streams the declared-length content through the hasher and
// the encoder into a temp object, returning the temp path.
func (w *objectWriter) writeBlobStream(src blobSource, header []byte, hasher io.Writer, reservation int64) (string, error) {
	tmp, err := w.newTemp()
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		w.forgetTemp(tmpName)
	}

	capped := &cappedWriter{dst: tmp, limit: reservation}
	zw, err := zlib.NewWriterLevel(capped, flate.NoCompression)
	if err != nil {
		discard()
		return "", w.manager.storeError(ReasonUnreadableFile, tmpName, err)
	}
	if _, err := zw.Write(header); err != nil {
		discard()
		return "", storeError(ReasonBudgetExhausted, err)
	}

	declared := src.Size()
	buf := make([]byte, 64*1024)
	var read int64
	for read < declared {
		if err := w.ctx.Err(); err != nil {
			discard()
			return "", interruptedCapture(err)
		}
		chunk := buf
		if want := declared - read; want < int64(len(buf)) {
			chunk = buf[:want]
		}
		n, err := src.ReadChunk(chunk)
		if n > 0 {
			if _, werr := hasher.Write(chunk[:n]); werr != nil {
				discard()
				return "", storeError(ReasonInternal, werr)
			}
			if _, werr := zw.Write(chunk[:n]); werr != nil {
				discard()
				return "", storeError(ReasonBudgetExhausted, werr)
			}
			read += int64(n)
			continue
		}
		if err := w.shortRead(src, read, declared, err); err != nil {
			discard()
			return "", err
		}
	}

	// The EOF probe, then an optional identity re-check. A source that yields a
	// byte beyond its declared length would make the header lie about the
	// content, so it aborts even when the extra bytes are small.
	if err := w.probeEOF(src, declared); err != nil {
		discard()
		return "", err
	}
	if fin, ok := src.(finalizable); ok {
		if err := fin.Finalize(); err != nil {
			discard()
			return "", err
		}
	}
	if err := zw.Close(); err != nil {
		discard()
		return "", w.manager.storeError(ReasonUnreadableFile, tmpName, err)
	}
	written, err := w.flushTemp(tmp)
	if err != nil {
		discard()
		return "", err
	}
	if written > reservation {
		discard()
		return "", w.manager.storeErrorf(ReasonUnverifiedObject, tmpName,
			"encoded object is %d bytes, over its %d byte reservation", written, reservation)
	}
	return tmpName, nil
}

// shortRead turns "the source stopped early" into a structured error.
func (w *objectWriter) shortRead(src blobSource, read, declared int64, err error) error {
	if err == nil {
		return w.manager.storeErrorf(ReasonUnreadableFile, src.Description(),
			"reading %s made no progress after %d of %d bytes", src.Description(), read, declared)
	}
	if errors.Is(err, io.EOF) {
		return w.manager.storeErrorf(ReasonUnreadableFile, src.Description(),
			"%s ended after %d bytes but its Git header declares %d", src.Description(), read, declared)
	}
	return w.manager.storeError(ReasonUnreadableFile, src.Description(), err)
}

// probeEOF proves the source yields nothing beyond its declared length.
func (w *objectWriter) probeEOF(src blobSource, declared int64) error {
	probe := make([]byte, 1)
	for attempt := 0; attempt < 4; attempt++ {
		n, err := src.ReadChunk(probe)
		if n > 0 {
			return w.manager.storeErrorf(ReasonUnreadableFile, src.Description(),
				"%s grew while it was being captured: its Git header declares %d bytes",
				src.Description(), declared)
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		return w.manager.storeError(ReasonUnreadableFile, src.Description(), err)
	}
	// A source that neither produces a byte nor reports EOF within the probe
	// budget cannot be proven to have ended, so it is not captured.
	return w.manager.storeErrorf(ReasonUnreadableFile, src.Description(),
		"%s did not report end of content after its declared %d bytes", src.Description(), declared)
}

// reserveEncoded charges an in-memory object's encoded-size bound, returning
// the reservation in bytes. The reservation is what the capped writer enforces.
func (w *objectWriter) reserveEncoded(raw []byte, path string) (int64, error) {
	return w.chargeReservation(int64(len(raw)), path)
}

// reserveBlobEncoded charges a streamed blob's encoded-size bound.
func (w *objectWriter) reserveBlobEncoded(rawLen int64, path string) (int64, error) {
	return w.chargeReservation(rawLen, path)
}

// chargeReservation charges the encoded-size bound of an object whose
// uncompressed length is rawLen, and returns the RESERVATION the per-object cap
// enforces: the zlib bound rounded up to whole allocation units.
//
// The rounding is part of the reservation, not a detail of the charge. A
// reservation that bounded only the encoded bytes would be smaller than the
// allocation the filesystem actually charges for them, so an object could
// occupy more space than the allowance reserved for it — which is exactly the
// overrun this store exists to prevent.
func (w *objectWriter) chargeReservation(rawLen int64, path string) (int64, error) {
	bound, err := zlibBound(rawLen)
	if err != nil {
		return 0, err
	}
	units, err := w.budget.chargeUnitsFor(bound)
	if err != nil {
		return 0, err
	}
	if err := w.budget.Charge("object "+path, units, path); err != nil {
		return 0, err
	}
	return units, nil
}

// writeLooseObject writes raw through a stored-block zlib encoder into a temp
// object, bounded by the reservation, and flushes it durably.
func (w *objectWriter) writeLooseObject(raw []byte, reservation int64, dst string) (string, error) {
	tmp, err := w.newTemp()
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	capped := &cappedWriter{dst: tmp, limit: reservation}
	zw, err := zlib.NewWriterLevel(capped, flate.NoCompression)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		w.forgetTemp(tmpName)
		return "", w.manager.storeError(ReasonUnreadableFile, tmpName, err)
	}
	if _, err := zw.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		w.forgetTemp(tmpName)
		return "", storeError(ReasonBudgetExhausted, err)
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		w.forgetTemp(tmpName)
		return "", w.manager.storeError(ReasonUnreadableFile, tmpName, err)
	}
	written, err := w.flushTemp(tmp)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		w.forgetTemp(tmpName)
		return "", err
	}
	if written > reservation {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		w.forgetTemp(tmpName)
		return "", w.manager.storeErrorf(ReasonUnverifiedObject, tmpName,
			"encoded object is %d bytes, over its %d byte reservation", written, reservation)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		w.forgetTemp(tmpName)
		return "", w.manager.storeError(ReasonUnreadableFile, tmpName, err)
	}
	return tmpName, nil
}

// flushTemp flushes a partially written object and returns its size. A queued
// object (deferGit) gets a writeout-only sync; flushPending's barrier makes it
// durable before anything relies on it. Otherwise the sync is full.
func (w *objectWriter) flushTemp(f *os.File) (int64, error) {
	sync := f.Sync
	if w.deferGit {
		sync = func() error { return w.writeout(f) }
	}
	if err := sync(); err != nil {
		return 0, storeError(ReasonUnreadableFile, err)
	}
	return f.Seek(0, io.SeekEnd)
}

// publishObject renames a verified temp object into its content-addressed path
// and flushes the directory so the name survives a crash. Objects are always
// installed before the ref that retains them, so an interrupted capture is
// invisible rather than half-published.
func (w *objectWriter) publishObject(tmpName, dst string) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return w.manager.storeError(ReasonUnreadableFile, dir, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return w.manager.storeError(ReasonUnreadableFile, dst, err)
	}
	w.forgetTemp(tmpName)
	if err := syncDir(dir); err != nil {
		return w.manager.storeError(ReasonUnreadableFile, dir, err)
	}
	// Write boundary: exactly one loose object is now durable at its
	// content-addressed path, and it was charged before it was written.
	w.manager.observeWrite(WriteEvent{
		Kind:      "object",
		Path:      dst,
		Allowance: w.budget.Allowance(),
	})
	return nil
}

// ---------------------------------------------------------------------------
// Hash verification (Git agreement)
// ---------------------------------------------------------------------------

// hashObjectsViaGit asks one Git process for the canonical hash of each file in
// paths, read as an object of objType. The invocation is read-only — no -w — so
// Git cannot write the objects it hashes, and --no-filters plus the sanitized
// environment means a project's clean filter is never executed on the way in.
func (w *objectWriter) hashObjectsViaGit(ctx context.Context, objType string, paths []string) ([]string, error) {
	var stdin strings.Builder
	for _, p := range paths {
		// --stdin-paths is newline-delimited, so a path that contains one
		// would be read as two.
		if strings.ContainsAny(p, "\n\r") {
			return nil, w.manager.storeErrorf(ReasonInternal, p,
				"verification path contains a line break")
		}
		stdin.WriteString(p)
		stdin.WriteByte('\n')
	}
	// One hex object name and a newline per path; SHA-256 names are the longest.
	maxOut := len(paths)*65 + 1024
	out, _, truncated, err := w.manager.gitRunEnv(ctx, w.gitDir, "", strings.NewReader(stdin.String()), nil, maxOut,
		"hash-object", "-t", objType, "--no-filters", "--stdin-paths")
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, w.manager.storeErrorf(ReasonUnverifiedObject, w.gitDir,
			"git hash-object output exceeded %d bytes for %d objects", maxOut, len(paths))
	}
	lines := strings.Fields(string(out))
	hashes := make([]string, 0, len(lines))
	for _, line := range lines {
		hash := strings.ToLower(line)
		if !validObjectHash(hash) {
			return nil, w.manager.storeErrorf(ReasonUnverifiedObject, w.gitDir,
				"git hash-object returned an unusable object name %q", line)
		}
		hashes = append(hashes, hash)
	}
	return hashes, nil
}

// verifyWithGit requires Git's own hash of the STORED bytes of every object in
// objs, all of type objType, to agree with the hash this process computed for
// the object it intended to write. Because the stored bytes are decompressed on
// the way to Git, agreement proves that what is on disk is what was named — a
// length-prefixed object describing different bytes is corruption, and it is
// refused here rather than surviving as a snapshot nobody can restore.
//
// The decompressed content of each object goes to a scratch file outside the
// store (Git hashes files by path in a batch), and one Git process hashes the
// whole batch. Scratch files are removed before return and are never part of
// the store or its allowance.
func (w *objectWriter) verifyWithGit(objType string, objs []pendingObject) error {
	if len(objs) == 0 {
		return nil
	}
	scratch, err := os.MkdirTemp("", "marshal-snapshot-verify-")
	if err != nil {
		return storeErrorf(ReasonUnreadableFile, "create verification scratch directory: %v", err)
	}
	defer os.RemoveAll(scratch)

	paths := make([]string, len(objs))
	for i, obj := range objs {
		p := filepath.Join(scratch, strconv.Itoa(i))
		storedHash, n, err := extractStoredContent(obj.tmp, obj.header, obj.contentLen, p)
		if err != nil {
			return err
		}
		if n != obj.contentLen {
			return w.manager.storeErrorf(ReasonUnverifiedObject, obj.tmp,
				"object content is %d bytes, not the declared %d", n, obj.contentLen)
		}
		if storedHash != obj.hash {
			return w.manager.storeErrorf(ReasonUnverifiedObject, obj.tmp,
				"stored bytes hash to %s but the object was named %s", storedHash, obj.hash)
		}
		paths[i] = p
	}
	gitHashes, err := w.gitHashObjects(w.ctx, objType, paths)
	if err != nil {
		return err
	}
	if len(gitHashes) != len(objs) {
		return w.manager.storeErrorf(ReasonUnverifiedObject, w.gitDir,
			"git hashed %d objects but %d were submitted", len(gitHashes), len(objs))
	}
	for i, obj := range objs {
		if gitHashes[i] != obj.hash {
			return w.manager.storeErrorf(ReasonUnverifiedObject, obj.tmp,
				"git computed object %s for the stored bytes but this process hashed them as %s", gitHashes[i], obj.hash)
		}
	}
	w.hashVerified += len(objs)
	return nil
}

// extractStoredContent copies a stored object's content (after proving its
// header) to dst, returning the hash of the stored object and the content bytes
// copied.
func extractStoredContent(path string, header []byte, contentLen int64, dst string) (string, int64, error) {
	r, err := contentReader(path, header, contentLen)
	if err != nil {
		return "", 0, err
	}
	defer r.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, storeErrorf(ReasonUnreadableFile, "create verification copy %s: %v", dst, err)
	}
	h := sha1.New()
	if _, err := h.Write(header); err != nil {
		_ = out.Close()
		return "", 0, storeError(ReasonInternal, err)
	}
	n, copyErr := io.Copy(io.MultiWriter(out, h), r)
	closeErr := out.Close()
	if copyErr != nil {
		return "", 0, storeErrorf(ReasonUnreadableFile, "read object %s: %v", path, copyErr)
	}
	if closeErr != nil {
		return "", 0, storeErrorf(ReasonUnreadableFile, "write verification copy %s: %v", dst, closeErr)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// flushPending verifies every queued object with Git, in bounded batches per
// object type, and only when ALL agree publishes them, syncing each object
// directory once. A failure publishes nothing; the queued temps are removed by
// cleanup like any other partial write. It must run before anything that
// relies on the objects being durable (the snapshot ref).
func (w *objectWriter) flushPending() error {
	if len(w.pending) == 0 {
		return nil
	}
	// Verification below runs per known type; an object of any other type
	// would be published unverified, so it fails closed instead.
	for _, obj := range w.pending {
		switch obj.objType {
		case objTypeBlob, objTypeTree, objTypeCommit:
		default:
			return w.manager.storeErrorf(ReasonInternal, obj.tmp,
				"queued object has unsupported type %q", obj.objType)
		}
	}
	for _, objType := range []string{objTypeBlob, objTypeTree, objTypeCommit} {
		var batch []pendingObject
		var batchBytes int64
		flush := func() error {
			if err := w.ctx.Err(); err != nil {
				return interruptedCapture(err)
			}
			err := w.verifyWithGit(objType, batch)
			batch, batchBytes = nil, 0
			return err
		}
		for _, obj := range w.pending {
			if obj.objType != objType {
				continue
			}
			batch = append(batch, obj)
			batchBytes += obj.contentLen
			if len(batch) >= verifyBatchMaxObjects || batchBytes >= verifyBatchMaxBytes {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := flush(); err != nil {
			return err
		}
	}

	// Every object agrees. Publish in queue order (blobs, then the trees that
	// name them, then the commit) and make each directory durable once.
	dirs := make(map[string]bool)
	var dirOrder []string
	for _, obj := range w.pending {
		dir := filepath.Dir(obj.dst)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return w.manager.storeError(ReasonUnreadableFile, dir, err)
		}
		if err := os.Rename(obj.tmp, obj.dst); err != nil {
			return w.manager.storeError(ReasonUnreadableFile, obj.dst, err)
		}
		w.forgetTemp(obj.tmp)
		if !dirs[dir] {
			dirs[dir] = true
			dirOrder = append(dirOrder, dir)
		}
	}
	for _, dir := range dirOrder {
		if err := w.writeoutDir(dir); err != nil {
			return w.manager.storeError(ReasonUnreadableFile, dir, err)
		}
	}
	// One full flush makes every object and directory synced above durable.
	// It flushes the drive's cache as a whole, so any published object will do.
	last := w.pending[len(w.pending)-1].dst
	if err := w.barrier(last); err != nil {
		return w.manager.storeError(ReasonUnreadableFile, last, err)
	}
	// Write boundary: each loose object is now durable at its content-addressed
	// path, and each was charged before it was written.
	for _, obj := range w.pending {
		w.manager.observeWrite(WriteEvent{
			Kind:      "object",
			Path:      obj.dst,
			Allowance: w.budget.Allowance(),
		})
	}
	w.pending = nil
	w.pendingByHash = nil
	return nil
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// contentReader returns a reader over the content of a stored loose object,
// after proving the stored stream begins with the expected header. It yields
// exactly contentLen bytes, so a stored object holding a different length is
// refused by its caller rather than silently truncated.
func contentReader(path string, header []byte, contentLen int64) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, storeErrorf(ReasonUnreadableFile, "open object %s: %v", path, err)
	}
	zr, err := zlib.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, storeErrorf(ReasonUnreadableFile, "decompress object %s: %v", path, err)
	}
	got := make([]byte, len(header))
	if _, err := io.ReadFull(zr, got); err != nil {
		_ = zr.Close()
		_ = f.Close()
		return nil, storeErrorf(ReasonUnverifiedObject, path, "stored object has no readable header: %v", err)
	}
	if !bytes.Equal(got, header) {
		_ = zr.Close()
		_ = f.Close()
		return nil, storeErrorf(ReasonUnverifiedObject, path,
			"stored object header is %q but the object was written as %q", got, header)
	}
	return &readerCloser{r: io.LimitReader(zr, contentLen), closers: []io.Closer{zr, f}}, nil
}

// readerCloser closes a chain of closers with the reader.
type readerCloser struct {
	r       io.Reader
	closers []io.Closer
}

func (rc *readerCloser) Read(p []byte) (int, error) { return rc.r.Read(p) }

func (rc *readerCloser) Close() error {
	var errs []error
	for _, c := range rc.closers {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// verifyStoredObject proves a stored object decompresses to exactly the header
// followed by contentLen bytes: nothing shorter, nothing longer, and no
// trailing data.
func (w *objectWriter) verifyStoredObject(path string, header []byte, contentLen int64) error {
	got, err := decompressLooseObject(path)
	if err != nil {
		return err
	}
	want, err := addInt64(int64(len(header)), contentLen)
	if err != nil {
		return err
	}
	if int64(len(got)) != want {
		return w.manager.storeErrorf(ReasonUnverifiedObject, path,
			"stored object decompressed to %d bytes, not the declared %d", len(got), want)
	}
	if !bytes.HasPrefix(got, header) {
		return w.manager.storeErrorf(ReasonUnverifiedObject, path,
			"stored object does not begin with its declared header %q", header)
	}
	return nil
}

// storedObjectMatches reports whether the object at path is exactly the object
// named wantHash.
//
// It streams the stored bytes rather than materialising them, so a large blob is
// verified without being held in memory. An absent file is not an error (there
// is simply nothing to deduplicate against). A file that is present but holds
// anything else IS an error: the name is derived from the content, so a mismatch
// is corruption or a hash collision, and treating it as a hit — or as a hole to
// be filled — would silently change what a snapshot that already referenced it
// can restore.
func storedObjectMatches(path, wantHash string, header []byte, contentLen int64) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, storeErrorf(ReasonUnreadableFile, "stat existing object %s: %v", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return false, storeErrorf(ReasonUnverifiedObject,
			"path %s exists but is not a readable object file", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return false, storeErrorf(ReasonUnreadableFile, "open object %s: %v", path, err)
	}
	defer f.Close()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return false, storeErrorf(ReasonUnverifiedObject,
			"an object exists at %s but is not a readable zlib stream: %v", path, err)
	}
	defer zr.Close()

	got := make([]byte, len(header))
	if _, err := io.ReadFull(zr, got); err != nil {
		return false, storeErrorf(ReasonUnverifiedObject,
			"an object exists at %s but has no readable header: %v", path, err)
	}
	if !bytes.Equal(got, header) {
		return false, storeErrorf(ReasonUnverifiedObject,
			"an object exists at %s with header %q, not the expected %q", path, got, header)
	}
	hasher := sha1.New()
	if _, err := hasher.Write(header); err != nil {
		return false, storeError(ReasonInternal, err)
	}
	counter := &countingReader{r: io.LimitReader(zr, contentLen)}
	if _, err := io.Copy(hasher, counter); err != nil {
		return false, storeErrorf(ReasonUnreadableFile, "read object %s: %v", path, err)
	}
	if counter.n != contentLen {
		return false, storeErrorf(ReasonUnverifiedObject,
			"an object exists at %s holding %d content bytes, not the expected %d", path, counter.n, contentLen)
	}
	// Trailing data means the stored object describes more than its header
	// declares, which is the corruption this check exists to catch.
	probe := make([]byte, 1)
	n, probeErr := zr.Read(probe)
	if n > 0 {
		return false, storeErrorf(ReasonUnverifiedObject,
			"an object exists at %s with trailing data after its %d declared bytes", path, contentLen)
	}
	if probeErr != nil && !errors.Is(probeErr, io.EOF) {
		return false, storeErrorf(ReasonUnreadableFile, "read object %s: %v", path, probeErr)
	}
	if hex.EncodeToString(hasher.Sum(nil)) != strings.ToLower(wantHash) {
		return false, storeErrorf(ReasonUnverifiedObject,
			"an object exists at %s but does not hash to its own name %s", path, wantHash)
	}
	return true, nil
}

// decompressLooseObject returns the full uncompressed object stored at path.
func decompressLooseObject(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, storeErrorf(ReasonUnreadableFile, "open object %s: %v", path, err)
	}
	defer f.Close()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return nil, storeErrorf(ReasonUnreadableFile, "decompress object %s: %v", path, err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		return nil, storeErrorf(ReasonUnreadableFile, "read object %s: %v", path, err)
	}
	return out, nil
}

// verifyExistingObject reports whether path already holds exactly raw. A file
// that exists but holds different bytes is NOT a deduplicated object: it is
// corruption or a collision, and treating it as a hit would install a snapshot
// whose objects do not contain what the snapshot claims. Because the path is
// derived from the object's own hash, a mismatch here is a hash collision and
// is reported rather than silently overwritten.
func verifyExistingObject(path string, raw []byte) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, storeErrorf(ReasonUnreadableFile,
			"stat existing object %s: %v", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return false, nil
	}
	got, err := decompressLooseObject(path)
	if err != nil {
		// An object that exists but cannot be decoded is corruption at a
		// content-addressed path. Rewriting it would silently change what any
		// snapshot that already referenced it can restore, so it is reported.
		return false, storeErrorf(ReasonUnverifiedObject,
			"an object exists at %s but is not a readable zlib stream: %v", path, err)
	}
	if !bytes.Equal(got, raw) {
		return false, storeErrorf(ReasonUnverifiedObject,
			"an object already exists at %s but holds different bytes", path)
	}
	return true, nil
}

// min64 returns the smaller of two int64 values.
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
