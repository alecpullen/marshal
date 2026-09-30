// Package conversation turns the session transcript into a document of
// semantic blocks, and keeps a reader's place in it across reflows.
//
// It is pure presentation logic over data handed to it: no DB, provider, Git
// or clipboard access, and no goroutines. The caller builds blocks from the
// transcript and the TUI renders them.
//
// The reason this package exists is that a transcript item is not a readable
// object. One assistant answer is a message, one tool call is an audit event,
// a run of same-tool calls renders as a single collapsed group, and the
// in-progress reasoning region is not an item at all. A reader who scrolls
// and then sees new output arrive needs to stay where they were, and needs
// "copy this" to mean the thing they selected — neither is expressible over a
// slice index into a rebuilt, re-sorted transcript.
package conversation

// BlockID is the presentation identity of one block. It is derived from the
// block's members, never from its position, so it is stable across rebuilds
// and reflows.
type BlockID string

// BlockKind classifies a block for rendering decisions that must not be made
// from content.
type BlockKind int

const (
	// BlockMessage is one user or assistant message.
	BlockMessage BlockKind = iota
	// BlockTool is a single tool call rendered on its own (a failure, an
	// edit with a diff, a call carrying hook or symbol metadata).
	BlockTool
	// BlockToolGroup is a collapsed run of consecutive same-tool calls.
	BlockToolGroup
	// BlockThinking is one completed reasoning entry.
	BlockThinking
	// BlockSubagent is a subagent summary card.
	BlockSubagent
	// BlockRunEvent is one plan-run event (verify failure, review finding,
	// commit, retry).
	BlockRunEvent
	// BlockJobExit is a background job finishing.
	BlockJobExit
)

// CopySource names what a piece of text IS, so a copy action can label its
// scope truthfully instead of guessing from the content. "Copy answer" and
// "Copy output" are different promises about the same bytes.
type CopySource string

const (
	// SourceAnswer is an assistant answer's original Markdown.
	SourceAnswer CopySource = "answer"
	// SourceCode is source code with its fence syntax removed and its
	// indentation preserved.
	SourceCode CopySource = "code"
	// SourceOutput is captured tool output, verbatim.
	SourceOutput CopySource = "output"
	// SourcePath is a filesystem path the block refers to.
	SourcePath CopySource = "path"
	// SourcePatch is a patch (diff) as fetched, not as rendered. It is a
	// source of its own rather than a kind of code: a patch carries line
	// markers and hunk headers that are part of what it IS, and stripping
	// them the way SourceCode strips a fence is exactly what makes a copied
	// diff stop applying.
	SourcePatch CopySource = "patch"
)

// CopyTarget is one thing a block can put on the clipboard, with the label
// that states its scope. A block carries several when its text contains
// several distinct things (an answer containing code).
type CopyTarget struct {
	Source CopySource
	Text   string
	Label  string
}

// Block is one semantic unit of the conversation.
type Block struct {
	// ID is this block's identity. For a single-member block it is that
	// member's identity; for a group it is derived from the group's first
	// member.
	ID BlockID
	// Kind classifies the block.
	Kind BlockKind
	// Members are the transcript identities this block covers, in order. A
	// group lists every member it collapsed; a single block lists one. A
	// member can appear both in a group and in a block of its own (a failed
	// call renders standalone inside a run of reads), which is why
	// Document.BlockForMember resolves to the narrowest match.
	Members []string
	// Children expose a group's members as addressable blocks, so a consumer
	// can navigate into a collapsed run without expanding it first.
	Children []Block
	// Source names what Text is, when the block has one dominant text.
	Source CopySource
	// Text is the block's source text as handed in by the caller.
	Text string
	// Hidden marks a block whose text is model context rather than something
	// the transcript draws. A skill body and a subagent's report reach the
	// model and are deliberately not rendered, so they are not reachable by
	// scrolling and a search must not offer to jump to them. It is a property
	// of the BLOCK rather than of its kind because the same kind renders
	// visibly in one place and is hidden in another.
	Hidden bool
	// Truncated marks a block whose captured text was capped by its source, so
	// a search over it can say what it actually covered rather than implying
	// the whole thing was searched.
	Truncated bool
	// CopyTargets are the clipboard payloads this block offers.
	CopyTargets []CopyTarget
	// Revision counts semantic change to this block's content. A consumer
	// caching a render keys on (ID, Revision, width, expansion, theme), so
	// an unchanged block is not reparsed when unrelated output arrives.
	Revision int
}

// Document is an ordered set of blocks with an identity index.
//
// The index is built once at construction; the document is otherwise
// immutable, so a reader's anchor cannot be invalidated by a later lookup.
type Document struct {
	blocks []Block
	// byID indexes the outermost blocks.
	byID map[BlockID]int
	// byMember resolves a transcript identity to the block that owns it. A
	// member covered by several blocks resolves to the NARROWEST one: the
	// single-item block wins over the group that also contains it, because
	// resolving a selected member to its enclosing group is how "copy this
	// line" silently copies four tool outputs.
	byMember map[string]int
}

// NewDocument indexes the supplied blocks.
//
// A block with no members is dropped: it has no identity to derive, and
// admitting it with an empty ID would collide with every other memberless
// block and make "the block the reader is on" ambiguous.
func NewDocument(blocks []Block) *Document {
	doc := &Document{
		blocks:   make([]Block, 0, len(blocks)),
		byID:     make(map[BlockID]int, len(blocks)),
		byMember: make(map[string]int, len(blocks)),
	}
	for _, b := range blocks {
		b = normalizeBlock(b)
		if b.ID == "" {
			continue
		}
		if _, dup := doc.byID[b.ID]; dup {
			// A duplicate identity would make the block a reader anchored
			// to ambiguous. Keep the first: order is the only tiebreak the
			// document has, and silently overwriting would move an anchor.
			continue
		}
		doc.byID[b.ID] = len(doc.blocks)
		doc.blocks = append(doc.blocks, b)
		doc.indexMembers(len(doc.blocks)-1, b)
	}
	return doc
}

// indexMembers records every member of a block, and of its children, against
// the outermost block that owns them — with the narrowest block winning.
//
// Children are indexed against their parent because they are not top-level
// blocks: they have no index of their own to scroll to. But their members
// must still resolve, or selecting a member of a collapsed group would find
// nothing; and the narrowest-block rule below is what makes a member that is
// also a block of its own resolve to that block rather than to the group.
func (d *Document) indexMembers(index int, b Block) {
	for _, m := range b.Members {
		if prev, ok := d.byMember[m]; ok && d.isNarrowerThan(prev, index) {
			// An already-recorded narrower block keeps the member: it is the
			// more specific answer, and the more specific answer is what the
			// user selected.
			continue
		}
		d.byMember[m] = index
	}
	for _, child := range b.Children {
		d.indexMembers(index, child)
	}
}

// isNarrowerThan reports whether the block at prev covers strictly fewer
// members than the block at candidate.
//
// Declaration order is deliberately NOT the tiebreak. Relying on it would
// make the answer depend on how the caller happened to order its blocks —
// which is exactly how "select this line" starts copying a whole group after
// an unrelated reordering upstream.
func (d *Document) isNarrowerThan(prev, candidate int) bool {
	return len(d.blocks[prev].Members) < len(d.blocks[candidate].Members)
}

// normalizeBlock stamps block identities throughout a subtree, so a caller
// can declare children without having to know the derivation rule. It is
// applied at construction rather than by the accessor so that two lookups of
// the same child cannot disagree about its identity.
//
// It also takes OWNERSHIP of the caller's slices. The document claims to be
// immutable, and a slice stored by reference is not: a caller that keeps its
// Members slice and later does members[0] = "other" would rename a block
// through the back door — the identity index would still say "msg:7" while the
// block's Members said something else, and every resolution would then name a
// member the block does not cover. Cloning at construction is the cheap end of
// the trade: it costs one copy per block, once, against a class of desync that
// is invisible until it corrupts an anchor.
//
// It clones Members and Children, and ONLY those: CopyTargets is carried
// through by reference, which is sound here only because nothing in this
// package ever writes to a CopyTarget through a block. A caller must not
// either — see Document.Blocks, which hands that same slice out.
func normalizeBlock(b Block) Block {
	b.ID = blockID(b)
	b.Members = cloneStrings(b.Members)
	if len(b.Children) == 0 {
		return b
	}
	children := make([]Block, len(b.Children))
	for i, child := range b.Children {
		children[i] = normalizeBlock(child)
	}
	b.Children = children
	return b
}

// cloneStrings returns an independent copy of a string slice, or nil for an
// empty one: the document's blocks must share no array with their caller.
func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

// cloneBlocks returns an independent copy of a block slice, so a caller cannot
// reach the document's own array by appending to what it was handed.
func cloneBlocks(in []Block) []Block {
	if len(in) == 0 {
		return nil
	}
	return append([]Block(nil), in...)
}

// blockID derives a block's identity from its members.
//
// Single-member blocks take the member's identity directly, so the identity a
// reader anchors to is the one the transcript produced. Groups are prefixed
// and keyed by their FIRST member: the group must stay the same block as it
// grows, and its first member is the one thing that cannot change while it
// grows (members are appended).
func blockID(b Block) BlockID {
	if len(b.Members) == 0 {
		return ""
	}
	if b.Kind == BlockToolGroup {
		return GroupBlockID(b.Members[0])
	}
	return BlockID(b.Members[0])
}

// GroupBlockID derives a collapsed group's identity from its first member.
//
// It is exported because the identity has to be derived in TWO places — here,
// where a document is built from blocks, and in the transcript builder, which
// records where each rendered block sits on screen — and the two derivations
// must agree exactly. They did not: the builder used the first member's raw
// identity for a group, so the group was recorded in the rendered-span table
// under a name the document never uses. Every lookup by the group's own
// identity then missed, and the reader's anchor fell through to a
// nearest-position guess that landed them at the top of the transcript.
//
// The rule therefore lives in one place and both callers call it. A prefix
// written twice is a prefix that will eventually be written differently.
func GroupBlockID(firstMember string) BlockID {
	if firstMember == "" {
		return ""
	}
	return BlockID("group:" + firstMember)
}

// Blocks returns the document's outermost blocks in order, as a COPY.
//
// A copy rather than the document's own slice because the document is
// immutable and a handed-out slice is not: a caller doing
// append(doc.Blocks(), b) with spare capacity would write into the document's
// tail, and the next lookup would then resolve against a block the index never
// saw. Blocks are small (identities, a text string and its slice headers), so
// the copy is cheap next to the walk that consumes it.
//
// The copy is SHALLOW: a block's own Members, Children and CopyTargets slices
// are shared, read-only.
//
// Mutating a Block or its CopyTargets or Children through what this returns
// corrupts the document: treat every returned Block, and every slice it points
// at, as read-only. Only the SLICE OF BLOCKS is the caller's — appending to it
// (or writing through it) cannot reach the document's own array, and that is
// the one mutation this method is built to survive, because it is the one a
// caller performs by accident.
//
// Members and Children are cloned at construction, so a caller cannot reach the
// document through the slice it PASSED IN either — but that protects the
// document from its caller's earlier slices, not from the blocks handed back
// here. Deep-copying every block's slices on every call would buy protection
// against a mutation no caller performs, at a cost paid by every render.
func (d *Document) Blocks() []Block { return cloneBlocks(d.blocks) }

// Len reports how many blocks the document holds.
func (d *Document) Len() int { return len(d.blocks) }

// Block returns the block with this identity.
func (d *Document) Block(id BlockID) (Block, bool) {
	i, ok := d.byID[id]
	if !ok {
		return Block{}, false
	}
	return d.blocks[i], true
}

// BlockForMember returns the narrowest block covering a transcript identity.
func (d *Document) BlockForMember(member string) (Block, bool) {
	i, ok := d.byMember[member]
	if !ok {
		return Block{}, false
	}
	return d.blocks[i], true
}

// IndexOf returns a block's position, for a caller that needs to render or
// scroll to it.
func (d *Document) IndexOf(id BlockID) (int, bool) {
	i, ok := d.byID[id]
	return i, ok
}

// Has reports whether the document holds this identity.
func (d *Document) Has(id BlockID) bool {
	_, ok := d.byID[id]
	return ok
}
