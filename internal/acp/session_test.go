package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func TestSessionCreateReturnsSessionID(t *testing.T) {
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return &app.Runtime{
				SessionID: "sess_test",
				State:     session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}),
			}, nil
		},
	})
	got, err := m.Create(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	resp, ok := got.(SessionResponse)
	if !ok {
		t.Fatalf("Create() result type = %T, want SessionResponse", got)
	}
	if resp.SessionID != "sess_test" {
		t.Fatalf("SessionID = %q, want sess_test", resp.SessionID)
	}
}

func TestSessionCreateStoresRuntime(t *testing.T) {
	var called int
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			called++
			return &app.Runtime{SessionID: "sess_xyz"}, nil
		},
	})
	if _, err := m.Create(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	rt, ok := m.Get("sess_xyz")
	if !ok {
		t.Fatalf("Get() ok = false, want true")
	}
	if rt.SessionID != "sess_xyz" {
		t.Fatalf("rt.SessionID = %q", rt.SessionID)
	}
	if called != 1 {
		t.Fatalf("StartRuntime called %d times, want 1", called)
	}
}

func TestSessionLoadPassesRequestedID(t *testing.T) {
	var gotOpts []app.Option
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			gotOpts = opts
			return &app.Runtime{SessionID: "sess_client_supplied"}, nil
		},
	})
	if _, err := m.Load(context.Background(), json.RawMessage(`{"sessionId":"sess_client_supplied"}`)); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(gotOpts) == 0 {
		t.Fatalf("StartRuntime received no options")
	}
}
