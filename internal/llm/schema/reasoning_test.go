package schema

import "testing"

func TestReasoningEmpty(t *testing.T) {
	var nilR *Reasoning
	if !nilR.Empty() {
		t.Fatal("nil Reasoning should be empty")
	}
	if !(&Reasoning{}).Empty() {
		t.Fatal("zero Reasoning should be empty")
	}
	if (&Reasoning{Text: "thought"}).Empty() {
		t.Fatal("text-only Reasoning should not be empty")
	}
	if (&Reasoning{Items: []ReasoningItem{{Provider: "p", Model: "m"}}}).Empty() {
		t.Fatal("items-only Reasoning should not be empty")
	}
}

func TestStripReasoningCopies(t *testing.T) {
	in := []ChatMessage{
		{Role: RoleAssistant, Content: "a", Reasoning: &Reasoning{Text: "r1"}},
		{Role: RoleUser, Content: "b"},
		{Role: RoleAssistant, Content: "c", Reasoning: &Reasoning{Items: []ReasoningItem{{Provider: "p", Model: "m"}}}},
	}
	out := StripReasoning(in)
	if out[0].Reasoning != nil || out[2].Reasoning != nil {
		t.Fatal("output should have no Reasoning")
	}
	if in[0].Reasoning == nil || in[2].Reasoning == nil {
		t.Fatal("input messages should keep their Reasoning")
	}
	if out[0].Content != "a" || out[1].Content != "b" || out[2].Content != "c" {
		t.Fatal("content should be preserved")
	}
}

func TestReasoningItemsForKeepsMatchingOnly(t *testing.T) {
	in := []ChatMessage{
		{
			Role: RoleAssistant,
			Reasoning: &Reasoning{
				Text: "kept text",
				Items: []ReasoningItem{
					{Provider: "p", Model: "m1", Kind: ReasoningKindOpenAI, Data: []byte(`{"a":1}`)},
					{Provider: "p", Model: "m2", Kind: ReasoningKindOpenAI, Data: []byte(`{"b":2}`)},
				},
			},
		},
		{
			Role: RoleAssistant,
			Reasoning: &Reasoning{
				Items: []ReasoningItem{
					{Provider: "p", Model: "m2", Kind: ReasoningKindOpenAI},
				},
			},
		},
	}
	out := ReasoningItemsFor(in, "p", "m1")
	if out[0].Reasoning == nil {
		t.Fatal("first message should keep a Reasoning")
	}
	if out[0].Reasoning.Text != "kept text" {
		t.Fatalf("text not kept: %q", out[0].Reasoning.Text)
	}
	if len(out[0].Reasoning.Items) != 1 || out[0].Reasoning.Items[0].Model != "m1" {
		t.Fatalf("expected exactly the m1 item, got %+v", out[0].Reasoning.Items)
	}
	if out[1].Reasoning != nil {
		t.Fatal("message whose only item was dropped and has no text should become nil")
	}
	// Input untouched.
	if len(in[0].Reasoning.Items) != 2 {
		t.Fatal("input messages should be unmodified")
	}
}

func TestWithoutReasoningItemsKeepsText(t *testing.T) {
	in := []ChatMessage{
		{Role: RoleAssistant, Reasoning: &Reasoning{Text: "t", Items: []ReasoningItem{{Provider: "p", Model: "m"}}}},
		{Role: RoleAssistant, Reasoning: &Reasoning{Items: []ReasoningItem{{Provider: "p", Model: "m"}}}},
	}
	out := WithoutReasoningItems(in)
	if out[0].Reasoning == nil || out[0].Reasoning.Text != "t" || len(out[0].Reasoning.Items) != 0 {
		t.Fatalf("text should be kept and items removed: %+v", out[0].Reasoning)
	}
	if out[1].Reasoning != nil {
		t.Fatal("items-only Reasoning should become nil")
	}
	if len(in[0].Reasoning.Items) != 1 {
		t.Fatal("input messages should be unmodified")
	}
}
