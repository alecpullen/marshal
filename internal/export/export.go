// Package export renders a session transcript as a single self-contained HTML
// file (docs/12 F15). It reads session.State messages and the audit log,
// converts assistant markdown to HTML via goldmark, and optionally redacts
// secret-bearing values via internal/redact. No external assets; no network.
package export

import (
	_ "embed"
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/redact"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/util"
)

//go:embed template.html
var templateHTML string

type item struct {
	IsMessage     bool
	IsTool        bool
	MessageClass  string
	RoleLabel     string
	ContentHTML   template.HTML
	Reasoning     template.HTML
	Usage         string
	ToolName      string
	ResultSummary string
	ToolArgs      string
	ToolError     string
}

type data struct {
	Title string
	Items []item
}

// Render produces the self-contained HTML for the session's active branch.
// When redactOn is true, secret-bearing values are masked before rendering.
func Render(state *session.State, redactOn bool) ([]byte, error) {
	tmpl, err := template.New("session").Parse(templateHTML)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	title := state.Title()
	if title == "" {
		title = "Marshal session " + state.SessionID()
	}

	transcript := state.Transcript()
	items := make([]item, 0, len(transcript))
	md := goldmark.New(goldmark.WithParser(safeParser()))
	for _, t := range transcript {
		switch t.Kind {
		case session.KindMessage:
			if t.Message == nil {
				continue
			}
			content := t.Message.Content
			reasoning := t.Message.Reasoning
			usage := t.Message.Usage
			if redactOn {
				content = redact.Secrets(content)
				reasoning = redact.Secrets(reasoning)
				usage = redact.Secrets(usage)
			}
			contentHTML := contentToHTML(md, content, t.Message.ContentType)
			// Reasoning is rendered through the same markdown pipeline as
			// content so thinking text (which may contain markdown) is
			// rendered consistently rather than escaped as plain text.
			reasoningHTML := contentToHTML(md, reasoning, session.ContentTypeMarkdown)
			cls := "user"
			role := "user"
			if t.Message.Role == session.RoleAssistant {
				cls = "assistant"
				role = "assistant"
			} else if t.Message.Role == session.RoleSystem {
				cls = "assistant"
				role = "system"
			}
			items = append(items, item{
				IsMessage:    true,
				MessageClass: cls,
				RoleLabel:    role,
				ContentHTML:  contentHTML,
				Reasoning:    reasoningHTML,
				Usage:        usage,
			})
		case session.KindAudit:
			if t.Audit == nil {
				continue
			}
			args := string(t.Audit.Args)
			resultSummary := t.Audit.ResultSummary
			toolError := t.Audit.Error
			if redactOn {
				args = redact.Secrets(args)
				resultSummary = redact.Secrets(resultSummary)
				toolError = redact.Secrets(toolError)
			}
			items = append(items, item{
				IsTool:        true,
				ToolName:      t.Audit.ToolName,
				ResultSummary: resultSummary,
				ToolArgs:      args,
				ToolError:     toolError,
			})
		}
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, data{Title: title, Items: items}); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return []byte(b.String()), nil
}

// Write renders and writes the file to path (defaulting to the working dir
// when path is empty).
func Write(state *session.State, path string, redactOn bool) error {
	if path == "" {
		path = filepath.Join(state.WorkingDir, "marshal-session-"+state.SessionID()+".html")
	}
	htmlBytes, err := Render(state, redactOn)
	if err != nil {
		return err
	}
	return os.WriteFile(path, htmlBytes, 0o644)
}

// safeParser returns a goldmark parser configured to drop raw HTML
// (block-level and inline) so that exported sessions cannot smuggle
// <script>, <iframe>, or other script-execution vectors into the
// shared HTML artifact. Goldmark v1.7.x does not expose a high-level
// "disable raw HTML" toggle, so we rebuild the block/inline parser
// lists with the raw-HTML parsers excluded.
func safeParser() parser.Parser {
	return parser.NewParser(
		parser.WithBlockParsers(safeBlockParsers()...),
		parser.WithInlineParsers(safeInlineParsers()...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)
}

func safeBlockParsers() []util.PrioritizedValue {
	rawHTML := parser.NewHTMLBlockParser()
	defaults := parser.DefaultBlockParsers()
	out := make([]util.PrioritizedValue, 0, len(defaults))
	for _, p := range defaults {
		if p.Value == rawHTML {
			continue
		}
		out = append(out, p)
	}
	return out
}

func safeInlineParsers() []util.PrioritizedValue {
	rawHTML := parser.NewRawHTMLParser()
	defaults := parser.DefaultInlineParsers()
	out := make([]util.PrioritizedValue, 0, len(defaults))
	for _, p := range defaults {
		if p.Value == rawHTML {
			continue
		}
		out = append(out, p)
	}
	return out
}

func contentToHTML(md goldmark.Markdown, content string, ct session.ContentType) template.HTML {
	// Code-like content types are rendered as escaped <pre><code> blocks
	// rather than run through the markdown renderer. Fenced code blocks
	// would break when the content itself contains literal backticks, and
	// markdown would mangle code as headings, lists, or other constructs.
	switch ct {
	case session.ContentTypeCode, session.ContentTypeToolResult,
		session.ContentTypeDiff, session.ContentTypePlan:
		return template.HTML("<pre><code>" + html.EscapeString(content) + "</code></pre>")
	}
	if ct == session.ContentTypeMarkdown ||
		strings.Contains(content, "\n") ||
		strings.ContainsAny(content, "*`#") {
		var sb strings.Builder
		if err := md.Convert([]byte(content), &sb); err != nil {
			return template.HTML(html.EscapeString(content))
		}
		return template.HTML(sb.String())
	}
	return template.HTML(html.EscapeString(content))
}
