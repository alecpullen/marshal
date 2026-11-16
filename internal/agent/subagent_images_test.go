package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

// runChildWithImages dispatches one agent.run whose child "ran a tool" that
// surfaced refs, and returns the report message the parent received. The fake
// exec does exactly what the child's real tool loop does with an
// image-bearing result — build the wire message through the runner's own
// builder — so the retention path under test is the production one.
func runChildWithImages(t *testing.T, description string, refs []schema.ImageRef) session.Message {
	t.Helper()
	state, _ := newPersistedState(t, "images-parent")
	childState, _ := newPersistedState(t, "images-child")

	run := NewSubagentTool(
		func(req SubagentRequest) (*Runner, *session.State, error) { return &Runner{}, childState, nil },
		nil, registry.New(), state,
		WithSubagentExec(func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
			child.buildToolResultMessage("image.view", registry.ToolResult{
				Summary: "screenshots",
				Images:  refs,
			}, "call-image")
			return "the child report", "", nil
		}),
	)

	args, err := json.Marshal(map[string]string{
		"prompt":      "look at the screenshot",
		"description": description,
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if _, err := run.Handler(context.Background(), registry.ToolCall{
		ID: "call-run", Name: "agent.run", Args: args,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	views := state.Subagents()
	if len(views) != 1 {
		t.Fatalf("subagents = %d, want 1", len(views))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := state.WaitSubagent(ctx, views[0].ID); err != nil {
		t.Fatalf("WaitSubagent: %v", err)
	}

	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeSubagentReport {
			return m
		}
	}
	t.Fatal("the parent transcript holds no subagent report message")
	return session.Message{}
}

// TestSubagentImagesHoisted: a child whose last tool result carried two refs
// hands both to the parent on its report, so the parent sees the picture
// without the child's whole transcript being replayed.
func TestSubagentImagesHoisted(t *testing.T) {
	refs := []schema.ImageRef{
		imgRef("aaaaaaaaaaaaaaaa", 1568, 882, 240_000),
		imgRef("bbbbbbbbbbbbbbbb", 640, 480, 1024),
	}
	msg := runChildWithImages(t, "snapshotter", refs)
	if len(msg.Images) != 2 {
		t.Fatalf("report images = %d, want the child's 2", len(msg.Images))
	}
	if msg.Images[0].Hash != refs[0].Hash || msg.Images[1].Hash != refs[1].Hash {
		t.Fatalf("report images = %+v, want the child's refs in order", msg.Images)
	}
	if !strings.Contains(msg.Content, "the child report") {
		t.Fatalf("report content = %q, want the child's report text", msg.Content)
	}
}

// TestSubagentImagesLabelled: the parent can tell whose picture it is — every
// hoisted ref names the child, and a ref with no label of its own gets the
// name alone rather than a dangling separator.
func TestSubagentImagesLabelled(t *testing.T) {
	labelled := imgRef("cccccccccccccccc", 320, 200, 4096) // Label: "screenshot of /tmp/ui.png"
	unlabelled := schema.ImageRef{Hash: "dddddddddddddddd", MIME: "image/png", Width: 100, Height: 80}
	msg := runChildWithImages(t, "snapshotter", []schema.ImageRef{labelled, unlabelled})
	if len(msg.Images) != 2 {
		t.Fatalf("report images = %d, want 2", len(msg.Images))
	}
	if got, want := msg.Images[0].Label, "snapshotter: screenshot of /tmp/ui.png"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if got, want := msg.Images[1].Label, "snapshotter"; got != want {
		t.Fatalf("label for an unlabelled ref = %q, want %q", got, want)
	}
	for i, ref := range msg.Images {
		if !strings.HasPrefix(ref.Label, "snapshotter") {
			t.Fatalf("image %d label = %q, want it to start with the child's name", i, ref.Label)
		}
	}
}

// TestTakeToolImagesClears: the images belong to the report, so taking them
// consumes them — a second take returns nothing and a plain result leaves the
// slot empty.
func TestTakeToolImagesClears(t *testing.T) {
	child := &Runner{}
	child.buildToolResultMessage("image.view", registry.ToolResult{
		Summary: "screenshots",
		Images: []schema.ImageRef{
			imgRef("eeeeeeeeeeeeeeee", 800, 600, 2048),
			imgRef("ffffffffffffffff", 400, 300, 1024),
		},
	}, "call-image")
	if got := child.TakeToolImages(); len(got) != 2 {
		t.Fatalf("first take = %d, want 2", len(got))
	}
	if got := child.TakeToolImages(); got != nil {
		t.Fatalf("second take = %+v, want nil", got)
	}
	// A result that carries no images must not resurrect the cleared set.
	child.buildToolResultMessage("file.read", registry.ToolResult{Summary: "text"}, "call-read")
	if got := child.TakeToolImages(); got != nil {
		t.Fatalf("take after a plain result = %+v, want nil", got)
	}
}
