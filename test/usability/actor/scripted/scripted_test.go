package scripted

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"marshal/test/usability/screen"
)

func TestScriptedTypeStep(t *testing.T) {
	scr := &Scripted{
		Name: "open help",
		Steps: []Step{
			{Send: "?", WaitFor: WaitFor{ScreenContains: "prompt ❯ "}},
			{SendKey: "esc", WaitFor: WaitFor{State: UIStatePredicate{HelpOpen: boolPtr(false)}}},
		},
	}

	// step 1: type ?
	act, err := scr.Act(context.Background(), screen.Screen{Content: "prompt ❯ "})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != "type" || act.Text != "?" {
		t.Fatalf("first action = %+v, want type '?", act)
	}

	// step 2: screen contains help -> send esc
	act, err = scr.Act(context.Background(), screen.Screen{Content: "marshal keys"})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != "key" || act.Key != "esc" {
		t.Fatalf("second action = %+v, want key esc", act)
	}

	// step 3: help closed -> done
	act, err = scr.Act(context.Background(), screen.Screen{Content: "prompt ❯ "})
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != "done" || !act.Success {
		t.Fatalf("final action = %+v, want done success", act)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestLoad(t *testing.T) {
	json := `{"name":"open help","steps":[{"send":"?","wait_for":{"screen_contains":"prompt"}},{"send_key":"esc","wait_for":{"state":{"help_open":false}}}]}`
	dir := t.TempDir()
	path := filepath.Join(dir, "script.json")
	if err := os.WriteFile(path, []byte(json), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	scr, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if scr.Name != "open help" {
		t.Fatalf("Name = %q, want open help", scr.Name)
	}
	if len(scr.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(scr.Steps))
	}
	if scr.Steps[0].Send != "?" || scr.Steps[0].WaitFor.ScreenContains != "prompt" {
		t.Fatalf("first step = %+v, want send ? wait prompt", scr.Steps[0])
	}
	if scr.Steps[1].SendKey != "esc" || scr.Steps[1].WaitFor.State.HelpOpen == nil || *scr.Steps[1].WaitFor.State.HelpOpen != false {
		t.Fatalf("second step = %+v, want key esc wait help_open=false", scr.Steps[1])
	}
}
