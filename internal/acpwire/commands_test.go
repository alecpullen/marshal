package acpwire

import (
	"encoding/json"
	"testing"
)

// TestCommandsJSON pins the bytes session/command_list and session/command
// carry: the dispatch fields a client reads off a command entry and a
// command's output, panel rows included. Each must encode exactly as the
// contract spells it.
func TestCommandsJSON(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name: "headless command",
			value: CommandInfo{
				Name:        "help",
				Description: "Show commands",
				Group:       "Chat",
				Kind:        CommandKindHeadless,
				HasHandler:  true,
			},
			want: `{"name":"help","description":"Show commands","group":"Chat","kind":"headless","hasHandler":true}`,
		},
		{
			name: "TUI-only command",
			value: CommandInfo{
				Name:        "trust",
				Description: "Explain project trust",
				Kind:        CommandKindTUIOnly,
				TUIOnly:     true,
				HasHandler:  true,
			},
			want: `{"name":"trust","description":"Explain project trust","kind":"tui_only","tuiOnly":true,"hasHandler":true}`,
		},
		{
			name: "prompt command",
			value: CommandInfo{
				Name:        "review-pr",
				Description: "Review a PR",
				Kind:        CommandKindPrompt,
				PromptBody:  "Review the PR.",
			},
			want: `{"name":"review-pr","description":"Review a PR","kind":"prompt","promptBody":"Review the PR."}`,
		},
		{
			name:  "hidden command",
			value: CommandInfo{Name: "dev", Kind: CommandKindHeadless, Hidden: true, HasHandler: true},
			want:  `{"name":"dev","description":"","kind":"headless","hidden":true,"hasHandler":true}`,
		},
		{
			name:  "result with text only",
			value: CommandResult{Text: "Switched to branch 2."},
			want:  `{"text":"Switched to branch 2."}`,
		},
		{
			name: "result with a panel",
			value: CommandResult{Doc: &WireDoc{
				Title:  "Timeline",
				Footer: "Enter opens a turn's actions.",
				Rows: []WireRow{
					{Header: "Turns"},
					{Text: "rewind", ActionLabel: "↵ rewind", ActionID: "a1"},
					{Text: "turn 1", Detail: "2m", Children: []WireRow{
						{Text: "restore", ActionLabel: "↵ restore", ActionID: "a2"},
					}},
				},
			}},
			want: `{"doc":{"title":"Timeline","footer":"Enter opens a turn's actions.","rows":[{"header":"Turns"},{"text":"rewind","actionLabel":"↵ rewind","actionId":"a1"},{"text":"turn 1","detail":"2m","children":[{"text":"restore","actionLabel":"↵ restore","actionId":"a2"}]}]}}`,
		},
		{
			name:  "result with an agent goal",
			value: CommandResult{Text: "Annotated.", AgentGoal: "annotate the artifact"},
			want:  `{"text":"Annotated.","agentGoal":"annotate the artifact"}`,
		},
		{
			name:  "empty result",
			value: CommandResult{},
			want:  `{}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			if string(got) != tc.want {
				t.Errorf("%s JSON:\n got: %s\nwant: %s", tc.name, got, tc.want)
			}
		})
	}
}
