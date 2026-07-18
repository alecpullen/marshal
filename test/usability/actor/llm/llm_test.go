package llm

import (
	"context"
	"testing"

	"marshal/test/usability/actor"
	"marshal/test/usability/screen"
)

type fakeClient struct {
	responses []string
	pos       int
}

func (f *fakeClient) Complete(ctx context.Context, system, prompt string) (string, error) {
	if f.pos >= len(f.responses) {
		return `{"action":"done","success":true}`, nil
	}
	r := f.responses[f.pos]
	f.pos++
	return r, nil
}

func TestLLMActorTypesAndDone(t *testing.T) {
	client := &fakeClient{responses: []string{
		`{"action":"type","text":"hello"}`,
	}}
	a := New(Config{MaxIterations: 5}, client)

	act, err := a.Act(context.Background(), screen.Screen{Content: "prompt ❯ "})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != actor.ActionType || act.Text != "hello" {
		t.Fatalf("first action = %+v, want type 'hello'", act)
	}

	act, err = a.Act(context.Background(), screen.Screen{Content: "prompt ❯ hello"})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != actor.ActionDone || !act.Success {
		t.Fatalf("final action = %+v, want done success", act)
	}
}
