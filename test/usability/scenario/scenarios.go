package scenario

import (
	"marshal/test/usability/actor/llm"
	"marshal/test/usability/actor/scripted"
)

// HelpOpenClose opens the help dock panel and dismisses it.
//
// After TUI Panel Framework §3 Tasks 4 + 3, "?" opens /help as a docked
// doc panel (the /help listing, with the keybinding cheatsheet as the
// Doc.Footer). Esc closes the docpanel and the dock clears.
//
// The scripted actor checks each step's WaitFor *before* sending (see
// scripted.go), so the first step must wait for a precondition that is
// true at startup ("Type a question" — the input prompt banner is
// already on screen). The second step waits for the post-`?` state
// ("Help" — the docpanel chrome title) before sending esc.
//
// The third step sends nothing: it is a postcondition. Reaching "done" means
// the viewport itself showed the prompt again with no Help title, so a close
// that silently fails can never report success.
func HelpOpenClose() *scripted.Scripted {
	return &scripted.Scripted{
		Name: "help_open_close",
		Steps: []scripted.Step{
			{Send: "?", WaitFor: scripted.WaitFor{ScreenContains: "Type a question"}},
			{SendKey: "esc", WaitFor: scripted.WaitFor{ScreenContains: "Help"}},
			{WaitOnly: true, WaitFor: scripted.WaitFor{
				ScreenContains:    "Type a question",
				ScreenNotContains: "Help",
			}},
		},
	}
}

// SubtractFix drives Marshal to add a missing Subtract function and make tests
// pass, and is used only by the exploratory LLM scenario.
//
// Its result is the actor's claim, not an independent verdict: nothing here runs
// the fixture's own `go test` to confirm the repair, so a success must not be
// read as evidence that the code is correct. It is opt-in and outside the
// scripted CI selection.
func SubtractFix() *llm.LLM {
	return llm.New(llm.Config{}, nil).WithGoal("Add a Subtract function to the calc package and run tests until they pass.")
}

func boolPtr(b bool) *bool { return &b }
