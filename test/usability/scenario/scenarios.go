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
func HelpOpenClose() *scripted.Scripted {
	return &scripted.Scripted{
		Name: "help_open_close",
		Steps: []scripted.Step{
			{Send: "?", WaitFor: scripted.WaitFor{ScreenContains: "Type a question"}},
			{SendKey: "esc", WaitFor: scripted.WaitFor{ScreenContains: "Help"}},
		},
	}
}

// SubtractFix drives Marshal to add a missing Subtract function and make tests pass.
func SubtractFix() *llm.LLM {
	return llm.New(llm.Config{}, nil).WithGoal("Add a Subtract function to the calc package and run tests until they pass.")
}

func boolPtr(b bool) *bool { return &b }
