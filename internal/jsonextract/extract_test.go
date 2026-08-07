package jsonextract

import (
	"errors"
	"testing"
)

func TestExtract(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "simple flat object",
			input: `some text {"a": 1, "b": 2} more text`,
			want:  `{"a": 1, "b": 2}`,
		},
		{
			name:  "nested object",
			input: `Sure! Here is the JSON: {"a": 1, "b": {"nested": true}} some trailing text {not really json}`,
			want:  `{"a": 1, "b": {"nested": true}}`,
		},
		{
			name:  "array values",
			input: `{"items": [1, 2, {"x": 3}], "done": true}`,
			want:  `{"items": [1, 2, {"x": 3}], "done": true}`,
		},
		{
			name:  "string with braces inside",
			input: `{"a": "string with { brace", "b": "also } brace"}`,
			want:  `{"a": "string with { brace", "b": "also } brace"}`,
		},
		{
			name:  "string with escaped quotes and braces",
			input: `{"msg": "he said \"hello { world\""}`,
			want:  `{"msg": "he said \"hello { world\""}`,
		},
		{
			name:  "multiple top-level objects returns first",
			input: `{"first": 1} {"second": 2}`,
			want:  `{"first": 1}`,
		},
		{
			name:  "markdown code fence stripped",
			input: "```json\n{\"a\": 1}\n```",
			want:  `{"a": 1}`,
		},
		{
			name:  "no leading or trailing fence",
			input: `{"a": 1}`,
			want:  `{"a": 1}`,
		},
		{
			name:    "no JSON object at all",
			input:   "I think the answer is 42.",
			wantErr: true,
		},
		{
			// Extract stays strict; only ExtractRepairing salvages a
			// truncated tail. See TestExtractRepairing.
			name:    "unmatched opening brace",
			input:   `{"a": 1, "b": 2`,
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "only markdown fence",
			input:   "```json\n```",
			wantErr: true,
		},
		{
			name:  "stray closing brace before object",
			input: `} {"action": {"x": 1}}`,
			want:  `{"action": {"x": 1}}`,
		},
		{
			name:  "prose with unmatched brace then object",
			input: `close the brace } then use: {"cmd":"echo hi"}`,
			want:  `{"cmd":"echo hi"}`,
		},
		{
			name:    "multiple stray braces without real object",
			input:   `} } }`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Extract(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Extract = %q, want error", got)
				}
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("Extract error = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Extract returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Extract = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractRepairing(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		want         string
		wantRepaired bool
		wantErr      bool
	}{
		{
			name:  "balanced object is not repaired",
			input: `{"action": {"type": "final"}}`,
			want:  `{"action": {"type": "final"}}`,
		},
		{
			name:         "truncated object is closed",
			input:        `{"rationale": "x", "action": {"type": "final"`,
			want:         `{"rationale": "x", "action": {"type": "final"}}`,
			wantRepaired: true,
		},
		{
			name:         "truncated array is closed",
			input:        `{"actions": [{"type": "tool_call"}`,
			want:         `{"actions": [{"type": "tool_call"}]}`,
			wantRepaired: true,
		},
		{
			// The "]}" tail models emit when they confuse the single-action
			// and parallel-actions forms. The stray ']' must be excised, or
			// the result is not valid JSON.
			name:         "stray array close is excised",
			input:        `{"action": {"type": "final", "content": "hi"}]}`,
			want:         `{"action": {"type": "final", "content": "hi"}}`,
			wantRepaired: true,
		},
		{
			name:  "brackets inside strings are untouched",
			input: `{"content": "an array ends with ]} here"}`,
			want:  `{"content": "an array ends with ]} here"}`,
		},
		{
			name:    "unterminated string is not repaired",
			input:   `{"action": {"content": "half a sen`,
			wantErr: true,
		},
		{
			name:    "no object at all",
			input:   "I think the answer is 42.",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, repaired, err := ExtractRepairing(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExtractRepairing = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractRepairing returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("text = %q, want %q", got, tt.want)
			}
			if repaired != tt.wantRepaired {
				t.Errorf("repaired = %v, want %v", repaired, tt.wantRepaired)
			}
		})
	}
}
