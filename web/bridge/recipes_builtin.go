package bridge

// reviewFindingsFormat and ciResultFormat spell out the structured result
// each built-in ends with. The runner parses the last fenced json block.
const reviewFindingsFormat = "```json\n" +
	`{"findings":[{"severity":"blocking|should-fix|nit","path":"file","line":1,"title":"short","body":"detail"}],"summary":"one paragraph"}` +
	"\n```"

const ciResultFormat = "```json\n" +
	`{"reproduced":true,"command":"the command you ran","cause":"root cause","fixed":true,"notes":"anything the reviewer should know"}` +
	"\n```"

func isBuiltinRecipe(name string) bool {
	for _, b := range builtinRecipes() {
		if b.Name == name {
			return true
		}
	}
	return false
}

// builtinRecipes are the recipes that ship with the bridge.
func builtinRecipes() []Recipe {
	return []Recipe{
		{
			Name: "review-pr", Title: "Review a PR", Kind: RecipePrompt, Mode: "plan",
			Description: "Review a pull request and return structured findings.",
			Inputs: []RecipeInput{
				{Name: "pr", Label: "PR number", Required: true},
				{Name: "title", Label: "PR title"},
			},
			Output: OutputReviewFindings, Builtin: true,
			Prompt: "Review pull request #{{pr}} ({{title}}).\n\n" +
				"1. Review the diff against the base branch. Cite files and lines.\n" +
				"2. Classify each finding as blocking, should-fix or nit.\n" +
				"3. End your final message with a fenced json block of exactly this shape:\n\n" + reviewFindingsFormat,
		},
		{
			Name: "fix-ci", Title: "Fix a failing check", Kind: RecipePrompt, Mode: "edit",
			Description: "Reproduce a failing CI check, fix it, and report the result.",
			Inputs: []RecipeInput{
				{Name: "check", Label: "Failing check", Required: true},
				{Name: "log", Label: "Log excerpt"},
				{Name: "command", Label: "Command that fails"},
			},
			Output: OutputCIResult, Builtin: true,
			Prompt: "The CI check {{check}} is failing.\n\nCommand: {{command}}\n\nLog excerpt:\n{{log}}\n\n" +
				"1. Reproduce first, by running the failing command.\n" +
				"2. If it does not reproduce, stop and report reproduced: false.\n" +
				"3. Never skip, disable or quarantine tests, and never change CI config to hide a failure.\n" +
				"4. End your final message with a fenced json block of exactly this shape:\n\n" + ciResultFormat,
		},
		{
			Name: "summarize-changes", Title: "Summarize changes", Kind: RecipePrompt, Mode: "plan",
			Description: "Summarize what changed in the repository since a ref or date.",
			Inputs:      []RecipeInput{{Name: "since", Label: "Since (ref or date)", Required: true}},
			Output:      OutputNone, Builtin: true,
			Prompt: "Summarize what changed in this repository since {{since}}. Group related changes and call out anything risky.",
		},
		{
			Name: "update-deps", Title: "Update dependencies", Kind: RecipePrompt, Mode: "edit",
			Description: "Update one ecosystem's dependencies and keep the build green.",
			Inputs:      []RecipeInput{{Name: "ecosystem", Label: "Ecosystem (go, npm, …)", Required: true}},
			Output:      OutputNone, Builtin: true,
			Prompt: "Update the {{ecosystem}} dependencies of this repository to their latest compatible versions. " +
				"Run the build and tests afterwards and fix what the update breaks.",
		},
	}
}
