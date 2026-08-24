package skills

import (
	"strings"
	"testing"

	"marshal/internal/contextpack"
)

// TestSkillLoadsAgainstAFullContextPack pins the bug that made skill
// loading impossible on any real repo: the context pack sits at exactly
// 100% of its budget (the builder truncates to fill it), so a check that
// billed skill bodies against remaining pack budget refused every skill
// with "only 0 tokens remain".
func TestSkillLoadsAgainstAFullContextPack(t *testing.T) {
	state := newTestState()
	// A pack at exactly its budget — the steady state on any repo whose
	// directory map exceeds the budget, which is the common case.
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoMap, Title: "Directory Map", Content: "..."},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 12000},
	})

	idx := NewIndex()
	idx.skills["systematic-debugging"] = Skill{
		Name: "systematic-debugging",
		Body: strings.Repeat("Find the root cause before proposing a fix. ", 200),
	}

	if err := NewSkillLoader(idx, state).Load("systematic-debugging", false); err != nil {
		t.Fatalf("skill must load against a full context pack, got: %v", err)
	}
	if !state.HasActiveSkill("systematic-debugging") {
		t.Error("skill did not become active")
	}
}
