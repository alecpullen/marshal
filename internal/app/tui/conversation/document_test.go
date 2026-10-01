package conversation

import (
	"testing"
)

func ids(blocks []Block) []BlockID {
	out := make([]BlockID, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.ID)
	}
	return out
}

// A block covering one transcript item is identified by that item, so the
// identity a reader anchors to is the same one the transcript produced.
func TestSingleBlockIDDerivesFromItsMember(t *testing.T) {
	doc := NewDocument([]Block{
		{Kind: BlockMessage, Members: []string{"msg:7"}, Source: SourceAnswer, Text: "hello"},
	})
	if got := doc.Blocks()[0].ID; got != BlockID("msg:7") {
		t.Fatalf("block ID = %q, want %q", got, "msg:7")
	}
}

// A collapsed group's identity derives from its first member, so the group is
// stable as it grows: adding a fourth tool call to a run of three must not
// renumber the group the reader is looking at.
func TestGroupIDDerivesFromFirstMember(t *testing.T) {
	three := NewDocument([]Block{{
		Kind:    BlockToolGroup,
		Members: []string{"audit:1", "audit:2", "audit:3"},
	}})
	four := NewDocument([]Block{{
		Kind:    BlockToolGroup,
		Members: []string{"audit:1", "audit:2", "audit:3", "audit:4"},
	}})

	if three.Blocks()[0].ID != four.Blocks()[0].ID {
		t.Fatalf("group identity changed as it grew: %q -> %q",
			three.Blocks()[0].ID, four.Blocks()[0].ID)
	}
	if got := three.Blocks()[0].ID; got != BlockID("group:audit:1") {
		t.Fatalf("group ID = %q, want %q", got, "group:audit:1")
	}
}

func TestLocateMemberReturnsExactNestedBlockAndAncestors(t *testing.T) {
	doc := NewDocument([]Block{{Kind: BlockNarration, Members: []string{"narration-source", "audit:1"}, Children: []Block{
		{Kind: BlockMessage, Members: []string{"narration-source"}, Text: "plan"},
		{Kind: BlockTool, Members: []string{"audit:1"}, Source: SourceOutput, Text: "small output", CopyTargets: []CopyTarget{{Source: SourceOutput, Text: "small output", Label: "Copy output"}}},
	}}})
	loc, ok := doc.LocateMember("audit:1")
	if !ok || loc.Block.ID != "audit:1" || loc.Block.Text != "small output" {
		t.Fatalf("location = %+v, %v", loc, ok)
	}
	if len(loc.Ancestors) != 1 || loc.Ancestors[0] != "narration-source" {
		t.Fatalf("ancestors = %v", loc.Ancestors)
	}
	if loc.Block.CopyTargets[0].Text != "small output" {
		t.Fatalf("child copy widened: %+v", loc.Block.CopyTargets)
	}
	// Existing lookup deliberately retains its top-level narrowest-block rule.
	outer, ok := doc.BlockForMember("audit:1")
	if !ok || outer.Kind != BlockNarration {
		t.Fatalf("legacy lookup changed: %+v, %v", outer, ok)
	}
}

func TestPresentationOnlyBlockHasNoSourceIdentity(t *testing.T) {
	doc := NewDocument([]Block{{ID: "note:ownership", Kind: BlockOwnershipNote, Text: "Ownership unavailable", PresentationOnly: true}})
	if doc.Len() != 1 || doc.Blocks()[0].ID != "note:ownership" {
		t.Fatalf("presentation note dropped: %+v", doc.Blocks())
	}
	if _, ok := doc.BlockForMember("note:ownership"); ok {
		t.Fatal("presentation note became a source member")
	}
	if _, ok := doc.LocateMember("note:ownership"); ok {
		t.Fatal("presentation note became locatable as a transcript source")
	}
}

// A group references every member it covers, so a member that also has its
// own block is not ambiguous.
func TestGroupReferencesEveryMember(t *testing.T) {
	doc := NewDocument([]Block{
		{Kind: BlockToolGroup, Members: []string{"audit:1", "audit:2"}},
		{Kind: BlockTool, Members: []string{"audit:3"}},
	})

	group := doc.Blocks()[0]
	if len(group.Members) != 2 {
		t.Fatalf("group members = %v, want 2", group.Members)
	}
	if block, ok := doc.BlockForMember("audit:3"); !ok || block.ID != BlockID("audit:3") {
		t.Fatalf("BlockForMember(audit:3) = %+v, %v; want the single-item block", block, ok)
	}
}

// THE requirement: selecting a member of a collapsed group must resolve to
// that member, not to the whole group. Resolving a member to its group is how
// "copy this line" silently copies four tool outputs.
func TestMemberOfGroupResolvesToTheNarrowestBlock(t *testing.T) {
	// The same member can legitimately appear in a group and on its own
	// (a failed call renders standalone inside a run of reads).
	doc := NewDocument([]Block{
		{Kind: BlockToolGroup, Members: []string{"audit:1", "audit:2"}},
		{Kind: BlockTool, Members: []string{"audit:2"}, Text: "the one I selected"},
	})

	got, ok := doc.BlockForMember("audit:2")
	if !ok {
		t.Fatal("BlockForMember(audit:2) not found")
	}
	if got.Kind != BlockTool || got.Text != "the one I selected" {
		t.Fatalf("resolved to %+v, want the single-member block, not the group", got)
	}

	// A member with no block of its own still resolves, to its group.
	only, ok := doc.BlockForMember("audit:1")
	if !ok || only.Kind != BlockToolGroup {
		t.Fatalf("BlockForMember(audit:1) = %+v, %v; want the group", only, ok)
	}
}

// The narrowest rule must not depend on declaration order. Here the narrow
// block is declared LAST, so "last write wins" would also pass the test
// above; this one fails unless the rule is genuinely about member count.
func TestMemberResolutionIgnoresDeclarationOrder(t *testing.T) {
	// Group first, then the narrow block.
	groupFirst := NewDocument([]Block{
		{Kind: BlockToolGroup, Members: []string{"audit:1", "audit:2", "audit:3"}},
		{Kind: BlockTool, Members: []string{"audit:2"}, Text: "narrow"},
	})
	// The same two blocks, declared in the opposite order.
	narrowFirst := NewDocument([]Block{
		{Kind: BlockTool, Members: []string{"audit:2"}, Text: "narrow"},
		{Kind: BlockToolGroup, Members: []string{"audit:1", "audit:2", "audit:3"}},
	})

	for name, doc := range map[string]*Document{"group-first": groupFirst, "narrow-first": narrowFirst} {
		got, ok := doc.BlockForMember("audit:2")
		if !ok {
			t.Fatalf("%s: BlockForMember(audit:2) not found", name)
		}
		if got.Kind != BlockTool || got.Text != "narrow" {
			t.Fatalf("%s: resolved audit:2 to %+v, want the narrow single-item block regardless of order", name, got)
		}
	}
}

// A member that only the group covers still resolves to the group, in both
// orderings: the rule must not make the group unreachable.
func TestGroupOnlyMemberResolvesToGroup(t *testing.T) {
	for name, doc := range map[string]*Document{
		"group-first": NewDocument([]Block{
			{Kind: BlockToolGroup, Members: []string{"audit:1", "audit:2", "audit:3"}},
			{Kind: BlockTool, Members: []string{"audit:2"}},
		}),
		"narrow-first": NewDocument([]Block{
			{Kind: BlockTool, Members: []string{"audit:2"}},
			{Kind: BlockToolGroup, Members: []string{"audit:1", "audit:2", "audit:3"}},
		}),
	} {
		got, ok := doc.BlockForMember("audit:1")
		if !ok || got.Kind != BlockToolGroup {
			t.Fatalf("%s: BlockForMember(audit:1) = %+v, %v; want the group", name, got, ok)
		}
	}
}

// An unknown member is reported as absent rather than as some other block, so
// a caller cannot silently copy the wrong thing.
func TestBlockForMemberAbsent(t *testing.T) {
	doc := NewDocument([]Block{{Kind: BlockMessage, Members: []string{"msg:1"}}})
	if block, ok := doc.BlockForMember("msg:99"); ok {
		t.Fatalf("BlockForMember(msg:99) = %+v, want not found", block)
	}
}

// A block's identity does not depend on position: rebuilding the document
// with the same content yields the same identities, and reordering does not
// rename anything.
func TestBlockIDsIndependentOfPosition(t *testing.T) {
	first := NewDocument([]Block{
		{Kind: BlockMessage, Members: []string{"msg:1"}},
		{Kind: BlockMessage, Members: []string{"msg:2"}},
	})
	reordered := NewDocument([]Block{
		{Kind: BlockMessage, Members: []string{"msg:2"}},
		{Kind: BlockMessage, Members: []string{"msg:1"}},
	})

	if ids(first.Blocks())[0] != ids(reordered.Blocks())[1] {
		t.Fatalf("msg:1 renamed by reordering: %v vs %v", ids(first.Blocks()), ids(reordered.Blocks()))
	}
}

// Revision counts semantic change so a consumer can tell "same block" from
// "same block, new content" — the difference between reusing a cached render
// and reparsing.
func TestRevisionCarriedThrough(t *testing.T) {
	doc := NewDocument([]Block{{Kind: BlockMessage, Members: []string{"msg:1"}, Revision: 3}})
	if got := doc.Blocks()[0].Revision; got != 3 {
		t.Fatalf("Revision = %d, want 3", got)
	}
}

// The document claims to be immutable, and an index built once is only honest
// if nothing can reach past it. Two slices are handed out — the caller's own
// Members and the document's Blocks — and both are cloned rather than aliased,
// so a caller that keeps either and later mutates it cannot desync the index
// from the blocks it indexes.
//
// The failure this pins is silent rather than loud: the byMember index would
// still answer "msg:1" for a block whose Members now say something else, and
// every resolution, copy and anchor would then name a member the block does
// not cover.
func TestDocumentIsImmuneToMutationOfTheCallersSlices(t *testing.T) {
	members := []string{"msg:1", "msg:2"}
	children := []Block{{Kind: BlockTool, Members: []string{"audit:1"}, Text: "read a.go"}}
	doc := NewDocument([]Block{{
		Kind:     BlockToolGroup,
		Members:  members,
		Children: children,
	}})

	// The caller mutates the slice it handed in, after construction.
	members[0] = "msg:99"
	children[0].Text = "overwritten"
	children[0].Members[0] = "audit:99"

	if block, ok := doc.BlockForMember("msg:1"); !ok || block.ID != GroupBlockID("msg:1") {
		t.Fatalf("BlockForMember(msg:1) = %+v, %v; the caller's mutation renamed a member the index still answers for",
			block, ok)
	}
	if block, ok := doc.BlockForMember("msg:99"); ok {
		t.Fatalf("BlockForMember(msg:99) = %+v; the caller's slice reached into the document", block)
	}
	if _, ok := doc.BlockForMember("audit:1"); !ok {
		t.Fatal("BlockForMember(audit:1) not found: a child's members must still resolve")
	}
	got := doc.Blocks()[0]
	if len(got.Members) != 2 || got.Members[0] != "msg:1" {
		t.Fatalf("block members = %v, want the members as constructed", got.Members)
	}
	if got.Children[0].Text != "read a.go" || got.Children[0].Members[0] != "audit:1" {
		t.Fatalf("block children = %+v, want the children as constructed", got.Children[0])
	}

	// Now the slice the document handed OUT. Appending to it writes into
	// whatever array it was given, so an aliased Blocks() is how a caller grows
	// the document's tail with a block the index never saw.
	blocks := doc.Blocks()
	blocks[0].Text = "overwritten"

	// The assertion above is NOT evidence that Blocks() clones: "the element I
	// wrote was written" holds for any slice of structs, cloned or not, because
	// a Block is a value and the write lands in the copy either way. What
	// separates a clone from an alias is that TWO calls never share an array,
	// so grow the returned slice and write through every element it now has,
	// then ask the DOCUMENT and a SECOND call what they hold. An aliased
	// Blocks() fails here and passes every assertion above.
	//
	// Only the Block structs are written, deliberately: the clone is shallow,
	// so a Block's own Members/Children/CopyTargets slices ARE the document's
	// (documented on Blocks, which tells a caller to treat them as read-only).
	// Writing nested storage would be testing the sharing the doc admits to
	// rather than the array-identity the clone is supposed to guarantee.
	blocks = append(blocks, Block{Kind: BlockMessage, Members: []string{"msg:3"}})
	for i := range blocks {
		blocks[i].Text = "overwritten"
	}
	if blocks[0].Text != "overwritten" {
		t.Fatalf("the returned slice is not the caller's to mutate: %q", blocks[0].Text)
	}

	if doc.Len() != 1 {
		t.Fatalf("appending to the returned slice grew the document to %d blocks", doc.Len())
	}
	if doc.Blocks()[0].Text != "" {
		t.Fatalf("writing through the returned slice reached the document: %q", doc.Blocks()[0].Text)
	}
	if _, ok := doc.BlockForMember("msg:3"); ok {
		t.Fatal("a block appended to the returned slice became resolvable in the document")
	}
	// The document's own view is untouched by the writes above — and so is a
	// SECOND call's, which is the part that pins the clone rather than the
	// value semantics.
	if again := doc.Blocks(); again[0].Text != "" || again[0].Members[0] != "msg:1" {
		t.Fatalf("the document changed through the slice it handed out: %+v", again[0])
	}
	if got, ok := doc.Block(GroupBlockID("msg:1")); !ok || got.Text != "" || got.Members[0] != "msg:1" {
		t.Fatalf("Block(msg:1) = %+v, %v; the caller's writes reached the document", got, ok)
	}
	// Writing through a SECOND call must not reach the FIRST caller either,
	// which is only true if every call copies.
	second := doc.Blocks()
	for i := range second {
		second[i].Text = "through the second call"
	}
	if blocks[0].Text != "overwritten" {
		t.Fatalf("a second Blocks() call aliased the first one's array: %+v", blocks[0])
	}
	if doc.Blocks()[0].Text != "" || doc.Blocks()[0].Members[0] != "msg:1" {
		t.Fatalf("the document changed through a repeat call: %+v", doc.Blocks()[0])
	}
}

// A block with no members cannot be identified and must be rejected rather
// than silently given an empty identity that collides with every other one.
func TestDocumentRejectsMemberlessBlock(t *testing.T) {
	doc := NewDocument([]Block{{Kind: BlockMessage}})

	for _, b := range doc.Blocks() {
		if b.ID == "" {
			t.Fatal("a memberless block was accepted with an empty identity")
		}
	}
	if len(doc.Blocks()) != 0 {
		t.Fatalf("memberless block survived as %+v", doc.Blocks())
	}
}

// A block whose identity is already taken is DROPPED rather than admitted.
// Pinning this deliberately: the alternative (a second block with the same
// ID) would make "the block the reader is anchored to" name two different
// rows, which is how a copy or a scroll silently lands on the wrong content.
// The cost of the drop is real — the second block's text leaves the
// transcript — and it is the FIRST block that survives, because that is the
// one an earlier reader could already be anchored to.
func TestDocumentDropsADuplicateIdentityBlock(t *testing.T) {
	doc := NewDocument([]Block{
		{Kind: BlockMessage, Members: []string{"msg:1"}, Text: "first"},
		{Kind: BlockMessage, Members: []string{"msg:1"}, Text: "second"},
	})
	if len(doc.Blocks()) != 1 {
		t.Fatalf("the document holds %d blocks, want only the first", len(doc.Blocks()))
	}
	if got := doc.Blocks()[0].Text; got != "first" {
		t.Fatalf("the surviving block carries %q, want the first block's text", got)
	}
	if doc.Len() != 1 {
		t.Fatalf("Len = %d after a duplicate was dropped", doc.Len())
	}
}

// Copy targets name what the text IS, so a copy action can state its scope
// honestly ("Copy answer" vs "Copy code") rather than guessing from content.
func TestBlockCarriesSemanticCopyTargets(t *testing.T) {
	doc := NewDocument([]Block{{
		Kind:    BlockMessage,
		Members: []string{"msg:1"},
		Source:  SourceAnswer,
		Text:    "the answer",
		CopyTargets: []CopyTarget{
			{Source: SourceAnswer, Text: "the answer", Label: "Copy answer"},
			{Source: SourceCode, Text: "fmt.Println()", Label: "Copy code"},
		},
	}})

	targets := doc.Blocks()[0].CopyTargets
	if len(targets) != 2 {
		t.Fatalf("got %d copy targets, want 2", len(targets))
	}
	if targets[0].Source != SourceAnswer || targets[0].Label != "Copy answer" {
		t.Fatalf("first target = %+v", targets[0])
	}
	if targets[1].Text == targets[0].Text {
		t.Fatal("code target must carry the code, not the answer it came from")
	}
}

// Identities must be unique within a document: two blocks sharing one would
// make "the block the reader anchored to" ambiguous.
func TestDocumentIdentitiesAreUnique(t *testing.T) {
	doc := NewDocument([]Block{
		{Kind: BlockMessage, Members: []string{"msg:1"}},
		{Kind: BlockTool, Members: []string{"audit:1"}},
		{Kind: BlockToolGroup, Members: []string{"audit:2", "audit:3"}},
	})

	seen := map[BlockID]bool{}
	for _, b := range doc.Blocks() {
		if seen[b.ID] {
			t.Fatalf("duplicate block ID %q", b.ID)
		}
		seen[b.ID] = true
	}
}

// Children expose a group's members as blocks in their own right, so a
// consumer can navigate into a collapsed run without the group having to be
// expanded first.
func TestGroupChildrenAreAddressable(t *testing.T) {
	doc := NewDocument([]Block{{
		Kind:    BlockToolGroup,
		Members: []string{"audit:1", "audit:2"},
		Children: []Block{
			{Kind: BlockTool, Members: []string{"audit:1"}, Text: "read a.go"},
			{Kind: BlockTool, Members: []string{"audit:2"}, Text: "read b.go"},
		},
	}})

	children := doc.Blocks()[0].Children
	if len(children) != 2 {
		t.Fatalf("got %d children, want 2", len(children))
	}
	if children[0].ID != BlockID("audit:1") || children[1].ID != BlockID("audit:2") {
		t.Fatalf("child IDs = %q, %q", children[0].ID, children[1].ID)
	}
	if children[0].ID == doc.Blocks()[0].ID {
		t.Fatal("a child shares its parent group's identity")
	}
}
