package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"marshal/internal/imagepayload"
	"marshal/internal/tools/registry"
)

// imageTestStore opens a store rooted in a temp directory. The zero Config
// takes the store's own defaults, which is what a runtime with no [images]
// overrides hands the manager.
func imageTestStore(t *testing.T) *imagepayload.Store {
	t.Helper()
	store, err := imagepayload.Open(t.TempDir(), imagepayload.Config{})
	if err != nil {
		t.Fatalf("open image store: %v", err)
	}
	return store
}

// pngBase64 encodes a width×height PNG and returns it base64-encoded, the
// shape an MCP server sends an image block in.
func pngBase64(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestMCPImageBlockBecomesRef: a type "image" block reaches the caller as a
// registry.ToolResult image ref, labelled with the server that produced it.
func TestMCPImageBlockBecomesRef(t *testing.T) {
	m := NewManager(nil, WithImageStore(imageTestStore(t)))
	handler := m.makeHandler(&stubCaller{
		res: CallToolResult{
			Content: []MCPContent{
				{Type: "text", Text: "captured the window"},
				{Type: "image", Data: pngBase64(t, 8, 6), MimeType: "image/png"},
			},
		},
	}, "shotsrv", "screenshot")

	res, err := handler(context.Background(), registry.ToolCall{Name: "screenshot"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Images) != 1 {
		t.Fatalf("len(Images) = %d, want 1", len(res.Images))
	}
	ref := res.Images[0]
	if !strings.Contains(ref.Label, "shotsrv") {
		t.Fatalf("label = %q, want it to name the server", ref.Label)
	}
	if ref.Hash == "" {
		t.Fatal("image ref carries no hash")
	}
	if ref.Width != 8 || ref.Height != 6 {
		t.Fatalf("ref is %dx%d, want 8x6", ref.Width, ref.Height)
	}
	// The text content still survives beside the image.
	if !strings.Contains(res.Content, "captured the window") {
		t.Fatalf("content = %q, want the text block", res.Content)
	}
}

// TestMCPImageFailureKeepsText: one unusable image block must not cost the
// result its text — the block is dropped with a notice line.
func TestMCPImageFailureKeepsText(t *testing.T) {
	m := NewManager(nil, WithImageStore(imageTestStore(t)))
	handler := m.makeHandler(&stubCaller{
		res: CallToolResult{
			Content: []MCPContent{
				{Type: "text", Text: "here is the report"},
				{Type: "image", Data: "not-valid-base64!!!", MimeType: "image/png"},
			},
		},
	}, "shotsrv", "screenshot")

	res, err := handler(context.Background(), registry.ToolCall{Name: "screenshot"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Images) != 0 {
		t.Fatalf("len(Images) = %d, want 0", len(res.Images))
	}
	if !strings.Contains(res.Content, "here is the report") {
		t.Fatalf("text content lost; content %q", res.Content)
	}
	if !strings.Contains(res.Content, "[image omitted:") {
		t.Fatalf("missing notice for the dropped image; content %q", res.Content)
	}
}

// TestMCPImageBlocksCappedAtFour: a server cannot flood one tool result with
// images — at most four refs per result.
func TestMCPImageBlocksCappedAtFour(t *testing.T) {
	content := make([]MCPContent, 0, 5)
	for i := 0; i < 5; i++ {
		content = append(content, MCPContent{
			Type:     "image",
			Data:     pngBase64(t, 8+i, 6+i),
			MimeType: "image/png",
		})
	}
	m := NewManager(nil, WithImageStore(imageTestStore(t)))
	handler := m.makeHandler(&stubCaller{res: CallToolResult{Content: content}}, "shotsrv", "screenshot")

	res, err := handler(context.Background(), registry.ToolCall{Name: "screenshot"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Images) != 4 {
		t.Fatalf("len(Images) = %d, want 4", len(res.Images))
	}
}
