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
