package sidepanel

import "testing"

func TestStyleHelpersPreserveText(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) string
	}{
		{"success", styleSuccess},
		{"error", styleError},
		{"warning", styleWarning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripANSI(tt.fn("+42"))
			if got != "+42" {
				t.Errorf("visible text = %q, want %q", got, "+42")
			}
		})
	}
}

func TestStyleHelpersEmptyString(t *testing.T) {
	if got := StripANSI(styleSuccess("")); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
