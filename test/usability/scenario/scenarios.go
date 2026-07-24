package scenario

import (
	"marshal/test/usability/actor/llm"
	"marshal/test/usability/actor/scripted"
)

// HelpOpenClose opens the help overlay and dismisses it.
func HelpOpenClose() *scripted.Scripted {
	return &scripted.Scripted{
		Name: "help_open_close",
		Steps: []scripted.Step{
			{Send: "?", WaitFor: scripted.WaitFor{ScreenContains: "Type a question"}},
			{SendKey: "esc", WaitFor: scripted.WaitFor{ScreenContains: "Keys", State: scripted.UIStatePredicate{HelpOpen: boolPtr(true)}}},
		},
	}
}

// SubtractFix drives Marshal to add a missing Subtract function and make tests pass.
func SubtractFix() *llm.LLM {
	return llm.New(llm.Config{}, nil).WithGoal("Add a Subtract function to the calc package and run tests until they pass.")
}

func boolPtr(b bool) *bool { return &b }
