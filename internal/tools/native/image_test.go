package native

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/imagepayload"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

// imageTestStore opens a store rooted in a temp directory with the default
// image limits, which is what the runtime hands the toolset.
func imageTestStore(t *testing.T) *imagepayload.Store {
	t.Helper()
	store, err := imagepayload.Open(filepath.Join(t.TempDir(), "images"), config.Default().Images.PayloadConfig())
	if err != nil {
		t.Fatalf("open image store: %v", err)
	}
	return store
}

// writeTestPNG writes a width×height PNG into dir and returns its path
// relative to dir.
func writeTestPNG(t *testing.T, dir, name string, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 200, A: 255})
		}
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	return name
}

func TestImageViewByPath(t *testing.T) {
	dir := t.TempDir()
	rel := writeTestPNG(t, dir, "shot.png", 8, 6)
	tool := (&toolSet{root: dir, images: imageTestStore(t)}).imageViewTool()

	result, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "image.view",
		Args: json.RawMessage(`{"path":"` + rel + `","label":"screenshot of shot.png"}`),
	})
	if err != nil {
		t.Fatalf("image.view by path: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("len(Images) = %d, want 1", len(result.Images))
	}
	ref := result.Images[0]
	if ref.Width != 8 || ref.Height != 6 {
		t.Fatalf("ref dimensions = %d×%d, want 8×6", ref.Width, ref.Height)
	}
	if ref.Hash == "" {
		t.Fatal("ref hash is empty")
	}
	if ref.MIME != "image/png" {
		t.Fatalf("ref MIME = %q, want image/png", ref.MIME)
	}
	if ref.Label != "screenshot of shot.png" {
		t.Fatalf("ref label = %q, want the argument", ref.Label)
	}
	if !strings.Contains(result.Content, ref.Hash) {
		t.Fatalf("content %q does not name the hash %s", result.Content, ref.Hash)
	}
	if !strings.Contains(result.Summary, "8×6") {
		t.Fatalf("summary %q does not report the dimensions", result.Summary)
	}
	// The source and the stored image are the same bytes: nothing to report.
	if result.Notice != nil {
		t.Fatalf("unexpected notice on an unmodified image: %+v", result.Notice)
	}
}

func TestImageViewByRef(t *testing.T) {
	dir := t.TempDir()
	rel := writeTestPNG(t, dir, "shot.png", 8, 6)
	tool := (&toolSet{root: dir, images: imageTestStore(t)}).imageViewTool()

	first, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "image.view",
		Args: json.RawMessage(`{"path":"` + rel + `"}`),
	})
	if err != nil {
		t.Fatalf("image.view by path: %v", err)
	}
	want := first.Images[0]

	second, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "image.view",
		Args: json.RawMessage(`{"ref":"` + want.Hash + `"}`),
	})
	if err != nil {
		t.Fatalf("image.view by ref: %v", err)
	}
	if len(second.Images) != 1 {
		t.Fatalf("len(Images) = %d, want 1", len(second.Images))
	}
	if got := second.Images[0]; got != want {
		t.Fatalf("ref = %+v, want %+v", got, want)
	}
}

func TestImageViewUnknownRef(t *testing.T) {
	dir := t.TempDir()
	tool := (&toolSet{root: dir, images: imageTestStore(t)}).imageViewTool()

	hash := strings.Repeat("a", 64)
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "image.view",
		Args: json.RawMessage(`{"ref":"` + hash + `"}`),
	})
	if err == nil {
		t.Fatal("expected an error result for an unknown hash")
	}
	if !strings.Contains(err.Error(), hash) {
		t.Fatalf("error %q does not name the hash %s", err, hash)
	}
}

func TestImageViewRequiresExactlyOneOfPathAndRef(t *testing.T) {
	dir := t.TempDir()
	writeTestPNG(t, dir, "shot.png", 4, 4)
	tool := (&toolSet{root: dir, images: imageTestStore(t)}).imageViewTool()

	cases := map[string]string{
		"both":    `{"path":"shot.png","ref":"` + strings.Repeat("b", 64) + `"}`,
		"neither": `{}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), registry.ToolCall{
				Name: "image.view",
				Args: json.RawMessage(args),
			})
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !errors.Is(err, registry.ErrInvalidArgs) {
				t.Fatalf("error = %v, want one wrapping registry.ErrInvalidArgs", err)
			}
		})
	}
}

func TestImageViewNotRegisteredWithoutStore(t *testing.T) {
	reg := registry.New()
	// A workspace root is required for registration at all; the store is what
	// image.view's registration is gated on.
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	if _, ok := reg.Lookup("image.view"); ok {
		t.Fatal("image.view must not be registered without an image store")
	}
}

func TestImageViewIsReadOnly(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: t.TempDir(),
		CommandRunner: &fakeRunner{},
		Images:        imageTestStore(t),
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, ok := reg.Lookup("image.view")
	if !ok {
		t.Fatal("image.view is not registered with a store wired")
	}
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("risk = %q, want %q", tool.Risk, registry.RiskReadOnly)
	}
}

// TestImageViewResultCarriesRefShape pins the result contract the transcript
// and the encoders depend on: the ref travels in Images, and Content stays
// text with the hash.
func TestImageViewResultCarriesRefShape(t *testing.T) {
	ref := schema.ImageRef{Hash: strings.Repeat("c", 64), MIME: "image/jpeg", Width: 10, Height: 20, Bytes: 2048, Label: "a diagram"}
	result := imageViewResult(ref, 40, 80, "image/png")
	if len(result.Images) != 1 || result.Images[0] != ref {
		t.Fatalf("Images = %+v, want the ref", result.Images)
	}
	if !strings.Contains(result.Content, "[downscaled from 40×80]") {
		t.Fatalf("content %q lacks the downscale suffix", result.Content)
	}
	if result.Notice == nil || result.Notice.Kind != NoticeImageAdjusted {
		t.Fatalf("notice = %+v, want kind %q", result.Notice, NoticeImageAdjusted)
	}
	if !strings.Contains(result.Notice.Text, "converted from png to jpeg") {
		t.Fatalf("notice text %q lacks the format change", result.Notice.Text)
	}
}
