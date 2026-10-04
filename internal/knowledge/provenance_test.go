package knowledge

import (
	"context"
	"testing"
	"time"

	"marshal/internal/app/session"
)

func extractWith(t *testing.T, in ExtractInput) {
	t.Helper()
	database, projectID := newTestDB(t)
	state := newTestState(t, t.TempDir())
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	in.DB, in.ProjectID = database, projectID
	in.Messages = state.Messages()
	in.RouteResolver = &fakeRouteResolver{route: knowledgeRoute(), prov: &fakeProvider{response: `{"session_summary":"s","memories":[{"kind":"fact","content":"a fact"}]}`}}
	in.Now = func() time.Time { return time.Unix(200, 0) }
	in.Logger = testLogger()
	Extract(context.Background(), in)
	ms, err := database.GetMemories(projectID)
	if err != nil || len(ms) != 1 {
		t.Fatalf("memories = %v, err %v", ms, err)
	}
	got := ms[0]
	t.Logf("saved %+v", got)
	if in.AgentLabel == "" && got.LearnedAgent != DefaultAgentLabel {
		t.Fatalf("LearnedAgent = %q, want default %q", got.LearnedAgent, DefaultAgentLabel)
	}
	if in.AgentLabel != "" && got.LearnedAgent != in.AgentLabel {
		t.Fatalf("LearnedAgent = %q, want %q", got.LearnedAgent, in.AgentLabel)
	}
	if got.LearnedStep != in.StepID {
		t.Fatalf("LearnedStep = %d, want %d", got.LearnedStep, in.StepID)
	}
}

func TestExtractRecordsLearnedAgentAndStep(t *testing.T) {
	extractWith(t, ExtractInput{AgentLabel: "reviewer #1", StepID: 4})
}

func TestExtractDefaultsAgentLabel(t *testing.T) {
	extractWith(t, ExtractInput{})
}

func TestProvenanceUsesLastStep(t *testing.T) {
	state := newTestState(t, t.TempDir())
	if label, step := Provenance(state); label != DefaultAgentLabel || step != 0 {
		t.Fatalf("empty = %q, %d", label, step)
	}
	state.BeginStep(session.Actor{})
	id := state.BeginStep(session.Actor{Label: "implementer"})
	if label, step := Provenance(state); label != "implementer" || step != id {
		t.Fatalf("got %q, %d; want implementer, %d", label, step, id)
	}
}
