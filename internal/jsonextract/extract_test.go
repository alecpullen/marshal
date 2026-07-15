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
