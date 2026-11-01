package scripted

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"marshal/test/usability/actor"
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

func strPtr(s string) *string { return &s }

// TestScriptedPasteAndResize pins the paste/resize action mapping, their wait
// gating, the empty-paste payload, and the exactly-one-action rejection.
func TestScriptedPasteAndResize(t *testing.T) {
	t.Run("PayloadAndGating", func(t *testing.T) {
		const payload = "héllo ☃"
		scr := &Scripted{
			Steps: []Step{
				{Paste: strPtr(payload), WaitFor: WaitFor{ScreenContains: "prompt"}},
				{Resize: &actor.Size{Width: 100, Height: 30}, WaitFor: WaitFor{ScreenContains: "SIZE"}},
				{Paste: strPtr(""), WaitFor: WaitFor{ScreenContains: "PASTE-END"}},
			},
		}

		// Wait gating: an unmatched condition yields a no-op and stays put.
		act, err := scr.Act(context.Background(), screen.Screen{Content: "loading"})
		if err != nil {
			t.Fatalf("Act (unmatched paste): %v", err)
		}
		if act.Type != actor.ActionNoOp {
			t.Fatalf("unmatched paste action = %+v, want noop", act)
		}
		if scr.pos != 0 {
			t.Fatalf("pos = %d after unmatched paste step, want 0", scr.pos)
		}

		// Matched: the paste action carries the payload verbatim.
		act, err = scr.Act(context.Background(), screen.Screen{Content: "prompt ❯ "})
		if err != nil {
			t.Fatalf("Act (paste): %v", err)
		}
		if act.Type != actor.ActionPaste || act.Text != payload {
			t.Fatalf("paste action = %+v, want type %q payload %q", act, actor.ActionPaste, payload)
		}
		if act.Width != 0 || act.Height != 0 {
			t.Fatalf("paste action = %+v, want no resize dimensions", act)
		}
		if scr.pos != 1 {
			t.Fatalf("pos = %d after paste step, want 1", scr.pos)
		}

		// The resize step is gated too.
		act, err = scr.Act(context.Background(), screen.Screen{Content: "prompt ❯ PASTE:héllo"})
		if err != nil {
			t.Fatalf("Act (unmatched resize): %v", err)
		}
		if act.Type != actor.ActionNoOp || scr.pos != 1 {
			t.Fatalf("unmatched resize action = %+v pos = %d, want noop at step 1", act, scr.pos)
		}

		// Matched: the resize action carries the dimensions and no text.
		act, err = scr.Act(context.Background(), screen.Screen{Content: "SIZE:30x100"})
		if err != nil {
			t.Fatalf("Act (resize): %v", err)
		}
		if act.Type != actor.ActionResize || act.Width != 100 || act.Height != 30 {
			t.Fatalf("resize action = %+v, want type %q size 100x30", act, actor.ActionResize)
		}
		if act.Text != "" {
			t.Fatalf("resize action text = %q, want empty", act.Text)
		}
		if scr.pos != 2 {
			t.Fatalf("pos = %d after resize step, want 2", scr.pos)
		}

		// An empty paste is a real paste: the pointer is set with an empty
		// payload, so it must still emit a paste action.
		act, err = scr.Act(context.Background(), screen.Screen{Content: "PASTE-END"})
		if err != nil {
			t.Fatalf("Act (empty paste): %v", err)
		}
		if act.Type != actor.ActionPaste || act.Text != "" {
			t.Fatalf("empty paste action = %+v, want type %q with an empty payload", act, actor.ActionPaste)
		}
		if scr.pos != 3 {
			t.Fatalf("pos = %d after empty paste step, want 3", scr.pos)
		}

		act, err = scr.Act(context.Background(), screen.Screen{Content: "PASTE-END"})
		if err != nil {
			t.Fatalf("Act (completed): %v", err)
		}
		if act.Type != actor.ActionDone || !act.Success {
			t.Fatalf("completed action = %+v, want done success", act)
		}
	})

	t.Run("MixedActionsRejected", func(t *testing.T) {
		cases := []struct {
			name string
			step Step
		}{
			{"paste plus send", Step{Paste: strPtr("x"), Send: "x"}},
			{"paste plus send_key", Step{Paste: strPtr("x"), SendKey: "esc"}},
			{"paste plus wait_only", Step{Paste: strPtr(""), WaitOnly: true, WaitFor: WaitFor{ScreenContains: "Help"}}},
			{"paste plus resize", Step{Paste: strPtr("x"), Resize: &actor.Size{Width: 80, Height: 24}}},
			{"resize plus send", Step{Resize: &actor.Size{Width: 80, Height: 24}, Send: "x"}},
			{"resize plus wait_only", Step{Resize: &actor.Size{Width: 80, Height: 24}, WaitOnly: true, WaitFor: WaitFor{ScreenContains: "Help"}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				scr := &Scripted{Steps: []Step{tc.step}}
				// The screen matches whatever the step waits for, so the
				// failure comes from the step's own validation.
				act, err := scr.Act(context.Background(), screen.Screen{Content: "Help"})
				if err == nil {
					t.Fatalf("Act error = nil, action = %+v; want a validation error", act)
				}
				if scr.pos != 0 {
					t.Fatalf("pos = %d after invalid step, want 0", scr.pos)
				}
			})
		}
	})
}

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

// TestScriptedWaitOnlyPostcondition drives a wait-only step through its three
// states: an unmatched condition stays put and emits no-op, a matched one
// emits no-op and advances, and the following observation reports done.
func TestScriptedWaitOnlyPostcondition(t *testing.T) {
	scr := &Scripted{
		Steps: []Step{
			{WaitOnly: true, WaitFor: WaitFor{ScreenContains: "Type a question", ScreenNotContains: "Help"}},
		},
	}

	// Unmatched: Help is still visible, so the postcondition fails and the
	// actor must keep waiting on the same step.
	open := screen.Screen{Content: "Help\n  /help cheatsheet"}
	act, err := scr.Act(context.Background(), open)
	if err != nil {
		t.Fatalf("Act (unmatched): %v", err)
	}
	if act.Type != actor.ActionNoOp {
		t.Fatalf("unmatched action = %+v, want noop", act)
	}
	if scr.pos != 0 {
		t.Fatalf("pos = %d after unmatched wait-only step, want 0", scr.pos)
	}

	// Matched: both predicates hold, so the step advances and stays a no-op.
	closed := screen.Screen{Content: "Type a question"}
	act, err = scr.Act(context.Background(), closed)
	if err != nil {
		t.Fatalf(" Act (matched): %v", err)
	}
	if act.Type != actor.ActionNoOp {
		t.Fatalf("matched action = %+v, want noop", act)
	}
	if scr.pos != 1 {
		t.Fatalf("pos = %d after matched wait-only step, want 1", scr.pos)
	}

	// Completed: the next observation has no steps left.
	act, err = scr.Act(context.Background(), closed)
	if err != nil {
		t.Fatalf("Act (completed): %v", err)
	}
	if act.Type != actor.ActionDone || !act.Success {
		t.Fatalf("completed action = %+v, want done success", act)
	}
}

// TestScriptedWaitOnlyRequiresAllPredicates pins the conjunctive contract: a
// screen that satisfies screen_contains but not screen_not_contains is not a
// match.
func TestScriptedWaitOnlyRequiresAllPredicates(t *testing.T) {
	scr := &Scripted{
		Steps: []Step{
			{WaitOnly: true, WaitFor: WaitFor{ScreenContains: "Type a question", ScreenNotContains: "Help"}},
		},
	}
	both := screen.Screen{Content: "Help\nType a question"}
	act, err := scr.Act(context.Background(), both)
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if act.Type != actor.ActionNoOp || scr.pos != 0 {
		t.Fatalf("action = %+v pos = %d, want noop at step 0", act, scr.pos)
	}
}

// TestScriptedRejectsInvalidWaitOnly covers ambiguous and empty action
// combinations.
func TestScriptedRejectsInvalidWaitOnly(t *testing.T) {
	cases := []struct {
		name string
		step Step
	}{
		{"wait_only without condition", Step{WaitOnly: true}},
		{"wait_only plus send", Step{WaitOnly: true, Send: "?", WaitFor: WaitFor{ScreenContains: "Help"}}},
		{"wait_only plus send_key", Step{WaitOnly: true, SendKey: "esc", WaitFor: WaitFor{ScreenContains: "Help"}}},
		{"no action at all", Step{WaitFor: WaitFor{ScreenContains: "Help"}}},
		{"send and send_key", Step{Send: "?", SendKey: "esc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scr := &Scripted{Steps: []Step{tc.step}}
			// The screen matches whatever the step waits for, so any failure
			// comes from the step's own validation.
			act, err := scr.Act(context.Background(), screen.Screen{Content: "Help"})
			if err == nil {
				t.Fatalf("Act error = nil, action = %+v; want a validation error", act)
			}
			if scr.pos != 0 {
				t.Fatalf("pos = %d after invalid step, want 0", scr.pos)
			}
		})
	}
}

// TestLoadWaitOnlyPostcondition decodes a wait-only step from JSON and
// confirms the existing (send-based) scripts still decode.
func TestLoadWaitOnlyPostcondition(t *testing.T) {
	data := `{"name":"help_close_wait","steps":[
		{"send":"?","wait_for":{"screen_contains":"Type a question"}},
		{"send_key":"esc","wait_for":{"screen_contains":"Help"}},
		{"wait_only":true,"wait_for":{"screen_contains":"Type a question","screen_not_contains":"Help"}}
	]}`
	path := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	scr, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(scr.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(scr.Steps))
	}
	last := scr.Steps[2]
	if !last.WaitOnly {
		t.Fatalf("third step WaitOnly = false, want true")
	}
	if last.Send != "" || last.SendKey != "" {
		t.Fatalf("third step = %+v, want no send action", last)
	}
	if last.WaitFor.ScreenContains != "Type a question" {
		t.Fatalf("third step ScreenContains = %q, want %q", last.WaitFor.ScreenContains, "Type a question")
	}
	if last.WaitFor.ScreenNotContains != "Help" {
		t.Fatalf("third step ScreenNotContains = %q, want %q", last.WaitFor.ScreenNotContains, "Help")
	}
}
