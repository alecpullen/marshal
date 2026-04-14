package tui

import (
	"testing"

	"github.com/alecpullen/marshal/internal/config"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantErr  bool
	}{
		{":quit", "quit", false},
		{":q", "quit", false},
		{":new", "new", false},
		{":n", "new", false},
		{":diff", "diff", false},
		{":d", "diff", false},
		{":config", "config", false},
		{":cfg", "config", false},
		{":c", "config", false},
		{":clear", "clear", false},
		{":cl", "clear", false},
		{":cancel", "cancel", false},
		{":x", "cancel", false},
		{":retry", "retry", false},
		{":r", "retry", false},
		{":sessions", "sessions", false},
		{":ls", "sessions", false},
		{":s", "sessions", false},
		{":help", "help", false},
		{":h", "help", false},
		{":?", "help", false},
		// Unknown
		{":unknown", "", true},
		{":", "", true},
	}

	for _, tt := range tests {
		name, _, cmdType, err := parseCommand(tt.input, nil) // nil config for built-in only tests
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseCommand(%q): expected error, got nil", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseCommand(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if name != tt.wantName {
			t.Errorf("parseCommand(%q): got %q, want %q", tt.input, name, tt.wantName)
		}
		if cmdType != "builtin" {
			t.Errorf("parseCommand(%q): got cmdType %q, want builtin", tt.input, cmdType)
		}
	}
}

func TestCompleteCommand(t *testing.T) {
	tests := []struct {
		partial     string
		wantSuggest string
		wantExact   bool
	}{
		{"qu", "quit", true},
		{"q", "quit", true}, // only "quit" starts with q (aliases not in name completion)
		{"he", "help", true},
		{"h", "help", true},
		{"di", "diff", true},
		{"se", "", false}, // "sessions" and "setmodel" both start with "se"
		{"ses", "sessions", true},
		{"set", "setmodel", true},
		{"c", "", false},       // "cancel", "clear", "config" all start with c, no common extension
		{"ca", "cancel", true}, // only "cancel" starts with "ca"
		{"cl", "clear", true},
		{"con", "config", true},
		{"xyz", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		got, exact := completeCommand(tt.partial)
		if got != tt.wantSuggest || exact != tt.wantExact {
			t.Errorf("completeCommand(%q): got (%q, %v), want (%q, %v)",
				tt.partial, got, exact, tt.wantSuggest, tt.wantExact)
		}
	}
}

func TestFindCompletions(t *testing.T) {
	// Create a config with custom commands
	cfg := &config.Config{
		Commands: map[string]config.CustomCommand{
			"deploy": {Action: "shell", Command: "./deploy.sh", Help: "deploy to production"},
			"test":   {Action: "shell", Command: "go test ./...", Help: "run tests"},
			"hist":   {Action: "view", View: "sessions", Help: "view history"},
		},
	}

	tests := []struct {
		partial     string
		wantMatches int
		wantFirst   string
		wantCustom  bool
	}{
		// Built-in matches
		{"qu", 1, "quit", false},
		{"q", 1, "quit", false},
		{"he", 1, "help", false}, // match "help" by name

		// Custom command matches
		{"de", 1, "deploy", true},
		{"test", 1, "test", true}, // only custom "test"
		{"hi", 1, "hist", true},

		// Multiple matches
		{"c", 3, "cancel", false}, // cancel, clear, config

		// No matches
		{"xyz", 0, "", false},
		{"", 0, "", false},
	}

	for _, tt := range tests {
		matches := FindCompletions(tt.partial, cfg)
		if len(matches) != tt.wantMatches {
			t.Errorf("FindCompletions(%q): got %d matches, want %d", tt.partial, len(matches), tt.wantMatches)
			continue
		}
		if tt.wantMatches == 0 {
			continue
		}
		if matches[0].Name != tt.wantFirst {
			t.Errorf("FindCompletions(%q): first match got %q, want %q", tt.partial, matches[0].Name, tt.wantFirst)
		}
		if matches[0].IsCustom != tt.wantCustom {
			t.Errorf("FindCompletions(%q): first match IsCustom got %v, want %v", tt.partial, matches[0].IsCustom, tt.wantCustom)
		}
	}
}

func TestGetGhostSuggestion(t *testing.T) {
	matches := []CompletionMatch{
		{Name: "quit"},
	}
	ghost := GetGhostSuggestion("qu", matches)
	if ghost != "it" {
		t.Errorf("GetGhostSuggestion('qu'): got %q, want 'it'", ghost)
	}

	// Multiple matches with common prefix
	matches = []CompletionMatch{
		{Name: "cancel"},
		{Name: "clear"},
		{Name: "config"},
	}
	ghost = GetGhostSuggestion("c", matches)
	if ghost != "" {
		t.Errorf("GetGhostSuggestion('c'): got %q, want '' (common prefix is 'c' itself)", ghost)
	}

	// No matches
	ghost = GetGhostSuggestion("xyz", nil)
	if ghost != "" {
		t.Errorf("GetGhostSuggestion('xyz'): got %q, want ''", ghost)
	}
}
