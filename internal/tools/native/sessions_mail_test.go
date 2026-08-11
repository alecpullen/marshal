package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func newTestToolSetWithSessions(t *testing.T) (*toolSet, *db.DB) {
	t.Helper()
	tmp := t.TempDir()
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	state := session.New(config.Default(), tmp, time.Unix(100, 0).UTC(), session.Persistence{DB: dbConn, SessionID: "current", Logger: nil})
	ts, err := newToolSet(Options{
		WorkspaceRoot: tmp,
		DB:            dbConn,
		ProjectID:     projectID,
		SessionState:  state,
	})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	return ts, dbConn
}

func TestSessionsListTool(t *testing.T) {
	ts, dbConn := newTestToolSetWithSessions(t)

	if err := dbConn.CreateSession("sess-old", ts.projectID, "old session", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CreateSession old: %v", err)
	}
	if err := dbConn.CreateSession("sess-new", ts.projectID, "new session", time.Unix(200, 0).UTC()); err != nil {
		t.Fatalf("CreateSession new: %v", err)
	}
	if err := dbConn.EndSession("sess-old", time.Unix(300, 0).UTC(), "done"); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := dbConn.SaveMessage("sess-new", "user", "hi", "plain", time.Unix(250, 0).UTC(), "", 0, false, 0); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	tool := ts.sessionsListTool()
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"limit":10}`)})
	if err != nil {
		t.Fatalf("sessions.list: %v", err)
	}
	if !strings.Contains(res.Content, "sess-new") || !strings.Contains(res.Content, "new session") {
		t.Fatalf("expected new session in output, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "live") {
		t.Fatalf("expected live flag for unended session, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "ended") {
		t.Fatalf("expected ended flag for old session, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "messages=1") {
		t.Fatalf("expected message count, got:\n%s", res.Content)
	}
}

func TestSessionSendTool_Broadcast(t *testing.T) {
	ts, _ := newTestToolSetWithSessions(t)
	tool := ts.sessionSendTool()
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"body":"hello all"}`)})
	if err != nil {
		t.Fatalf("session.send broadcast: %v", err)
	}
	if !strings.Contains(res.Content, "Broadcast") {
		t.Fatalf("expected broadcast confirmation, got %q", res.Content)
	}
}

func TestSessionSendTool_ByTitle(t *testing.T) {
	ts, _ := newTestToolSetWithSessions(t)
	if err := ts.db.CreateSession("target", ts.projectID, "helper", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CreateSession target: %v", err)
	}

	tool := ts.sessionSendTool()
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"to":"helper","body":"hi helper"}`)})
	if err != nil {
		t.Fatalf("session.send by title: %v", err)
	}
	if !strings.Contains(res.Content, "target") {
		t.Fatalf("expected target session in confirmation, got %q", res.Content)
	}

	// Unknown recipient.
	_, err = tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"to":"nobody","body":"hi"}`)})
	if err == nil || !strings.Contains(err.Error(), "no session matches") {
		t.Fatalf("expected unknown recipient error, got %v", err)
	}
}

func TestSessionSendTool_AmbiguousTitle(t *testing.T) {
	ts, _ := newTestToolSetWithSessions(t)
	if err := ts.db.CreateSession("a", ts.projectID, "helper", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CreateSession a: %v", err)
	}
	if err := ts.db.CreateSession("b", ts.projectID, "helper", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("CreateSession b: %v", err)
	}

	tool := ts.sessionSendTool()
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"to":"helper","body":"hi"}`)})
	if err == nil || !strings.Contains(err.Error(), "multiple sessions match") {
		t.Fatalf("expected ambiguous title error, got %v", err)
	}
}
