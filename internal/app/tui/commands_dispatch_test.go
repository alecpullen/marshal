package tui

import (
	"reflect"
	"testing"
)

func TestParseReviewArgs(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		model  string
		base   string
		rangeV string
		focus  []string
	}{
		{
			name:  "model only",
			args:  []string{"--model", "ollama/codellama", "main.go"},
			model: "ollama/codellama",
			focus: []string{"main.go"},
		},
		{
			name:  "model equals",
			args:  []string{"--model=ollama/codellama", "main.go"},
			model: "ollama/codellama",
			focus: []string{"main.go"},
		},
		{
			name:  "base only",
			args:  []string{"--base", "main", "src"},
			base:  "main",
			focus: []string{"src"},
		},
		{
			name:  "base equals",
			args:  []string{"--base=main", "src"},
			base:  "main",
			focus: []string{"src"},
		},
		{
			name:   "range only",
			args:   []string{"main...HEAD", "src"},
			rangeV: "main...HEAD",
			focus:  []string{"src"},
		},
		{
			name:   "all flags",
			args:   []string{"--model=ollama/codellama", "--base=main", "main...HEAD", "focus", "words"},
			model:  "ollama/codellama",
			base:   "main",
			rangeV: "main...HEAD",
			focus:  []string{"focus", "words"},
		},
		{
			name:  "no flags",
			args:  []string{"just", "focus"},
			focus: []string{"just", "focus"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, base, rangeV, remaining := parseReviewArgs(tc.args)
			if model != tc.model {
				t.Fatalf("model = %q, want %q", model, tc.model)
			}
			if base != tc.base {
				t.Fatalf("base = %q, want %q", base, tc.base)
			}
			if rangeV != tc.rangeV {
				t.Fatalf("range = %q, want %q", rangeV, tc.rangeV)
			}
			if !reflect.DeepEqual(remaining, tc.focus) {
				t.Fatalf("remaining = %v, want %v", remaining, tc.focus)
			}
		})
	}
}
