package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// discardLogger is the logger the embedding's tests serve with.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// embeddingState is a session state a bare runtime can serve.
func embeddingState(t *testing.T, id string) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: id})
}

// embeddingPipe is one embedding serving an adopted runtime over a pipe, with
// the client's end dialled.
type embeddingPipe struct {
	emb    *Embedding
	host   *rpc.Host
	served *service.Session
	rt     *app.Runtime
}

// openEmbedding serves rt over a pipe through e and dials the client's end.
func openEmbedding(t *testing.T, rt *app.Runtime, d service.Deps) *embeddingPipe {
	t.Helper()
	servedHost, served := service.New(d)
	emb, err := NewEmbedding(EmbeddingConfig{Logger: discardLogger(), MaxFrameBytes: rpc.MaxFrameBytes})
	if err != nil {
		t.Fatalf("NewEmbedding: %v", err)
	}
	if err := emb.Adopt(rt, servedHost, served); err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	client, agent := rpc.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = emb.Serve(ctx, agent)
	}()
	t.Cleanup(func() {
		_ = client.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the embedding did not stop")
		}
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := emb.Close(closeCtx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	host, err := rpc.Dial(ctx, client, rpc.DialOptions{SharedFilesystem: true})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return &embeddingPipe{emb: emb, host: host, served: served, rt: rt}
}

// attach attaches the client to id.
func (p *embeddingPipe) attach(t *testing.T, id string) acpclient.Session {
	t.Helper()
	sess, err := p.host.Attach(context.Background(), id)
	if err != nil {
		t.Fatalf("attach %q: %v", id, err)
	}
	return sess
}

// TestAnEmbeddingServesTheRuntimeItAdopted pins the embedding's basic path:
// the client attaches to the adopted runtime's ID, reads the status the served
// session answers, and a call it makes reaches the state.
func TestAnEmbeddingServesTheRuntimeItAdopted(t *testing.T) {
	st := embeddingState(t, "s1")
	broker := pubsub.NewBroker[session.Event]()
	st.SetEventBroker(broker)
	rt := &app.Runtime{State: st, SessionID: "s1", EventBroker: broker, Config: config.Default(), WorkingDir: t.TempDir()}
	p := openEmbedding(t, rt, service.Deps{State: st})
	sess := p.attach(t, "s1")

	gotStatus, err := json.Marshal(sess.Status())
	if err != nil {
		t.Fatalf("marshal the client's status: %v", err)
	}
	wantStatus, err := json.Marshal(p.served.Status())
	if err != nil {
		t.Fatalf("marshal the served status: %v", err)
	}
	if !bytes.Equal(gotStatus, wantStatus) {
		t.Errorf("the client's status =\n%s\nwant the served session's\n%s", gotStatus, wantStatus)
	}

	if err := sess.SetNotice(context.Background(), acpwireNotice("hello")); err != nil {
		t.Fatalf("SetNotice: %v", err)
	}
	notice, ok := st.Notice()
	if !ok || notice.Message != "hello" {
		t.Fatalf("the state's notice = %+v (set %t), want %q", notice, ok, "hello")
	}
}

// acpwireNotice is one notice the client sets.
func acpwireNotice(message string) acpwire.Notice {
	return acpwire.Notice{Category: acpwire.NoticeInternal, Severity: "info", Message: message}
}

// TestAnEmbeddingServesTheEmbeddingsTrustPrompt pins decision 5: the adopted
// pair carries the embedding's trust prompt, where a pair built from the
// runtime's ServiceDeps would carry none.
func TestAnEmbeddingServesTheEmbeddingsTrustPrompt(t *testing.T) {
	dir := t.TempDir()
	st := embeddingState(t, "s1")
	broker := pubsub.NewBroker[session.Event]()
	st.SetEventBroker(broker)
	rt := &app.Runtime{State: st, SessionID: "s1", EventBroker: broker, Config: config.Default(), WorkingDir: dir}
	p := openEmbedding(t, rt, service.Deps{State: st, TrustPrompt: dir})
	p.attach(t, "s1")

	if got := p.host.TrustPrompt(); got != dir {
		t.Fatalf("TrustPrompt() = %q, want the embedding's %q", got, dir)
	}
}

// TestAnEmbeddingStartsNoRuntime pins the refusing starter: a session/new that
// names no replacement fails, and the adopted runtime is still the only one
// served.
func TestAnEmbeddingStartsNoRuntime(t *testing.T) {
	st := embeddingState(t, "s1")
	broker := pubsub.NewBroker[session.Event]()
	st.SetEventBroker(broker)
	rt := &app.Runtime{State: st, SessionID: "s1", EventBroker: broker, Config: config.Default(), WorkingDir: t.TempDir()}
	p := openEmbedding(t, rt, service.Deps{State: st})

	// The client's own NewSession refuses without an attached session, so the
	// refusal is pinned where session/new without replaces reaches it: the
	// manager's Create, which calls the refusing starter.
	_, err := p.emb.host.manager.Create(context.Background(), json.RawMessage(`{"cwd":"`+t.TempDir()+`","mcpServers":[]}`))
	if err == nil {
		t.Fatal("session/new = nil error, want the embedding's refusal")
	}
	if !strings.Contains(err.Error(), "embedded mode starts no runtime") {
		t.Fatalf("session/new error = %v, want the refusal's text", err)
	}
	if _, ok := p.emb.Service("s1"); !ok {
		t.Fatal("the adopted session is no longer served")
	}
}

// TestAnEmbeddedReplacementStaysServed pins that a replacement made through the
// embedding's pair is served under its new ID, and the old ID is gone.
func TestAnEmbeddedReplacementStaysServed(t *testing.T) {
	st := embeddingState(t, "s1")
	broker := pubsub.NewBroker[session.Event]()
	st.SetEventBroker(broker)
	rt := &app.Runtime{State: st, SessionID: "s1", EventBroker: broker, Config: config.Default(), WorkingDir: t.TempDir()}
	// The runtime's own NewSession points the runtime at the new state, as
	// Runtime.NewSession does.
	d := service.Deps{
		State: st,
		NewSession: func(name string) (*session.State, error) {
			next := embeddingState(t, "s2")
			next.SetEventBroker(broker)
			rt.State = next
			rt.SessionID = "s2"
			return next, nil
		},
	}
	p := openEmbedding(t, rt, d)
	p.attach(t, "s1")

	next, err := p.host.NewSession(context.Background(), "second")
	if err != nil {
		t.Fatalf("session/new with replaces: %v", err)
	}
	if got := next.ID(); got != "s2" {
		t.Fatalf("the replacement's ID = %q, want %q", got, "s2")
	}
	p.attach(t, "s2")
	if _, ok := p.emb.Service("s2"); !ok {
		t.Fatal("Service(s2) does not serve the replacement")
	}
	if _, ok := p.emb.Service("s1"); ok {
		t.Fatal("Service(s1) still serves the replaced session")
	}
}

// TestAnEmbeddingsCloseKeepsTheRuntime pins decision 5's other half: Close
// releases the service and closes no runtime.
func TestAnEmbeddingsCloseKeepsTheRuntime(t *testing.T) {
	st := embeddingState(t, "s1")
	broker := pubsub.NewBroker[session.Event]()
	st.SetEventBroker(broker)
	rt := &app.Runtime{State: st, SessionID: "s1", EventBroker: broker, Config: config.Default(), WorkingDir: t.TempDir()}
	p := openEmbedding(t, rt, service.Deps{State: st})
	p.attach(t, "s1")

	// The runtime's broker is what Close would take down: a subscription must
	// still deliver after the embedding's own Close.
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	sub := broker.Subscribe(subCtx)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.emb.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.served.Stop(context.Background(), "stop"); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("Stop after Close = %v, want the %q code", err, acpwire.CodeSessionClosed)
	}
	if _, ok := p.emb.Service("s1"); ok {
		t.Fatal("Close left the session's service served")
	}
	st.AddMessage(session.RoleUser, "still here", session.ContentTypePlain)
	// Drain whatever else the service's forwarders published: the signal is the
	// state's own message_added, or the broker closing if Close reached the
	// runtime.
	deadline := time.After(time.Second)
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				t.Fatal("the runtime's event broker was closed: the embedding's Close reached the runtime")
			}
			if ev.Type == session.EventMessageAdded {
				return
			}
		case <-deadline:
			t.Fatal("the state published nothing after Close: the runtime is not the embedding's to close")
		}
	}
}

// TestAdoptRefusesAnEmptyOrTakenID pins adopt's two refusals.
func TestAdoptRefusesAnEmptyOrTakenID(t *testing.T) {
	emb, err := NewEmbedding(EmbeddingConfig{Logger: discardLogger(), MaxFrameBytes: rpc.MaxFrameBytes})
	if err != nil {
		t.Fatalf("NewEmbedding: %v", err)
	}
	st := embeddingState(t, "s1")
	servedHost, served := service.New(service.Deps{State: st})
	if err := emb.Adopt(&app.Runtime{State: st}, servedHost, served); err == nil {
		t.Fatal("adopt with no session ID = nil error, want a refusal")
	}
	rt := &app.Runtime{State: st, SessionID: "s1"}
	if err := emb.Adopt(rt, servedHost, served); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if err := emb.Adopt(rt, servedHost, served); err == nil {
		t.Fatal("adopt of a taken ID = nil error, want a refusal")
	}
}
