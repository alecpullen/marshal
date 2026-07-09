package diffview

import (
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// defaultLang is the lexer used when no path hint is available. Marshal's
// own code is Go; the agent's edits are mostly Go too. Callers can override
// with Options.Language.
const defaultLang = "go"

// detectLanguage maps a file path to a chroma lexer name. Returns the
// default (Go) when the path is empty or the extension is unknown.
func detectLanguage(h Hunk) string {
	if h.FilePath == "" {
		return defaultLang
	}
	ext := strings.ToLower(filepath.Ext(h.FilePath))
	lexer := lexers.Get(strings.TrimPrefix(ext, "."))
	if lexer == nil || lexer.Config().Name == "fallback" {
		return defaultLang
	}
	return lexer.Config().Name
}

// highlightCode renders a code line with chroma syntax tokens, returning
// lipgloss-styled fragments. On any error or missing lexer, returns the
// plain content (F17 R1 fallback). The caller wraps the result in
// baseStyle.Render(...) so unclassified text inherits the line color.
func highlightCode(content, language string) string {
	if content == "" {
		return content
	}
	lang := language
	if lang == "" {
		lang = defaultLang
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	iterator, err := lexer.Tokenise(nil, content)
	if err != nil {
		return content
	}
	var b strings.Builder
	for token := iterator(); token != chroma.EOF; token = iterator() {
		txt := token.Value
		switch token.Type {
		case chroma.Keyword:
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Render(txt))
		case chroma.String, chroma.StringDouble, chroma.StringSingle:
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render(txt))
		case chroma.Comment, chroma.CommentSingle, chroma.CommentMultiline:
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true).Render(txt))
		case chroma.Number, chroma.NumberFloat, chroma.NumberHex:
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Render(txt))
		case chroma.NameClass, chroma.NameBuiltin:
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Render(txt))
		default:
			b.WriteString(txt)
		}
	}
	return b.String()
}
