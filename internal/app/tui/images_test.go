package tui

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/acptest"
	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/clipboard"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/imagepayload"
	"marshal/internal/llm/schema"
)

// imageRecorder is a session that records the images uploaded and the prompts
// sent through it. Every other method is the wrapped session's.
type imageRecorder struct {
	acpclient.Session
	puts    [][]byte
	labels  []string
	prompts []acpwire.PromptParams
}

// Unwrap exposes the adapter this recorder wraps, so stateOf reaches the state
// behind it.
func (r *imageRecorder) Unwrap() acpclient.Session { return r.Session }

func (r *imageRecorder) ImagesPut(ctx context.Context, sessionID string, data []byte, label string) (acpwire.PromptImage, error) {
	r.puts = append(r.puts, data)
	r.labels = append(r.labels, label)
	return r.Session.ImagesPut(ctx, sessionID, data, label)
}

func (r *imageRecorder) Prompt(ctx context.Context, params acpwire.PromptParams) (acpwire.PromptResult, error) {
	r.prompts = append(r.prompts, params)
	return r.Session.Prompt(ctx, params)
}

// fakeClipboardReader is the injected clipboard image reader.
type fakeClipboardReader struct {
	data  []byte
	err   error
	calls int
}

func (r *fakeClipboardReader) ReadImage(ctx context.Context) ([]byte, error) {
	r.calls++
	return r.data, r.err
}

// imageTestRunner is an agent runner that can carry images, so a prompt with
// image refs runs instead of being refused.
type imageTestRunner struct {
	goals  []string
	images [][]schema.ImageRef
}

func (r *imageTestRunner) Run(ctx context.Context, goal string) error {
	return r.RunWithImages(ctx, goal, nil)
}

func (r *imageTestRunner) RunWithImages(ctx context.Context, goal string, images []schema.ImageRef) error {
	r.goals = append(r.goals, goal)
	r.images = append(r.images, images)
	return nil
}

// pngFixture encodes one w×h PNG: the smallest real image the payload store
// accepts.
func pngFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 0x20, G: 0x40, B: 0x60, A: 0xFF})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

// imageStore opens a payload store for one test.
func imageStore(t *testing.T) *imagepayload.Store {
	t.Helper()
	store, err := imagepayload.Open(t.TempDir(), imagepayload.Config{})
	if err != nil {
		t.Fatalf("open image store: %v", err)
	}
	return store
}

// imageModel builds a sized model over the test agent, with an image store and
// the real command registry, whose session is rec and whose clipboard is r.
func imageModel(t *testing.T, rec *imageRecorder, r clipboard.Reader, opts ...Option) Model {
	t.Helper()
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	store := imageStore(t)
	host, sess := acptest.Open(t, service.Deps{
		State:    state,
		Runner:   runnerOf(&imageTestRunner{}),
		Commands: reg,
		Images:   store,
	})
	rec.Session = sess
	options := append([]Option{withImageReader(r)}, opts...)
	m := New(host, rec, options...)
	m.resize(80, 24)
	m.refreshViewport()
	return m
}

// TestCtrlVPasteInsertChip pins the Ctrl+V path: one clipboard read, one
// images.put, and one chip with the ref the session returned.
func TestCtrlVPasteInsertChip(t *testing.T) {
	reader := &fakeClipboardReader{data: pngFixture(t, 4, 3)}
	rec := &imageRecorder{}
	m := imageModel(t, rec, reader)

	m = sendKey(m, tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})

	if reader.calls != 1 {
		t.Fatalf("clipboard reads = %d, want 1", reader.calls)
	}
	if len(rec.puts) != 1 {
		t.Fatalf("images.put calls = %d, want 1", len(rec.puts))
	}
	if len(m.images) != 1 || m.images[0].Hash == "" {
		t.Fatalf("draft images = %+v, want one ref with a hash", m.images)
	}
	if got := m.input.Value(); !strings.Contains(got, "[image #1") || !strings.Contains(got, "4×3") {
		t.Fatalf("composer = %q, want the image chip with its dimensions", got)
	}
}

// TestCtrlVNoImageNotices pins that an empty clipboard is reported, not
// swallowed, and leaves the draft alone.
func TestCtrlVNoImageNotices(t *testing.T) {
	reader := &fakeClipboardReader{err: clipboard.ErrNoImage}
	rec := &imageRecorder{}
	m := imageModel(t, rec, reader)

	m = sendKey(m, tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})

	if len(m.images) != 0 {
		t.Fatalf("draft images = %+v, want none", m.images)
	}
	if strings.Contains(m.input.Value(), "[image #") {
		t.Fatalf("composer = %q, want no chip", m.input.Value())
	}
	if note := lastMessageContent(stateOf(m).Messages()); !strings.Contains(strings.ToLower(note), "clipboard has no image") {
		t.Fatalf("notice = %q, want the empty-clipboard message", note)
	}
}

// TestChipMarksDownscale pins the chip's fields: dimensions, size, label and
// the downscale warning an edge past max_edge earns.
func TestChipMarksDownscale(t *testing.T) {
	rec := &imageRecorder{}
	m := imageModel(t, rec, nil)

	m.attachImage(acpwire.PromptImage{
		Hash:   "h1",
		MIME:   "image/png",
		Width:  4000,
		Height: 3000,
		Bytes:  4 << 20,
		Label:  "shot.png",
	})

	got := m.input.Value()
	for _, want := range []string{"[image #1", "4000×3000", "4.0 MB", "shot.png", "will downscale"} {
		if !strings.Contains(got, want) {
			t.Fatalf("composer = %q, want it to contain %q", got, want)
		}
	}
}

// TestSubmitSendsImageRefs pins that submitting carries the chips' refs and
// only the user's own text, and that the draft clears.
func TestSubmitSendsImageRefs(t *testing.T) {
	reader := &fakeClipboardReader{data: pngFixture(t, 2, 2)}
	rec := &imageRecorder{}
	m := imageModel(t, rec, reader)

	m = sendKey(m, tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if len(m.images) != 1 {
		t.Fatalf("draft images = %+v, want one", m.images)
	}
	want := m.images[0]

	m.input.SetValue("what is this?")
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(rec.prompts) != 1 {
		t.Fatalf("prompts = %+v, want one", rec.prompts)
	}
	got := rec.prompts[0]
	if len(got.Images) != 1 || got.Images[0].Hash != want.Hash {
		t.Fatalf("prompt images = %+v, want the chip's ref %q", got.Images, want.Hash)
	}
	if got.Text != "what is this?" {
		t.Fatalf("prompt text = %q, want the typed line only", got.Text)
	}
	if len(m.images) != 0 {
		t.Fatalf("draft images after submit = %+v, want none", m.images)
	}
	if strings.Contains(m.input.Value(), "[image #") {
		t.Fatalf("composer after submit = %q, want it cleared", m.input.Value())
	}
}

// TestSlashImageAttachesPath pins /image <path>: the file is uploaded and
// lands in the draft as one chip.
func TestSlashImageAttachesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(path, pngFixture(t, 6, 5), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	rec := &imageRecorder{}
	m := imageModel(t, rec, nil)

	m.input.SetValue("/image " + path)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(asModel(t, updated), cmd)

	if len(rec.puts) != 1 {
		t.Fatalf("images.put calls = %d, want 1", len(rec.puts))
	}
	if len(rec.labels) != 1 || rec.labels[0] != "x.png" {
		t.Fatalf("labels = %v, want the file's name", rec.labels)
	}
	if len(m.images) != 1 || m.images[0].Hash == "" {
		t.Fatalf("draft images = %+v, want one ref with a hash", m.images)
	}
	if got := m.input.Value(); !strings.Contains(got, "[image #1") || !strings.Contains(got, "6×5") {
		t.Fatalf("composer = %q, want the image chip with its dimensions", got)
	}
}
