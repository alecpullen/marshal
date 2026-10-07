package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The versioned layout this file describes is:
//
//	<root>/v2/<workspace-hash>/manifest.json
//	<root>/v2/<workspace-hash>/generations/<generation-id>/   (bare Git repo)
//	<root>/v2/<workspace-hash>/staging/<staging-id>/          (unpublished scratch)
//
// Legacy stores live at <root>/<workspace-hash>/ exactly as the shadow-repo
// code has always written them. Nothing here reads their refs for mutation or
// writes into them at all: a legacy store is discovered, reported, and left
// byte-for-byte alone. That is deliberate — old Marshal binaries do not honour
// this layout, so a mutation could race a writer this process cannot see.
const (
	// LayoutVersion identifies the manifest schema. A manifest carrying a
	// different version is refused rather than interpreted: acting on a layout
	// this build does not understand is how reconciliation deletes live data.
	LayoutVersion = 1

	v2DirName          = "v2"
	generationsDirName = "generations"
	stagingDirName     = "staging"
	manifestFileName   = "manifest.json"
	// manifestTempPrefix marks the scratch file an atomic manifest replace
	// writes before renaming it into place. A leftover with this prefix can
	// only be a replace that never completed, which is what makes it a known
	// stage artifact rather than a file of unknown provenance.
	manifestTempPrefix = ".tmp-manifest-"

	// ManifestMaxBytes bounds one serialized manifest. The bound exists so a
	// manifest cannot grow without limit as generations accumulate: an
	// unbounded manifest would consume the very budget it is meant to protect,
	// and would have to be fully read into memory to be parsed.
	ManifestMaxBytes int64 = 2 << 20 // 2 MiB

	// MaxGenerationsPerWorkspace bounds how many generation records one
	// manifest may carry, for the same reason.
	MaxGenerationsPerWorkspace = 512

	// generationIDSeqWidth is the fixed decimal width of a generation ID's
	// creation sequence. 20 digits covers every uint64 exactly, so lexical
	// order and creation order always agree — the property rotation and
	// oldest-first reclamation depend on.
	generationIDSeqWidth = 20
	// generationIDSuffixLen is the hex length of a generation ID's random
	// suffix, which keeps two generations created in the same nanosecond (or
	// after a clock jump) distinct.
	generationIDSuffixLen = 8
	// stagingIDRandomLen is the hex length of a staging identifier.
	stagingIDRandomLen = 16
	stagingIDPrefix    = "stage-"
)

// snapshotRefPrefix is the ONLY ref namespace that retains snapshot objects.
// Generation repositories are created with no branch and no reflog, so an
// object is kept alive exactly when some ref below this prefix names it.
const snapshotRefPrefix = "refs/snapshots/"

// GenerationState is the lifecycle state of one generation.
//
// The states are ordered by what they permit: a generation may only be deleted
// after the intent is durable as GenerationDeleting, and an interrupted
// deletion is resumed rather than reinterpreted. A half-removed Git directory
// cannot be trusted to contain what its refs claim, so "deleting" is never
// usable and never becomes usable again.
type GenerationState string

const (
	// GenerationCreating is a generation whose record exists but whose
	// repository may not be initialised yet. It has never published a ref.
	GenerationCreating GenerationState = "creating"
	// GenerationActive is the one generation a workspace captures into.
	GenerationActive GenerationState = "active"
	// GenerationSealed is a finished generation retained for rollback only.
	GenerationSealed GenerationState = "sealed"
	// GenerationDeleting is a generation whose removal was decided and
	// persisted but may not have finished.
	GenerationDeleting GenerationState = "deleting"
)

// valid reports whether s is a state this build understands.
func (s GenerationState) valid() bool {
	switch s {
	case GenerationCreating, GenerationActive, GenerationSealed, GenerationDeleting:
		return true
	}
	return false
}

// Usable reports whether a generation in this state may serve a capture or a
// rollback. A generation being deleted is deliberately excluded: reusing one
// would resurrect storage that is already scheduled to disappear.
func (s GenerationState) Usable() bool {
	return s == GenerationActive || s == GenerationSealed
}

// GenerationOrigin records how a generation came to exist, which is what
// decides whether it may ever be reclaimed automatically.
type GenerationOrigin string

const (
	// OriginNative is a generation this Marshal created and captured into.
	OriginNative GenerationOrigin = "native"
	// OriginLegacy is a generation holding history migrated from a pre-v2
	// store. It is the only surviving copy of that rollback history.
	OriginLegacy GenerationOrigin = "legacy"
)

func (o GenerationOrigin) valid() bool { return o == OriginNative || o == OriginLegacy }

// GenerationRecord is one generation's durable metadata inside a manifest.
type GenerationRecord struct {
	// ID is the generation identifier, also the name of its directory.
	ID string `json:"id"`
	// State is the lifecycle state.
	State GenerationState `json:"state"`
	// Origin records whether the generation is native or migrated.
	Origin GenerationOrigin `json:"origin"`
	// Root is the workspace root this generation was created for. It is
	// recorded so a generation discovered after its checkout was deleted still
	// reports which workspace it belonged to.
	Root string `json:"root,omitempty"`
	// CreatedAt is when the generation record was written.
	CreatedAt time.Time `json:"created_at"`
	// LastPublishedAt is when a snapshot ref was last published into this
	// generation. It is the ordering key for oldest-first reclamation.
	LastPublishedAt time.Time `json:"last_published_at,omitzero"`
	// Protected marks a generation that no budget pressure may reclaim.
	// Use IsProtected rather than reading this field: a legacy-origin
	// generation is protected even when the flag was not persisted.
	Protected bool `json:"protected,omitempty"`
	// ProtectionReleased records that this generation's protection was
	// explicitly lifted by the history-discard confirmation, so that releasing
	// it is a durable, auditable event rather than a missing flag. It does not
	// itself authorise anything: the removal still goes through the usual
	// intent-first `deleting` sequence.
	ProtectionReleased bool `json:"protection_released,omitempty"`
}

// IsProtected reports whether the generation must never be automatically
// reclaimed. Legacy-origin generations are protected by definition: their
// objects are the only surviving copy of rollback history that predates bounded
// generations, so reclaiming one would silently discard history the user never
// agreed to lose.
//
// ProtectionReleased is the ONE thing that lifts that definition. It is written
// only by the history-discard confirmation, which requires a controlling
// terminal, the full offline acknowledgement, and the full generation ID — so a
// legacy-origin generation is protected until a human explicitly says otherwise,
// and the fact that they did is itself durable in the manifest.
func (r *GenerationRecord) IsProtected() bool {
	if r == nil {
		return false
	}
	if r.ProtectionReleased {
		return false
	}
	return r.Protected || r.Origin == OriginLegacy
}

// CaptureState records an in-flight capture so an interruption is recoverable.
// It holds both the staging identifier (what to remove if the capture never
// published) and the intended publication ref and hash (what to keep if the
// capture published but crashed before the manifest caught up).
type CaptureState struct {
	// StagingID names the scratch directory reserved for this capture.
	StagingID string `json:"staging_id"`
	// GenerationID is the generation the ref will be published into.
	GenerationID string `json:"generation_id"`
	// Ref is the publication ref. It is always derived from Hash, never
	// accepted as free text, so the two can never disagree.
	Ref string `json:"ref"`
	// Hash is the snapshot object the ref will name.
	Hash string `json:"hash"`
	// StartedAt is when the capture was admitted.
	StartedAt time.Time `json:"started_at"`
}

// Validate rejects a capture record that could not be reconciled safely.
//
// An empty Hash and Ref together mean "publication intent not recorded yet",
// which is the state between reserving staging and producing the first object.
// Setting only one of them is refused: a ref that does not correspond to its
// hash would make reconciliation look up the wrong ref and conclude that an
// unpublished capture had been published.
func (cs *CaptureState) Validate(mf *Manifest) error {
	if cs == nil {
		return nil
	}
	if !validStagingID(cs.StagingID) {
		return storeErrorf(ReasonInternal, "capture staging identifier %q is not a safe path element", cs.StagingID)
	}
	if !validGenerationID(cs.GenerationID) {
		return storeErrorf(ReasonInternal, "capture generation identifier %q is not a safe path element", cs.GenerationID)
	}
	if mf != nil && mf.Generation(cs.GenerationID) == nil {
		return storeErrorf(ReasonInternal, "capture names generation %q, which the manifest does not list", cs.GenerationID)
	}
	if cs.Hash == "" && cs.Ref == "" {
		return nil
	}
	if cs.Hash == "" || cs.Ref == "" {
		return storeErrorf(ReasonInternal,
			"capture records hash %q and ref %q; both or neither must be set", cs.Hash, cs.Ref)
	}
	want, err := snapshotRefFor(cs.Hash)
	if err != nil {
		return storeErrorf(ReasonInternal, "capture hash %q is not a hexadecimal object id", cs.Hash)
	}
	if cs.Ref != want {
		return storeErrorf(ReasonInternal, "capture ref %q does not match the ref for hash %q (%q)", cs.Ref, cs.Hash, want)
	}
	return nil
}

// SetPublicationIntent records the ref and hash a capture intends to publish.
// It exists so the intent is durable BEFORE the ref is written: a crash between
// writing the ref and updating the manifest must leave a record that says which
// ref to look for.
func (c *Catalog) SetPublicationIntent(stagingID, hash string) (*CaptureState, error) {
	if err := c.requireOwned(); err != nil {
		return nil, err
	}
	man, err := c.Load()
	if err != nil {
		return nil, err
	}
	if man == nil || man.Capture == nil {
		return nil, c.errf(ReasonInterruptedCapture, "workspace %s has no in-flight capture", c.workspace)
	}
	if man.Capture.StagingID != stagingID {
		return nil, c.errf(ReasonInterruptedCapture,
			"workspace %s has an in-flight capture for staging %q, not %q",
			c.workspace, man.Capture.StagingID, stagingID)
	}
	ref, err := snapshotRefFor(hash)
	if err != nil {
		return nil, c.err(ReasonInternal, err)
	}
	man.Capture.Hash = strings.ToLower(hash)
	man.Capture.Ref = ref
	if err := c.Save(man); err != nil {
		return nil, err
	}
	out := *man.Capture
	return &out, nil
}

// Manifest is the durable, per-workspace description of a versioned store. It
// is replaced atomically as a whole: there is no partial-update state a reader
// could interleave with.
type Manifest struct {
	// Version is the layout version; it must equal LayoutVersion.
	Version int `json:"version"`
	// Workspace is the workspace hash the catalog belongs to.
	Workspace string `json:"workspace"`
	// Root is the canonical workspace root. It may name a directory that no
	// longer exists: a deleted checkout must still be reported, not forgotten.
	Root string `json:"root,omitempty"`
	// ActiveID is the single generation captures go into, or "" when every
	// generation is sealed.
	ActiveID string `json:"active_id,omitzero"`
	// NextSeq is the creation sequence the next generation ID will use.
	NextSeq uint64 `json:"next_seq"`
	// Generations lists every generation, ordered by ID (creation order).
	Generations []GenerationRecord `json:"generations,omitempty"`
	// Capture records an in-flight capture, or is nil when none is running.
	Capture *CaptureState `json:"capture,omitempty"`
}

// Sequence returns the generation's creation sequence and whether the
// identifier carried one parseable. Oldest-first reclamation orders by this
// value rather than by the identifier string only so the ordering survives a
// change to the identifier's textual shape.
func (r *GenerationRecord) Sequence() (uint64, bool) {
	if r == nil {
		return 0, false
	}
	seq, _, ok := parseGenerationID(r.ID)
	return seq, ok
}

// Generation returns the record for id, or nil.
func (mf *Manifest) Generation(id string) *GenerationRecord {
	if mf == nil {
		return nil
	}
	for i := range mf.Generations {
		if mf.Generations[i].ID == id {
			return &mf.Generations[i]
		}
	}
	return nil
}

// Active returns the active generation's record, or nil.
func (mf *Manifest) Active() *GenerationRecord {
	if mf == nil || mf.ActiveID == "" {
		return nil
	}
	return mf.Generation(mf.ActiveID)
}

// IsProtected reports whether the named generation is protected from automatic
// reclamation. An unknown id is not protected.
func (mf *Manifest) IsProtected(id string) bool {
	return mf.Generation(id).IsProtected()
}

// IDs returns every generation identifier in creation order.
func (mf *Manifest) IDs() []string {
	if mf == nil {
		return nil
	}
	ids := make([]string, 0, len(mf.Generations))
	for i := range mf.Generations {
		ids = append(ids, mf.Generations[i].ID)
	}
	sort.Strings(ids)
	return ids
}

// Sealed returns every sealed generation record in creation order.
func (mf *Manifest) Sealed() []*GenerationRecord {
	if mf == nil {
		return nil
	}
	out := make([]*GenerationRecord, 0, len(mf.Generations))
	for i := range mf.Generations {
		if mf.Generations[i].State == GenerationSealed {
			out = append(out, &mf.Generations[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// removeGeneration drops the record for id and clears the active pointer if it
// named that generation.
func (mf *Manifest) removeGeneration(id string) {
	if mf == nil {
		return
	}
	kept := mf.Generations[:0]
	for i := range mf.Generations {
		if mf.Generations[i].ID != id {
			kept = append(kept, mf.Generations[i])
		}
	}
	mf.Generations = kept
	if mf.ActiveID == id {
		mf.ActiveID = ""
	}
}

// Validate rejects a manifest that cannot be acted on safely, so a corrupt
// catalog is quarantined instead of being guessed at.
func (mf *Manifest) Validate() error {
	if mf == nil {
		return storeErrorf(ReasonInternal, "manifest is nil")
	}
	if mf.Version != LayoutVersion {
		return storeErrorf(ReasonInternal, "manifest layout version %d is not supported by this build (%d)", mf.Version, LayoutVersion)
	}
	if !validWorkspaceID(mf.Workspace) {
		return storeErrorf(ReasonInternal, "manifest workspace identifier %q is not a hex workspace hash", mf.Workspace)
	}
	if mf.Root != "" && !filepath.IsAbs(mf.Root) {
		return storeErrorf(ReasonInternal, "manifest workspace root %q is not absolute", mf.Root)
	}
	if len(mf.Generations) > MaxGenerationsPerWorkspace {
		return storeErrorf(ReasonInternal, "manifest lists %d generations, over the %d bound", len(mf.Generations), MaxGenerationsPerWorkspace)
	}

	seen := make(map[string]bool, len(mf.Generations))
	active := 0
	for i := range mf.Generations {
		g := &mf.Generations[i]
		if !validGenerationID(g.ID) {
			return storeErrorf(ReasonInternal, "generation identifier %q is not a safe path element", g.ID)
		}
		if seen[g.ID] {
			return storeErrorf(ReasonInternal, "generation identifier %q is listed twice", g.ID)
		}
		seen[g.ID] = true
		if !g.State.valid() {
			return storeErrorf(ReasonInternal, "generation %s has unknown state %q", g.ID, g.State)
		}
		if !g.Origin.valid() {
			return storeErrorf(ReasonInternal, "generation %s has unknown origin %q", g.ID, g.Origin)
		}
		if g.State == GenerationActive {
			active++
		}
	}
	// Exactly one active generation is the invariant every capture depends on.
	// More than one would let two captures publish into the same workspace at
	// once; a mismatch with ActiveID would leave captures with no target.
	if active > 1 {
		return storeErrorf(ReasonInternal, "manifest lists %d active generations; a workspace has at most one", active)
	}
	if mf.ActiveID != "" {
		rec := mf.Generation(mf.ActiveID)
		if rec == nil {
			return storeErrorf(ReasonInternal, "manifest active generation %q is not listed", mf.ActiveID)
		}
		if rec.State != GenerationActive {
			return storeErrorf(ReasonInternal, "manifest active generation %q is in state %q", mf.ActiveID, rec.State)
		}
	} else if active != 0 {
		return storeErrorf(ReasonInternal, "manifest has an active generation but no active identifier")
	}
	return mf.Capture.Validate(mf)
}

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

// formatGenerationID builds a generation identifier: a fixed-width creation
// sequence followed by a random suffix. Sorting the identifiers as strings
// therefore reproduces creation order, which is what lets oldest-first
// reclamation be a plain sort. The identifier is derived from the store's own
// sequence counter and randomness only — never from the workspace filesystem
// path — so two workspaces can never share an identifier by construction.
func formatGenerationID(seq uint64, suffix string) string {
	return fmt.Sprintf("%0*d", generationIDSeqWidth, seq) + "-" + suffix
}

// parseGenerationID splits an identifier back into its parts.
func parseGenerationID(id string) (seq uint64, suffix string, ok bool) {
	if !validGenerationID(id) {
		return 0, "", false
	}
	seq, err := strconv.ParseUint(id[:generationIDSeqWidth], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return seq, id[generationIDSeqWidth+1:], true
}

// formatStagingID builds the identifier of one capture's scratch directory.
func formatStagingID(suffix string) string { return stagingIDPrefix + suffix }

// ---------------------------------------------------------------------------
// Path element validation
// ---------------------------------------------------------------------------

// validPathElement reports whether s is a single, safe path element. Every
// identifier that becomes a directory name is validated through this before any
// filesystem call, so a corrupt manifest cannot steer a delete or a create
// outside the store.
func validPathElement(s string) bool {
	switch s {
	case "", ".", "..":
		return false
	}
	if strings.ContainsAny(s, `/\`) || strings.ContainsRune(s, 0) {
		return false
	}
	// A trailing dot or space is ambiguous on Windows and a leading dot risks
	// colliding with the scratch-file prefix.
	return !strings.HasPrefix(s, ".") && !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, " ")
}

// validWorkspaceID reports whether s is a workspace hash as written by the
// legacy layout, so both layouts agree on which directory belongs to a
// workspace.
func validWorkspaceID(s string) bool {
	if !validPathElement(s) || len(s) != 12 {
		return false
	}
	return isHex(s)
}

func validGenerationID(s string) bool {
	if !validPathElement(s) || len(s) != generationIDSeqWidth+1+generationIDSuffixLen {
		return false
	}
	if s[generationIDSeqWidth] != '-' {
		return false
	}
	for i := 0; i < generationIDSeqWidth; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return isHex(s[generationIDSeqWidth+1:])
}

func validStagingID(s string) bool {
	if !validPathElement(s) || !strings.HasPrefix(s, stagingIDPrefix) ||
		len(s) != len(stagingIDPrefix)+stagingIDRandomLen {
		return false
	}
	return isHex(s[len(stagingIDPrefix):])
}

// validObjectHash reports whether s is a hexadecimal Git object id. Callers
// must validate before building a ref name: hash text is never allowed to
// become a Git revision expression.
func validObjectHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return isHex(s)
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// snapshotRefFor returns the publication ref for a snapshot hash.
func snapshotRefFor(hash string) (string, error) {
	if !validObjectHash(hash) {
		return "", storeErrorf(ReasonInternal, "snapshot hash %q is not a hexadecimal object id", hash)
	}
	// Git lower-cases hex object names; a hash that only differs by case would
	// otherwise name a second ref for the same object.
	return snapshotRefPrefix + strings.ToLower(hash), nil
}

// hashFromSnapshotRef returns the object hash a publication ref names.
func hashFromSnapshotRef(ref string) (string, bool) {
	if !strings.HasPrefix(ref, snapshotRefPrefix) {
		return "", false
	}
	hash := strings.TrimPrefix(ref, snapshotRefPrefix)
	if !validObjectHash(hash) {
		return "", false
	}
	return hash, true
}

// ---------------------------------------------------------------------------
// Catalog
// ---------------------------------------------------------------------------

// Catalog is the versioned store of exactly one workspace: its manifest, its
// generations, and the scratch space of an in-flight capture.
//
// A Catalog is a handle, not a lock. Every mutating method requires the store
// lock to be held (see Manager.Acquire), because reconciliation and deletion
// must never race a capture.
type Catalog struct {
	manager   *Manager
	workspace string
	root      string

	// WorkspaceRoot is the canonical workspace root this handle was built for,
	// or "" when the handle came from a bare workspace hash. It is recorded in
	// a new manifest so a workspace whose checkout is later deleted can still
	// be reported instead of silently forgotten.
	WorkspaceRoot string
}

// Catalog returns the catalog handle for a workspace hash. It performs no
// I/O: the catalog may not exist yet, and a handle for a workspace whose store
// was deleted must still be constructible so discovery can report it.
func (m *Manager) Catalog(workspace string) (*Catalog, error) {
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	if !validWorkspaceID(workspace) {
		return nil, storeErrorf(ReasonInternal, "workspace identifier %q is not a hex workspace hash", workspace)
	}
	return &Catalog{
		manager:   m,
		workspace: workspace,
		root:      filepath.Join(m.V2Root(), workspace),
	}, nil
}

// CatalogForRoot returns the catalog handle for a workspace root, hashing the
// root the same way the legacy shadow-repo layout does so both layouts agree
// on which directory belongs to which workspace.
func (m *Manager) CatalogForRoot(root string) (*Catalog, error) {
	canonical, err := canonicalWorkspaceRoot(root)
	if err != nil {
		return nil, err
	}
	c, err := m.Catalog(WorkspaceHashFor(canonical))
	if err != nil {
		return nil, err
	}
	c.WorkspaceRoot = canonical
	return c, nil
}

// V2Root is the versioned store root, <root>/v2.
func (m *Manager) V2Root() string { return filepath.Join(m.root, v2DirName) }

// Workspace is the workspace hash this catalog belongs to.
func (c *Catalog) Workspace() string { return c.workspace }

// Root is the catalog directory, <root>/v2/<workspace-hash>.
func (c *Catalog) Root() string { return c.root }

// ManifestPath is the catalog's manifest file.
func (c *Catalog) ManifestPath() string { return filepath.Join(c.root, manifestFileName) }

// GenerationsDir is the directory holding every generation repository.
func (c *Catalog) GenerationsDir() string { return filepath.Join(c.root, generationsDirName) }

// StagingDir is the directory holding every capture's scratch space.
func (c *Catalog) StagingDir() string { return filepath.Join(c.root, stagingDirName) }

// GenerationDir returns the repository directory of a generation, refusing any
// identifier that is not a single safe path element.
func (c *Catalog) GenerationDir(id string) (string, error) {
	if !validGenerationID(id) {
		return "", c.errf(ReasonInternal, "generation identifier %q is not a safe path element", id)
	}
	return c.containedPath(generationsDirName, id)
}

// StagingPath returns the scratch directory of one capture.
func (c *Catalog) StagingPath(id string) (string, error) {
	if !validStagingID(id) {
		return "", c.errf(ReasonInternal, "staging identifier %q is not a safe path element", id)
	}
	return c.containedPath(stagingDirName, id)
}

// containedPath joins validated elements onto the catalog directory and proves
// the result is still inside it. The check is redundant while every element is
// validated, and that redundancy is the point: it turns a future validation bug
// into a refused operation rather than a delete outside the store.
func (c *Catalog) containedPath(parts ...string) (string, error) {
	path := filepath.Join(append([]string{c.root}, parts...)...)
	rel, err := filepath.Rel(c.root, path)
	if err != nil {
		return "", c.err(ReasonSymlinkEscape, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", c.errf(ReasonSymlinkEscape, "path %q escapes the workspace catalog", path)
	}
	return path, nil
}

// requireOwned refuses a mutation unless this manager holds the store lock.
func (c *Catalog) requireOwned() error {
	if !c.manager.Owned() {
		return c.errf(ReasonNotOwned, "snapshot store is not owned by this manager")
	}
	return nil
}

// err decorates an error with the catalog's path and workspace.
func (c *Catalog) err(reason StoreReason, err error) *StoreError {
	se := storeError(reason, err)
	se.Path = c.root
	se.Workspace = c.workspace
	return se
}

// errf is err with a formatted cause.
func (c *Catalog) errf(reason StoreReason, format string, args ...any) *StoreError {
	return c.err(reason, fmt.Errorf(format, args...))
}

// Load reads and validates the manifest. An absent manifest returns
// (nil, nil): a catalog that has never been written is not an error, and
// callers must distinguish it from a manifest that is present but unreadable,
// which is quarantined rather than replaced.
func (c *Catalog) Load() (*Manifest, error) {
	data, err := os.ReadFile(c.ManifestPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, c.err(ReasonUnreadableFile, fmt.Errorf("read manifest: %w", err))
	}
	if bound := c.manager.manifestBound(); int64(len(data)) > bound {
		return nil, c.errf(ReasonUnreadableFile, "manifest is %d bytes, over the %d byte bound", len(data), bound)
	}
	var mf Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&mf); err != nil {
		return nil, c.err(ReasonUnreadableFile, fmt.Errorf("decode manifest: %w", err))
	}
	// Trailing data means the file is not what it claims to be. Refusing it
	// keeps a truncated or double-written manifest from being half-applied.
	if err := dec.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		return nil, c.errf(ReasonUnreadableFile, "manifest has trailing data after the document")
	}
	if err := mf.Validate(); err != nil {
		return nil, c.err(ReasonUnreadableFile, fmt.Errorf("invalid manifest: %w", err))
	}
	return &mf, nil
}

// Save replaces the manifest atomically: the bytes are written to a scratch
// file in the same directory, flushed, and renamed over the old manifest, and
// the directory is then flushed so the rename itself is durable. A reader
// therefore sees either the whole old manifest or the whole new one, which is
// what makes every state transition recoverable after a crash.
func (c *Catalog) Save(mf *Manifest) error {
	if err := c.requireOwned(); err != nil {
		return err
	}
	if mf == nil {
		return c.errf(ReasonInternal, "manifest is nil")
	}
	mf.Version = LayoutVersion
	mf.Workspace = c.workspace
	if err := mf.Validate(); err != nil {
		return c.err(ReasonInternal, fmt.Errorf("refusing to write an invalid manifest: %w", err))
	}
	data, err := marshalManifest(mf)
	if err != nil {
		return c.err(ReasonInternal, fmt.Errorf("encode manifest: %w", err))
	}
	if bound := c.manager.manifestBound(); int64(len(data)) > bound {
		return c.errf(ReasonBudgetExhausted, "manifest would be %d bytes, over the %d byte bound", len(data), bound)
	}
	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return c.err(ReasonUnreadableFile, fmt.Errorf("create workspace catalog: %w", err))
	}
	if err := c.manager.writeManifestAtomic(c.ManifestPath(), data); err != nil {
		return c.err(ReasonUnreadableFile, err)
	}
	return nil
}

// marshalManifest encodes a manifest. It is a single function so the size a
// caller reserves and the size Save actually writes cannot drift apart: a
// reservation computed from a different encoder would bound nothing.
func marshalManifest(mf *Manifest) ([]byte, error) {
	return json.MarshalIndent(mf, "", "  ")
}

// ManifestReplacementBytes is the worst-case reservation for replacing this
// catalog's manifest. Both files can exist at once, because an atomic replace
// creates the new file before the rename removes the old one, and each of them
// is bounded only by ManifestMaxBytes. Admission must reserve this before it
// permits a capture (enforcement lands with Task 5).
func (c *Catalog) ManifestReplacementBytes() (int64, error) {
	unit := c.manager.limits.Normalize().AllocationUnitBytes
	per, err := roundUpAllocation(c.manager.manifestBound(), unit)
	if err != nil {
		return 0, c.err(ReasonAccountingOverflow, err)
	}
	total, err := addInt64(per, per)
	if err != nil {
		return 0, c.err(ReasonAccountingOverflow, err)
	}
	return total, nil
}

// ensureManifest loads the manifest, creating an empty one for this catalog's
// root on first use. An unreadable manifest is never overwritten: the error
// propagates so the caller quarantines the catalog instead of replacing state
// it could not interpret.
func (c *Catalog) ensureManifest() (*Manifest, error) {
	man, err := c.Load()
	if err != nil {
		return nil, err
	}
	if man != nil {
		return man, nil
	}
	if c.WorkspaceRoot == "" {
		return nil, c.errf(ReasonInternal, "workspace catalog has no manifest and no recorded workspace root")
	}
	if err := c.requireOwned(); err != nil {
		return nil, err
	}
	man = &Manifest{Version: LayoutVersion, Workspace: c.workspace, Root: c.WorkspaceRoot}
	if err := c.Save(man); err != nil {
		return nil, err
	}
	return man, nil
}

// ---------------------------------------------------------------------------
// Atomic manifest replacement
// ---------------------------------------------------------------------------

// writeManifestAtomic performs the durable replace of one manifest file.
//
// The scratch file is created in the same directory as the target because a
// rename is only atomic within one filesystem; a scratch file in the temp
// directory could leave a half-written manifest at the final path.
func (m *Manager) writeManifestAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, manifestTempPrefix+"*")
	if err != nil {
		return m.storeError(ReasonUnreadableFile, dir, err)
	}
	tmpName := tmp.Name()
	discard := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		discard()
		return m.storeError(ReasonUnreadableFile, tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		discard()
		return m.storeError(ReasonUnreadableFile, tmpName, err)
	}
	// Flush the content before the rename: otherwise a crash can publish a
	// name that points at unwritten bytes.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		discard()
		return m.storeError(ReasonUnreadableFile, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		discard()
		return m.storeError(ReasonUnreadableFile, tmpName, err)
	}

	if m.hooks.BeforeManifestRename != nil {
		// Crash boundary: the scratch file is durable and the previous manifest
		// is still intact.
		//
		// A hook error is deliberately NOT cleaned up. Hook errors model
		// interruptions, not recoverable write failures, and leaving the
		// scratch file behind reproduces exactly what a crash at this boundary
		// leaves on disk — which is the state reconciliation has to be able to
		// clean. Deleting it here would make the boundary untestable.
		if err := m.hooks.BeforeManifestRename(path); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		discard()
		return m.storeError(ReasonUnreadableFile, path, err)
	}
	if err := syncDir(dir); err != nil {
		return m.storeError(ReasonUnreadableFile, dir, err)
	}
	// Write boundary: the manifest replacement is complete. Both the old and
	// the new manifest existed at the same time while the rename was pending,
	// which is why admission reserves the full replacement rather than one
	// file.
	m.observeWrite(WriteEvent{Kind: "manifest", Path: path})
	if m.hooks.AfterManifestWrite != nil {
		// Crash boundary: the new manifest is durable in its final path.
		if err := m.hooks.AfterManifestWrite(path); err != nil {
			return err
		}
	}
	return nil
}

// syncDir flushes a directory entry so a rename inside it survives a crash.
//
// Some platforms cannot sync a directory handle at all (Windows returns an
// invalid-parameter error). That is not a durability failure this code can act
// on — the rename is still atomic there — so it is ignored rather than turned
// into a failed write. Any other error is reported: silently continuing would
// claim a durability the filesystem did not provide.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil && !isDirSyncUnsupported(syncErr) {
		return syncErr
	}
	return closeErr
}

func isDirSyncUnsupported(err error) bool {
	return errors.Is(err, fs.ErrInvalid) ||
		errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, syscall.EBADF) ||
		errors.Is(err, syscall.ENOTSUP)
}

// canonicalWorkspaceRoot returns the canonical form of a workspace root.
//
// Symbolic links are resolved when the path exists, so two names for one tree
// map to one store. A path that no longer exists keeps its absolute form: a
// workspace whose checkout was deleted must still be discovered and reported,
// which resolving could never do for a missing path.
func canonicalWorkspaceRoot(root string) (string, error) {
	if root == "" {
		return "", storeErrorf(ReasonInternal, "workspace root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", storeError(ReasonUnreadableFile, err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return abs, nil
}
