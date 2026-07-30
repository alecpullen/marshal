package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
)

// TestTranscriptRespectsLeftWidth is the regression gate for content
// overflowing behind the side rail. Every transcript line must measure no
// wider than the viewport, and the assembled frame no wider than the
// terminal — otherwise lipgloss pads the left column past leftWidth and the
// rail is pushed off the right edge.
//
// The tab cases are the ones that regressed: ansi.StringWidth counts "\t" as
// one cell while the terminal expands it to the next tab stop, so every wrap
// decision undercounts. Tab-indented Go source and shell output hit this
// constantly. The remaining cases are guards on shapes that already worked.
func TestTranscriptRespectsLeftWidth(t *testing.T) {
	contents := []struct {
		name string
		ct   session.ContentType
		body string
	}{
		{"tabs plain", session.ContentTypePlain, "a\t" + strings.Repeat("b\t", 40) + "c"},
		{"tabs markdown", session.ContentTypeMarkdown, "a\t" + strings.Repeat("b\t", 40) + "c"},
		{"tab-indented go", session.ContentTypeCode,
			"func f() {\n\t\tif x {\n\t\t\t\treturn " + strings.Repeat("longName, ", 12) + "nil\n\t\t}\n}"},
		{"long path", session.ContentTypeMarkdown,
			"see " + strings.Repeat("verylongpathsegment/", 12) + "file.go"},
		{"long url", session.ContentTypeMarkdown, "https://example.com/" + strings.Repeat("a", 200)},
		{"md table", session.ContentTypeMarkdown,
			"| col a | col b | col c |\n|---|---|---|\n| " + strings.Repeat("x", 60) + " | y | z |"},
		{"fenced code", session.ContentTypeMarkdown,
			"```go\nfunc f() { " + strings.Repeat("// long comment ", 20) + " }\n```"},
		{"cjk", session.ContentTypeMarkdown, strings.Repeat("日本語のテキスト", 30)},
		{"emoji", session.ContentTypeMarkdown, strings.Repeat("🚀✅👍 ", 40)},
	}

	for _, c := range contents {
		for _, w := range []int{120, 140, 180, 240} {
			t.Run(c.name, func(t *testing.T) {
				m := newTestModel(t)
				m.state.Config.TUI.SidePanel.Enabled = true
				m.resize(w, 30)
				m.state.AddMessage(session.RoleAssistant, c.body, c.ct)
				m.refreshViewport()

				for i, l := range strings.Split(m.viewport.View(), "\n") {
					if lw := ansi.StringWidth(l); lw > m.leftWidth {
						t.Errorf("w=%d: transcript line %d width %d exceeds leftWidth %d",
							w, i, lw, m.leftWidth)
						break
					}
				}
				for _, l := range strings.Split(m.viewString(), "\n") {
					if lw := ansi.StringWidth(l); lw > w {
						t.Errorf("frame line width %d exceeds terminal width %d", lw, w)
						break
					}
				}
			})
		}
	}
}

func TestExpandTabs(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"no tabs", "no tabs"},
		{"\t", "        "},
		{"a\tb", "a       b"},
		{"ab\tc", "ab      c"},
		{"abcdefg\th", "abcdefg h"},
		{"abcdefgh\ti", "abcdefgh        i"},
		{"a\t\tb", "a               b"},
		{"a\nb\tc", "a\nb       c"},
		{"日本\tx", "日本    x"},
	} {
		if got := expandTabs(tc.in); got != tc.want {
			t.Errorf("expandTabs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
