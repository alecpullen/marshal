package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/activity"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestPublicProgressToolRequiresResponseReferenceAndAppliesUpdate(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	tool := PublicProgressTool(state)
	args := json.RawMessage(`{"mode":"begin","headline":"Inspecting the change"}`)
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: args}); err == nil || !strings.Contains(err.Error(), "response reference") {
		t.Fatalf("handler without response reference error = %v", err)
	}
	state.AddMessage(session.RoleUser, "progress test", session.ContentTypePlain)
	messageID := state.Messages()[0].ID
	state.BeginActivityRun(messageID)
	response := state.BeginActivityResponse()
	ctx := activity.WithRef(context.Background(), response)
	result, err := tool.Handler(ctx, registry.ToolCall{ID: "p1", Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.Summary == "" || len(state.ActivitySnapshot().ProgressRevisions) != 1 {
		t.Fatalf("result=%+v progress=%+v", result, state.ActivitySnapshot().ProgressRevisions)
	}
}

func TestRegisterAllAdvertisesPublicProgressOnlyWithSessionState(t *testing.T) {
	root := t.TempDir()
	withoutState := registry.New()
	if err := RegisterAll(withoutState, Options{WorkspaceRoot: root}); err != nil {
		t.Fatal(err)
	}
	if _, ok := withoutState.Lookup("progress.update"); ok {
		t.Fatal("progress.update registered without a session state")
	}
	withState := registry.New()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})
	if err := RegisterAll(withState, Options{WorkspaceRoot: root, SessionState: state}); err != nil {
		t.Fatal(err)
	}
	tool, ok := withState.Lookup("progress.update")
	if !ok || tool.Deferred || tool.Cacheable {
		t.Fatalf("progress capability = %+v, registered=%v", tool, ok)
	}
}
