package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/session"
)

func TestLastRequest(t *testing.T) {
	s := newSyncStack(t)
	got, err := s.LastRequest(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.(map[string]any)["request"]; !ok || v != nil {
		t.Fatalf("no request recorded: got %v", got)
	}

	s.st.SetRequestInspection(session.RequestInspection{
		At: time.Now(), Provider: "ollama", Model: "m1",
		Messages: []session.InspectionMessage{{Role: "user", Content: "hi"}},
	})
	got, err = s.LastRequest(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	r := got.(map[string]any)["request"].(RequestJSON)
	if r.Provider != "ollama" || r.Model != "m1" || len(r.Messages) != 1 || r.Messages[0].Content != "hi" || r.Outcome.Status != "dispatched" {
		t.Fatalf("request = %+v", r)
	}
}
