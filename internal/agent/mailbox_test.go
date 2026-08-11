package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestRunTaskDeliversSessionMail(t *testing.T) {
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	recipientID := "sess-recipient"
	if err := dbConn.CreateSession(recipientID, projectID, "recipient", time.Unix(100, 0).UTC()); err != nil {
		t.Fatalf("create recipient session: %v", err)
	}
	if err := dbConn.SendMail(projectID, "sess-sender", recipientID, "handoff context", time.Unix(200, 0).UTC()); err != nil {
		t.Fatalf("send mail: %v", err)
	}

	state := session.New(config.Default(), "/repo", time.Unix(300, 0).UTC(), session.Persistence{DB: dbConn, SessionID: recipientID, Logger: nil})
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"Ack."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "hello"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}

	msgs := state.Messages()
	var found bool
	for _, m := range msgs {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "Mail from other sessions") && strings.Contains(m.Content, "handoff context") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected system mail message in transcript, got: %+v", msgs)
	}

	unread, err := dbConn.UnreadMail(recipientID)
	if err != nil {
		t.Fatalf("UnreadMail: %v", err)
	}
	if len(unread) != 0 {
		t.Fatalf("expected mail marked read, got %d unread", len(unread))
	}
}

func TestRunTaskNoMailWithoutDB(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0).UTC(), session.Persistence{})
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"done","action":{"type":"final","content":"Ack."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "hello"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}

	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "Mail from other sessions") {
			t.Fatalf("unexpected mail message without DB: %q", m.Content)
		}
	}
}
