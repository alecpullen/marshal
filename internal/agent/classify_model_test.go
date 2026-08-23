package agent

import (
	"context"
	"testing"

	"marshal/internal/agent/agenttest"
)

func TestNewModelClassifierParsesClasses(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  TaskClass
	}{
		{"question", ClassQuestion},
		{"Edit", ClassEdit},
		{" command ", ClassCommand},
		{"EDIT\n", ClassEdit},
	} {
		p := &agenttest.ScriptedProvider{Responses: []string{tc.reply}}
		classify := NewModelClassifier(p, "router-model")
		got, err := classify(context.Background(), "some goal")
		if err != nil {
			t.Fatalf("reply %q: err = %v", tc.reply, err)
		}
		if got != tc.want {
			t.Fatalf("reply %q: class = %q, want %q", tc.reply, got, tc.want)
		}
	}
}

func TestNewModelClassifierRejectsGarbage(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"I think this is an edit because..."}}
	classify := NewModelClassifier(p, "router-model")
	if _, err := classify(context.Background(), "goal"); err == nil {
		t.Fatal("expected error for unrecognized reply, got nil")
	}
}
