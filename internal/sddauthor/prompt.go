package sddauthor

import "fmt"

// RenderPrompt builds the authoring handoff prompt. The skill body supplies
// the detailed format; the prompt supplies the request-specific design and
// the authoritative candidate path.
func RenderPrompt(req Request) string {
	return fmt.Sprintf(
		"You are authoring a Marshal executable SDD plan.\n\n"+
			"The approved design is reference input:\n\n%s\n\n"+
			"Write exactly one plan to the provided candidate path: %s.\n"+
			"Inspect the repository before naming files or operations.\n"+
			"Do not edit source files, run implementation commands, commit, or start /sdd.\n"+
			"Use marshal.* blocks only when preconditions are proven; use scoped marshal.agent fallback for unresolved work.",
		req.Design, req.PlanPath)
}
