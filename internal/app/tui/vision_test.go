package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/acptest"
	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
)

// visionRefusal is the refusal a text-only route answers a prompt carrying
// images with, worded as the runner words it.
const visionRefusal = `model m has vision off; set vision = "on" or switch model`

// visionRecorder is a session that refuses prompts carrying images and records
// the send-anyway calls and the prompts sent through it. Every other method is
// the wrapped session's.
type visionRecorder struct {
	acpclient.Session
	refuse  bool
	forces  []string
	prompts []acpwire.PromptParams
}

// Unwrap exposes the adapter this recorder wraps, so stateOf reaches the state
// behind it.
func (r *visionRecorder) Unwrap() acpclient.Session { return r.Session }

func (r *visionRecorder) Prompt(ctx context.Context, params acpwire.PromptParams) (acpwire.PromptResult, error) {
	r.prompts = append(r.prompts, params)
	if r.refuse {
		return acpwire.PromptResult{}, &acpwire.Error{Code: acpwire.CodeVisionUnsupported, Message: visionRefusal}
	}
	return r.Session.Prompt(ctx, params)
}

func (r *visionRecorder) ForceVision(ctx context.Context, provider, model string) error {
	r.forces = append(r.forces, provider+"/"+model)
	return r.Session.ForceVision(ctx, provider, model)
}

// visionModel builds a sized model over the test agent whose session is rec,
// with route as its active route. A zero route leaves the session without one.
func visionModel(t *testing.T, rec *visionRecorder, route session.RouteInfo) Model {
	t.Helper()
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	if route.Provider != "" || route.Model != "" {
		state.SetActiveRoute(route)
	}
	host, sess := acptest.Open(t, service.Deps{
		State:    state,
		Runner:   runnerOf(&imageTestRunner{}),
		Commands: reg,
	})
	rec.Session = sess
	m := New(host, rec)
	m.resize(80, 24)
	m.refreshViewport()
	return m
}

// visionDraftModel attaches one image, types a line and submits it, leaving the
// model holding the refusal the recorder answers with.
func visionDraftModel(t *testing.T, rec *visionRecorder, route session.RouteInfo) Model {
	t.Helper()
	m := visionModel(t, rec, route)
	m.input.SetValue("what is this?")
	m.attachImage(acpwire.PromptImage{Hash: "h1", MIME: "image/png", Width: 4, Height: 3, Bytes: 128})
	return sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// TestVisionRefusalKeepsDraft pins that a vision refusal is not a failed run:
// the composer keeps the line and the chip, and the notice is the refusal's
// own message naming the cause and the fix.
func TestVisionRefusalKeepsDraft(t *testing.T) {
	rec := &visionRecorder{refuse: true}
	m := visionDraftModel(t, rec, session.RouteInfo{Active: true, Provider: "p", Model: "m"})

	if len(rec.prompts) != 1 {
		t.Fatalf("prompts = %+v, want one", rec.prompts)
	}
	if got := m.input.Value(); !strings.Contains(got, "what is this?") || !strings.Contains(got, "[image #1") {
		t.Fatalf("composer = %q, want the refused line and its chip back", got)
	}
	if len(m.images) != 1 || m.images[0].Hash != "h1" {
		t.Fatalf("draft images = %+v, want the refused ref", m.images)
	}
	if note := lastMessageContent(stateOf(m).Messages()); !strings.Contains(note, "cannot see images") || !strings.Contains(note, "vision off") {
		t.Fatalf("notice = %q, want the refusal and its fix", note)
	}
	if m.busy {
		t.Fatal("busy = true after a refusal, want the model idle")
	}
}

// TestVisionRefusalSendAnyway pins the safety valve: the send-anyway key
// records the override on the route's provider and model, then resends the
// same text and the same refs.
func TestVisionRefusalSendAnyway(t *testing.T) {
	rec := &visionRecorder{refuse: true}
	m := visionDraftModel(t, rec, session.RouteInfo{Active: true, Provider: "p", Model: "m"})

	m = sendKey(m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModAlt})

	if want := []string{"p/m"}; len(rec.forces) != 1 || rec.forces[0] != want[0] {
		t.Fatalf("force calls = %v, want %v", rec.forces, want)
	}
	if !stateOf(m).VisionForced("p", "m") {
		t.Fatal("the override did not reach the session state")
	}
	if len(rec.prompts) != 2 {
		t.Fatalf("prompts = %+v, want the refusal and the resend", rec.prompts)
	}
	first, second := rec.prompts[0], rec.prompts[1]
	if second.Text != first.Text {
		t.Fatalf("resend text = %q, want %q", second.Text, first.Text)
	}
	if len(second.Images) != 1 || second.Images[0].Hash != first.Images[0].Hash {
		t.Fatalf("resend images = %+v, want the same refs %+v", second.Images, first.Images)
	}
}

// TestVisionRefusalWithoutRouteArmsNoKey pins that the key does nothing when
// the session has no active route: there is nothing to force, so no call is
// made and the draft stays put.
func TestVisionRefusalWithoutRouteArmsNoKey(t *testing.T) {
	rec := &visionRecorder{refuse: true}
	m := visionDraftModel(t, rec, session.RouteInfo{})

	m = sendKey(m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModAlt})

	if len(rec.forces) != 0 {
		t.Fatalf("force calls = %v, want none", rec.forces)
	}
	if len(rec.prompts) != 1 {
		t.Fatalf("prompts = %+v, want the refusal only", rec.prompts)
	}
	if got := m.input.Value(); !strings.Contains(got, "what is this?") {
		t.Fatalf("composer = %q, want the draft kept", got)
	}
	if len(m.images) != 1 || m.images[0].Hash != "h1" {
		t.Fatalf("draft images = %+v, want the ref kept", m.images)
	}
}
