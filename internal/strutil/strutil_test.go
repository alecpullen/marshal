package strutil

import "testing"

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		max      int
		ellipsis bool
		want     string
	}{
		{"short", "hello", 10, true, "hello"},
		{"exact", "hello", 5, true, "hello"},
		{"cut with ellipsis", "hello world", 5, true, "hello…"},
		{"cut without ellipsis", "hello world", 5, false, "hello"},
		{"rune safe", "héllo wörld", 4, false, "héll"},
		{"rune safe ellipsis", "日本語のテキスト", 3, true, "日本語…"},
		{"zero max", "abc", 0, true, ""},
		{"negative max", "abc", -1, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truncate(tt.s, tt.max, tt.ellipsis); got != tt.want {
				t.Errorf("Truncate(%q, %d, %v) = %q, want %q", tt.s, tt.max, tt.ellipsis, got, tt.want)
			}
		})
	}
}

func TestTruncateMiddle(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncate", "hello world", 7, "hel…rld"},
		{"truncate even", "abcdefgh", 5, "ab…gh"},
		{"rune safe", "日本語のテキスト", 5, "日本…スト"},
		{"path keeps basename", "internal/llm/routing/resolve.go", 18, "resolve.go"},
		{"path basename too long", "internal/llm/routing/resolve.go", 8, "res…e.go"},
		{"basename fits exactly", "internal/llm/routing/resolve.go", 10, "resolve.go"},
		{"basename fits with room", "a/very/deep/path/code.go", 8, "code.go"},
		{"too small", "abc", 2, ""},
		{"zero max", "abc", 0, ""},
		{"negative max", "abc", -1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TruncateMiddle(tt.s, tt.max); got != tt.want {
				t.Errorf("TruncateMiddle(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
		})
	}
}

func TestCompactTokens(t *testing.T) {
	if got := CompactTokens(842); got != "842" {
		t.Errorf("CompactTokens(842) = %q", got)
	}
	if got := CompactTokens(18000); got != "18k" {
		t.Errorf("CompactTokens(18000) = %q", got)
	}
	if got := CompactTokens(999); got != "999" {
		t.Errorf("CompactTokens(999) = %q", got)
	}
}

func TestPtr(t *testing.T) {
	p := Ptr(42)
	if p == nil || *p != 42 {
		t.Errorf("Ptr(42) = %v", p)
	}
}
