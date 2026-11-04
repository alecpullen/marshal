package commands

import (
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// TestKindPutsTUIOnlyFirst pins the precedence Kind applies: a command that is
// both TUI-only and carries a prompt body is still TUI-only, a bare prompt body
// makes it a prompt command, and a handler alone makes it headless.
func TestKindPutsTUIOnlyFirst(t *testing.T) {
	tuiOnly := Command{Name: "trust", TUIOnly: true, PromptBody: "Explain project trust."}
	if got := tuiOnly.Kind(); got != acpwire.CommandKindTUIOnly {
		t.Errorf("TUI-only command with a prompt body: Kind() = %q, want %q", got, acpwire.CommandKindTUIOnly)
	}

	prompt := Command{Name: "review-pr", PromptBody: "Review the PR."}
	if got := prompt.Kind(); got != acpwire.CommandKindPrompt {
		t.Errorf("command with only a prompt body: Kind() = %q, want %q", got, acpwire.CommandKindPrompt)
	}

	headless := Command{Name: "diff", Handler: func(*session.State, []string) Result { return Text("ok") }}
	if got := headless.Kind(); got != acpwire.CommandKindHeadless {
		t.Errorf("command with only a handler: Kind() = %q, want %q", got, acpwire.CommandKindHeadless)
	}
}

// TestInfoCarriesTheDispatchFields pins Info as what a client's dispatch
// reads: the listing fields plus hidden, TUI-only, whether a handler exists and
// the prompt body.
func TestInfoCarriesTheDispatchFields(t *testing.T) {
	trust := Info(Command{
		Name:        "trust",
		Description: "Explain project trust",
		Args:        "<action>",
		Group:       "Project",
		Hidden:      true,
		TUIOnly:     true,
		Handler:     func(*session.State, []string) Result { return Text("trusted") },
	})
	if !trust.TUIOnly {
		t.Errorf("Info(trust).TUIOnly = false, want true")
	}
	if !trust.HasHandler {
		t.Errorf("Info(trust).HasHandler = false, want true")
	}
	if !trust.Hidden {
		t.Errorf("Info(trust).Hidden = false, want true")
	}
	if trust.Kind != acpwire.CommandKindTUIOnly {
		t.Errorf("Info(trust).Kind = %q, want %q", trust.Kind, acpwire.CommandKindTUIOnly)
	}
	if trust.Name != "trust" || trust.Description != "Explain project trust" || trust.Args != "<action>" || trust.Group != "Project" {
		t.Errorf("Info(trust) listing fields not carried: %+v", trust)
	}

	review := Info(Command{Name: "review-pr", Description: "Review a PR", PromptBody: "Review the PR."})
	if review.PromptBody != "Review the PR." {
		t.Errorf("Info(review-pr).PromptBody = %q, want %q", review.PromptBody, "Review the PR.")
	}
	if review.HasHandler {
		t.Errorf("Info(review-pr).HasHandler = true, want false")
	}
	if review.Kind != acpwire.CommandKindPrompt {
		t.Errorf("Info(review-pr).Kind = %q, want %q", review.Kind, acpwire.CommandKindPrompt)
	}
}

// wireTestDoc builds a doc with a header, an action row whose children hold a
// second action row, and a plain row.
func wireTestDoc() *Doc {
	return &Doc{
		Title:  "Timeline",
		Footer: "Enter opens a turn's actions.",
		Rows: []Row{
			{Header: "Turns"},
			{
				Text:        "turn 1",
				Detail:      "2m",
				ActionLabel: "↵ restore",
				Action:      func(*session.State) Result { return Text("outer") },
				Children: []Row{
					{Text: "restore", ActionLabel: "↵ restore", Action: func(*session.State) Result { return Text("inner") }},
					{Text: "plain"},
				},
			},
			{Text: "read-only", Detail: "no action"},
		},
	}
}

// TestWireDocOfMintsActionsDepthFirst pins the minting order: each row action
// is minted once, a row's own action before its children's, and the ID the
// mint returned is what the row carries. With a nil mint no row carries an ID.
func TestWireDocOfMintsActionsDepthFirst(t *testing.T) {
	var minted []string
	mint := func(action func(*session.State) Result) string {
		minted = append(minted, action(nil).Text)
		return "a" + string(rune('0'+len(minted)))
	}

	doc := WireDocOf(wireTestDoc(), mint)

	if len(minted) != 2 {
		t.Fatalf("mint called %d times, want 2: %v", len(minted), minted)
	}
	if minted[0] != "outer" || minted[1] != "inner" {
		t.Errorf("mint order = %v, want [outer inner]", minted)
	}
	if doc.Title != "Timeline" || doc.Footer != "Enter opens a turn's actions." {
		t.Errorf("doc frame not carried: %+v", doc)
	}
	if doc.Rows[0].Header != "Turns" || doc.Rows[0].ActionID != "" {
		t.Errorf("header row = %+v, want a header and no action ID", doc.Rows[0])
	}
	if doc.Rows[1].ActionLabel != "↵ restore" || doc.Rows[1].ActionID != "a1" {
		t.Errorf("outer action row = %+v, want label %q and ID %q", doc.Rows[1], "↵ restore", "a1")
	}
	if doc.Rows[1].Detail != "2m" {
		t.Errorf("outer action row detail = %q, want %q", doc.Rows[1].Detail, "2m")
	}
	if doc.Rows[1].Children[0].ActionID != "a2" {
		t.Errorf("child action row ID = %q, want %q", doc.Rows[1].Children[0].ActionID, "a2")
	}
	if doc.Rows[1].Children[1].ActionID != "" {
		t.Errorf("plain child row carries ID %q, want none", doc.Rows[1].Children[1].ActionID)
	}
	if doc.Rows[2].ActionID != "" {
		t.Errorf("plain row carries ID %q, want none", doc.Rows[2].ActionID)
	}
	if doc.Rows[2].Text != "read-only" || doc.Rows[2].Detail != "no action" {
		t.Errorf("plain row = %+v, want its text and detail", doc.Rows[2])
	}
}

// TestWireDocOfWithoutMintKeepsLabels pins the nil-mint path: no row carries an
// ID, and the action label survives as plain text.
func TestWireDocOfWithoutMintKeepsLabels(t *testing.T) {
	doc := WireDocOf(wireTestDoc(), nil)

	if doc == nil {
		t.Fatal("WireDocOf(non-nil doc, nil) = nil, want a doc")
	}
	if doc.Rows[1].ActionLabel != "↵ restore" {
		t.Errorf("outer action label = %q, want %q", doc.Rows[1].ActionLabel, "↵ restore")
	}
	if doc.Rows[1].ActionID != "" {
		t.Errorf("outer action row carries ID %q, want none", doc.Rows[1].ActionID)
	}
	if doc.Rows[1].Children[0].ActionID != "" {
		t.Errorf("child action row carries ID %q, want none", doc.Rows[1].Children[0].ActionID)
	}
	if doc.Rows[1].Children[0].ActionLabel != "↵ restore" {
		t.Errorf("child action label = %q, want %q", doc.Rows[1].Children[0].ActionLabel, "↵ restore")
	}
}

// TestWireResultOfKeepsTheAgentGoal pins the result conversion: the text, the
// doc and the agent goal all reach the wire, and a nil doc stays nil.
func TestWireResultOfKeepsTheAgentGoal(t *testing.T) {
	res := WireResultOf(Result{
		Text:      "Annotated.",
		Doc:       wireTestDoc(),
		AgentGoal: "annotate the artifact",
	}, nil)

	if res.Text != "Annotated." {
		t.Errorf("Text = %q, want %q", res.Text, "Annotated.")
	}
	if res.AgentGoal != "annotate the artifact" {
		t.Errorf("AgentGoal = %q, want %q", res.AgentGoal, "annotate the artifact")
	}
	if res.Doc == nil {
		t.Fatal("Doc = nil, want the converted doc")
	}
	if res.Doc.Title != "Timeline" || len(res.Doc.Rows) != 3 {
		t.Errorf("Doc = %+v, want the doc's title and three rows", res.Doc)
	}

	plain := WireResultOf(Result{Text: "ok"}, nil)
	if plain.Doc != nil {
		t.Errorf("Doc from a text-only result = %+v, want nil", plain.Doc)
	}
	if plain.Text != "ok" {
		t.Errorf("Text = %q, want %q", plain.Text, "ok")
	}
}
