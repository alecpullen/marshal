package tui

import "testing"

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
		name, _, err := parseCommand(tt.input)
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
		{"se", "sessions", true},
		{"c", "c", false},      // "cancel", "clear", "config" all start with c
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
