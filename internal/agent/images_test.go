package agent

import (
	"strings"
	"testing"

	"marshal/internal/imagepayload"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

// imgRef builds an ImageRef for the tests below. The hash is padded so
// shortHash's 8-character truncation is observable, and the MIME is the
// common one a screenshot arrives as.
func imgRef(hash string, w, h, bytes int) schema.ImageRef {
	return schema.ImageRef{
		Hash:   hash,
		MIME:   "image/png",
		Width:  w,
		Height: h,
		Bytes:  bytes,
		Label:  "screenshot of /tmp/ui.png",
	}
}

// TestBuildToolResultMessageCarriesImages: the human-readable summary line is
// what a text-only consumer (spill record, transcript, pruned stub) reads, and
// the refs ride along on the message for the encoder.
func TestBuildToolResultMessageCarriesImages(t *testing.T) {
	res := registry.ToolResult{
		Summary: "2 images",
		Content: "see attached",
		Images: []schema.ImageRef{
			imgRef("aaaaaaaaaaaaaaaa", 1568, 882, 240_000),
			imgRef("bbbbbbbbbbbbbbbb", 640, 480, 1024),
		},
	}

	msg := BuildNativeToolResultMessage("snapshot", res, "c1")
	if len(msg.Images) != 2 {
		t.Fatalf("len(msg.Images) = %d, want 2", len(msg.Images))
	}
	if got := strings.Count(msg.Content, "[image: "); got != 2 {
		t.Fatalf("[image: …] lines = %d, want 2; content %q", got, msg.Content)
	}
	if !strings.Contains(msg.Content, "[image: 1568×882 png 234 KB aaaaaaaa]") {
		t.Fatalf("missing first image line; content %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "[image: 640×480 png 1 KB bbbbbbbb]") {
		t.Fatalf("missing second image line; content %q", msg.Content)
	}
	if !strings.HasPrefix(msg.Content, "Tool snapshot result: 2 images\n\nsee attached") {
		t.Fatalf("existing content assembly changed; content %q", msg.Content)
	}

	// The envelope path (no tool-call ID) returns a user-role message, and it
	// must carry the refs too.
	user := BuildToolResultMessage("snapshot", res)
	if user.Role != schema.RoleUser {
		t.Fatalf("role = %v, want user", user.Role)
	}
	if len(user.Images) != 2 {
		t.Fatalf("len(user.Images) = %d, want 2", len(user.Images))
	}
}

// TestBuildToolResultMessageCapsImages: a chatty tool cannot blow the prompt
// up in one result — at most ten images per message.
func TestBuildToolResultMessageCapsImages(t *testing.T) {
	var refs []schema.ImageRef
	for i := 0; i < 12; i++ {
		refs = append(refs, imgRef("0123456789abcdef", 320, 200, 2048))
	}
	msg := BuildNativeToolResultMessage("snapshot", registry.ToolResult{Summary: "12 images", Images: refs}, "c1")
	if len(msg.Images) != 10 {
		t.Fatalf("len(msg.Images) = %d, want 10", len(msg.Images))
	}
	if got := strings.Count(msg.Content, "[image: "); got != 10 {
		t.Fatalf("[image: …] lines = %d, want 10", got)
	}
}

// TestEstimateTokensCountsImages: an image costs its budget figure, not the
// handful of bytes its summary line takes.
func TestEstimateTokensCountsImages(t *testing.T) {
	text := strings.Repeat("a", 400) // 100 tokens, so the division is exact
	base := []schema.ChatMessage{{Role: schema.RoleUser, Content: text}}
	withImage := []schema.ChatMessage{{
		Role:    schema.RoleUser,
		Content: text,
		Images:  []schema.ImageRef{{Width: 1568, Height: 882}},
	}}
	want := imagepayload.EstimateTokens(1568, 882)
	if got := estimateTokens(withImage) - estimateTokens(base); got != want {
		t.Fatalf("image raised the estimate by %d, want %d", got, want)
	}
}

// TestShedImagesNoOpBelowBudget: the happy path is untouched — same slice,
// nothing shed.
func TestShedImagesNoOpBelowBudget(t *testing.T) {
	wire := []schema.ChatMessage{
		{Role: schema.RoleTool, Content: "result", Images: []schema.ImageRef{{Width: 10, Height: 10}}},
	}
	got, shed := shedImages(wire, 100_000)
	if shed != 0 {
		t.Fatalf("shed = %d, want 0", shed)
	}
	if len(got[0].Images) != 1 {
		t.Fatalf("images were dropped under budget: %d", len(got[0].Images))
	}
	if got[0].Content != "result" {
		t.Fatalf("content changed under budget: %q", got[0].Content)
	}
}

// TestShedImagesDropsToolResultsFirst: pass 0 sheds tool results only, so a
// user's own screenshot survives as long as the budget allows it.
func TestShedImagesDropsToolResultsFirst(t *testing.T) {
	img := schema.ImageRef{Width: 1568, Height: 882}
	wire := []schema.ChatMessage{
		{Role: schema.RoleTool, Content: "tool output", Images: []schema.ImageRef{img}},
		{Role: schema.RoleUser, Content: "what is this?", Images: []schema.ImageRef{img}},
	}
	// Between the two: with the tool image shed the prompt fits, without it
	// does not.
	budget := 2000
	if estimateTokens(wire) <= budget {
		t.Fatalf("fixture does not overflow: %d", estimateTokens(wire))
	}
	got, shed := shedImages(wire, budget)
	if shed != 1 {
		t.Fatalf("shed = %d, want 1", shed)
	}
	if len(got[0].Images) != 0 {
		t.Fatalf("tool result kept its images")
	}
	if len(got[1].Images) != 1 {
		t.Fatalf("user message lost its images")
	}
}

// TestShedImagesAppendsNotice: the dropped image leaves a visible trace, so
// the model knows it saw something it can no longer see.
func TestShedImagesAppendsNotice(t *testing.T) {
	wire := []schema.ChatMessage{
		{Role: schema.RoleTool, Content: "tool output", Images: []schema.ImageRef{{Width: 1568, Height: 882}}},
	}
	got, shed := shedImages(wire, 100)
	if shed != 1 {
		t.Fatalf("shed = %d, want 1", shed)
	}
	if !strings.HasSuffix(got[0].Content, "\n[image omitted to fit context]") {
		t.Fatalf("missing shed notice; content %q", got[0].Content)
	}
	if !strings.HasPrefix(got[0].Content, "tool output") {
		t.Fatalf("original content lost; content %q", got[0].Content)
	}
}
