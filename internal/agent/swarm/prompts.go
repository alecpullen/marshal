package swarm

import "fmt"

type ScoutFocus struct {
	Area        string
	Instruction string
}

var DefaultScoutFocuses = []ScoutFocus{
	{Area: "code", Instruction: "Find the implementation files, packages, and symbols most relevant to the goal."},
	{Area: "tests", Instruction: "Find the existing tests that cover the behaviour the goal touches, and how they are run."},
	{Area: "docs", Instruction: "Find documentation, configuration, and build files related to the goal."},
}

func plannerPrompt(ts *TaskState) string {
	return "You are the swarm planner. Read the shared task state below and produce a numbered plan of 3-7 steps for accomplishing the goal. Steps must be concrete and verifiable. Respond with a final action whose content is only the numbered plan, one step per line.\n\n" + ts.Render()
}

func scoutPrompt(ts *TaskState, focus ScoutFocus) string {
	return fmt.Sprintf("You are a repo scout assigned the focus area %q. %s\n\nUse read-only tools to inspect the repository. When done, respond with a final action whose content lists your findings: relevant file paths, symbols, and anything risky or surprising. Be concise.\n\n%s", focus.Area, focus.Instruction, ts.Render())
}

func implementerPrompt(ts *TaskState) string {
	return "You are the swarm implementer. Follow the plan and use the scout findings in the shared task state below. If the state contains tester feedback about failing tests, your job this round is to fix exactly those failures. Make the smallest change that accomplishes the goal, then run the narrowest useful validation. When done, respond with a final action summarising exactly what you changed.\n\n" + ts.Render()
}

func testerPrompt(ts *TaskState) string {
	return "You are the swarm tester. Run the project's tests for the change described in the shared task state below. Do not modify source files; only run tests and inspect output. End your final answer with TWO lines in this order: a JSON line `TEST_FAILURES_JSON: [...]` (an empty array if all tests pass) followed by a line reading exactly \"VERDICT: PASS\" or \"VERDICT: FAIL\".\n\n" + ts.Render()
}

func reviewerPrompt(ts *TaskState) string {
	return "You are the swarm reviewer. Inspect the changes made for the goal below — start with git.diff, then read the touched files as needed. Identify bugs, risks, or missed cases. Respond with a final action containing your review: either APPROVE with a one-line justification, or a list of concrete issues.\n\n" + ts.Render()
}
