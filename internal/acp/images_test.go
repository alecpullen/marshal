package acp

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/imagepayload"
	"marshal/internal/llm/schema"
)

// pngOnePixel is a 1×1 opaque PNG: the smallest real image the store accepts.
func pngOnePixel(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
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

// imageRunner records the images each run carried and writes the goal message
// the way *agent.Runner does, so a test reads them back from the transcript the
// service produced.
type imageRunner struct {
	state *session.State

	mu     sync.Mutex
	goals  []string
	images [][]schema.ImageRef
	err    error
}

func (r *imageRunner) Run(ctx context.Context, goal string) error {
	return r.record(goal, nil)
}

func (r *imageRunner) RunWithImages(ctx context.Context, goal string, images []schema.ImageRef) error {
	return r.record(goal, images)
}

func (r *imageRunner) record(goal string, images []schema.ImageRef) error {
	r.mu.Lock()
	r.goals = append(r.goals, goal)
	r.images = append(r.images, images)
	err := r.err
	r.mu.Unlock()
	r.state.AddMessageWithImages(session.RoleUser, goal, session.ContentTypePlain, images)
	return err
}

// seen returns the goals and the images each run was given.
func (r *imageRunner) seen() ([]string, [][]schema.ImageRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.goals...), append([][]schema.ImageRef(nil), r.images...)
}

// TestImagesPutThenPrompt puts a real PNG, prompts with the ref it returned,
// and reads the images back off the turn's own message.
func TestImagesPutThenPrompt(t *testing.T) {
	store := imageStore(t)
	state := seamState(t, controlSession)
	runner := &imageRunner{state: state}
	conn, _ := startControlAgent(t, true, service.Deps{
		State:  state,
		Runner: func() service.Runner { return runner },
		Images: store,
	}, nil)

	var put acpwire.ImagesPutResult
	if err := conn.Call(context.Background(), acpwire.MethodImagesPut, acpwire.ImagesPutParams{
		SessionID: controlSession,
		Data:      base64.StdEncoding.EncodeToString(pngOnePixel(t)),
		Label:     "shot of the ui",
	}, &put); err != nil {
		t.Fatalf("session/images_put: %v", err)
	}
	if put.Image.Hash == "" {
		t.Fatal("session/images_put returned no hash")
	}
	if put.Image.Label != "shot of the ui" {
		t.Fatalf("label = %q, want the one uploaded", put.Image.Label)
	}
	if !strings.HasPrefix(put.Image.MIME, "image/") {
		t.Fatalf("mime = %q, want an image type", put.Image.MIME)
	}

	var res acpwire.PromptResult
	if err := conn.Call(context.Background(), acpwire.MethodPrompt, acpwire.PromptCallParams{
		SessionID: controlSession,
		Prompt:    []acpwire.TextBlock{{Type: "text", Text: "what is this?"}},
		Images:    []acpwire.PromptImage{put.Image},
	}, &res); err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	if res.StopReason != acpwire.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", res.StopReason, acpwire.StopReasonEndTurn)
	}

	goals, images := runner.seen()
	if len(goals) != 1 || goals[0] != "what is this?" {
		t.Fatalf("the runner saw %v, want one turn with the prompt's text", goals)
	}
	if len(images) != 1 || len(images[0]) != 1 {
		t.Fatalf("the runner was given %v, want exactly one image", images)
	}
	if got := images[0][0]; got.Hash != put.Image.Hash || got.MIME != put.Image.MIME {
		t.Fatalf("the runner was given %+v, want the ref session/images_put returned", got)
	}

	// The turn's own message carries the images, so the next turn replays them.
	var carried []schema.ImageRef
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && m.Content == "what is this?" {
			carried = m.Images
		}
	}
	if len(carried) != 1 || carried[0].Hash != put.Image.Hash {
		t.Fatalf("the prompt's message carries %v, want the uploaded ref", carried)
	}
}

// TestPromptWithUnknownRef fails the prompt before the turn starts when the
// hash is not in the store, with a plain message — not a vision refusal.
func TestPromptWithUnknownRef(t *testing.T) {
	state := seamState(t, controlSession)
	runner := &imageRunner{state: state}
	conn, _ := startControlAgent(t, true, service.Deps{
		State:  state,
		Runner: func() service.Runner { return runner },
		Images: imageStore(t),
	}, nil)

	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var res acpwire.PromptResult
	err := conn.Call(context.Background(), acpwire.MethodPrompt, acpwire.PromptCallParams{
		SessionID: controlSession,
		Prompt:    []acpwire.TextBlock{{Type: "text", Text: "look at this"}},
		Images:    []acpwire.PromptImage{{Hash: hash, MIME: "image/png", Width: 1, Height: 1}},
	}, &res)
	if err == nil {
		t.Fatal("a prompt with an unknown image hash answered no error")
	}
	if want := "image " + hash + " is not in the store"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to carry %q", err, want)
	}
	if acpclient.IsCode(err, acpwire.CodeVisionUnsupported) {
		t.Fatalf("err = %v, want a store failure rather than a vision refusal", err)
	}
	if goals, _ := runner.seen(); len(goals) != 0 {
		t.Fatalf("the runner ran %v, want the prompt to fail before the turn", goals)
	}
}

// TestPromptRefusedWithoutVision is the route's refusal as the client sees it:
// the code is vision_unsupported, the message is the preset line, and the text
// does not go back on the steering queue.
func TestPromptRefusedWithoutVision(t *testing.T) {
	store := imageStore(t)
	state := seamState(t, controlSession)
	ref, err := store.Put(context.Background(), bytes.NewReader(pngOnePixel(t)), imagepayload.Options{})
	if err != nil {
		t.Fatalf("put fixture: %v", err)
	}
	const detail = `model llama-text has vision off; set [models.presets.llama-text] vision = "on" or switch model`
	runner := &imageRunner{state: state, err: &schema.VisionError{
		Model:  "llama-text",
		Reason: schema.VisionReasonPresetOff,
		Detail: detail,
	}}
	conn, _ := startControlAgent(t, true, service.Deps{
		State:  state,
		Runner: func() service.Runner { return runner },
		Images: store,
	}, nil)

	var res acpwire.PromptResult
	err = conn.Call(context.Background(), acpwire.MethodPrompt, acpwire.PromptCallParams{
		SessionID: controlSession,
		Prompt:    []acpwire.TextBlock{{Type: "text", Text: "what is this?"}},
		Queued:    true,
		Images:    []acpwire.PromptImage{{Hash: ref.Hash, MIME: ref.MIME, Width: ref.Width, Height: ref.Height, Bytes: ref.Bytes}},
	}, &res)
	if err == nil {
		t.Fatal("a prompt refused for vision answered no error")
	}
	if !acpclient.IsCode(err, acpwire.CodeVisionUnsupported) {
		t.Fatalf("err = %v, want CodeVisionUnsupported", err)
	}
	if !strings.Contains(err.Error(), detail) {
		t.Fatalf("err = %v, want it to carry %q", err, detail)
	}
	if q := state.SteeringQueue(); len(q) != 0 {
		t.Fatalf("steering queue = %v, want the refused text kept off it", q)
	}
}

// TestPromptWithImagesOnAPlainRunner: a runner with no image-carrying run
// refuses the prompt rather than dropping the images.
func TestPromptWithImagesOnAPlainRunner(t *testing.T) {
	store := imageStore(t)
	state := seamState(t, controlSession)
	ref, err := store.Put(context.Background(), bytes.NewReader(pngOnePixel(t)), imagepayload.Options{})
	if err != nil {
		t.Fatalf("put fixture: %v", err)
	}
	runner := newControlRunner()
	conn, _ := startControlAgent(t, true, service.Deps{
		State:  state,
		Runner: func() service.Runner { return runner },
		Images: store,
	}, nil)

	var res acpwire.PromptResult
	err = conn.Call(context.Background(), acpwire.MethodPrompt, acpwire.PromptCallParams{
		SessionID: controlSession,
		Prompt:    []acpwire.TextBlock{{Type: "text", Text: "what is this?"}},
		Images:    []acpwire.PromptImage{{Hash: ref.Hash, MIME: ref.MIME}},
	}, &res)
	if err == nil {
		t.Fatal("a prompt carrying images reached a runner that cannot take them")
	}
	if !acpclient.IsCode(err, acpwire.CodeVisionUnsupported) {
		t.Fatalf("err = %v, want CodeVisionUnsupported", err)
	}
	if !strings.Contains(err.Error(), "has vision off") {
		t.Fatalf("err = %v, want the vision wording", err)
	}
	if goals := runner.goalsSeen(); len(goals) != 0 {
		t.Fatalf("the plain runner ran %v, want it never to run", goals)
	}
}
