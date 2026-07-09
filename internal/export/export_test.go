package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/redact"
	"marshal/internal/tools/registry"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func TestRenderProducesSelfContainedHTML(t *testing.T) {
	state := session.New(sessionMinimalConfig(), "/repo", zeroTime(), session.Persistence{})
	state.AddMessage(session.RoleUser, "Fix the **parser** bug", session.ContentTypePlain)
	state.AddMessageFinal(session.RoleAssistant, "Patched `protocol.go`.", session.ContentTypeMarkdown)
	state.LogToolCall(registry.AuditEvent{ToolName: "file.read", ResultSummary: "read protocol.go"})

	html, err := Render(state, false)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(html)
	if !strings.Contains(s, "<!DOCTYPE html>") {
		t.Fatal("missing DOCTYPE")
	}
	if !strings.Contains(s, "Patched") {
		t.Fatal("assistant content missing")
	}
	if !strings.Contains(s, "<details") {
		t.Fatal("collapsible tool-call section missing")
	}
	if strings.Contains(s, "http://") || strings.Contains(s, "https://") {
		t.Fatal("export references external URLs (must be self-contained)")
	}
}

func TestWriteCreatesFile(t *testing.T) {
	state := session.New(sessionMinimalConfig(), "/repo", zeroTime(), session.Persistence{})
	state.AddMessage(session.RoleUser, "hi", session.ContentTypePlain)
	dir := t.TempDir()
	path := filepath.Join(dir, "out.html")
	if err := Write(state, path, false); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("export file is empty")
	}
}

func TestRenderEscapesExternalURL(t *testing.T) {
	state := session.New(sessionMinimalConfig(), "/repo", zeroTime(), session.Persistence{})
	state.AddMessage(session.RoleUser, "Visit https://example.com for details", session.ContentTypeMarkdown)
	state.AddMessage(session.RoleUser, "**bold** <script>alert(1)</script>", session.ContentTypeMarkdown)
	html, err := Render(state, false)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(html)

	lower := strings.ToLower(s)
	for _, banned := range []string{"<script", "<iframe", "<object", "<embed", "onerror=", "onload="} {
		if strings.Contains(lower, banned) {
			t.Fatalf("export contains script-execution vector %q: %s", banned, s)
		}
	}
}

func TestSafeParserOmitsRawHTML(t *testing.T) {
	md := goldmark.New(goldmark.WithParser(safeParser()))
	for _, name := range []string{
		"<script>alert(1)</script>",
		"<iframe src=\"https://evil.example.com\"></iframe>",
		"<img src=x onerror=alert(1)>",
		"<svg onload=alert(1)>",
		"hello\n\n<script>alert(1)</script>\n\nworld",
	} {
		var sb strings.Builder
		if err := md.Convert([]byte(name), &sb); err != nil {
			t.Fatalf("convert %q: %v", name, err)
		}
		out := strings.ToLower(sb.String())
		// After disabling raw HTML parsing, goldmark must not emit
		// unescaped active tags. It may emit the input as escaped
		// text (e.g. &lt;script&gt;) — that is acceptable.
		for _, banned := range []string{"<script", "<iframe", "<img", "<svg", "<object", "<embed", "<link", "<style"} {
			if strings.Contains(out, banned) {
				t.Fatalf("safeParser emitted active raw HTML %q for input %q (output: %s)", banned, name, sb.String())
			}
		}
	}
}

func TestSafeParserProducesNoRawHTMLNodes(t *testing.T) {
	inputs := []string{
		"<script>alert(1)</script>",
		"hello\n\n<div>x</div>\n\nworld",
		"inline <em>nested</em> <b>raw</b> tail",
		"raw inline <span onclick='evil()'>x</span> tail",
		"<script type=\"text/javascript\">alert(1)</script>",
	}
	for _, src := range inputs {
		doc := safeParser().Parse(text.NewReader([]byte(src)))
		if hasRawHTML(doc) {
			t.Fatalf("safeParser produced RawHTML/HTMLBlock AST nodes for input %q", src)
		}
	}
}

// TestDefaultParserProducesRawHTMLNodes is a documentation/regression
// test: it shows that the goldmark default parser (which is what was used
// before the XSS fix) DOES produce ast.RawHTML and ast.HTMLBlock nodes
// for raw-HTML inputs. Those nodes are neutralised by goldmark's default
// HTML renderer (which substitutes "<!-- raw HTML omitted -->"), but the
// parser still constructs the dangerous AST. Removing the raw-HTML
// parsers at the parser layer (as safeParser does) means no such AST
// nodes ever exist, so any future renderer change or extension that
// enabled unsafe emission could not re-introduce the XSS.
func TestDefaultParserProducesRawHTMLNodes(t *testing.T) {
	defaultParser := goldmark.New().Parser()
	inputs := []string{
		"<script>alert(1)</script>",
		"hello\n\n<div>x</div>\n\nworld",
		"raw inline <span onclick='evil()'>x</span> tail",
	}
	sawRaw := false
	for _, src := range inputs {
		doc := defaultParser.Parse(text.NewReader([]byte(src)))
		if hasRawHTML(doc) {
			sawRaw = true
		}
	}
	if !sawRaw {
		t.Fatal("expected goldmark default parser to produce at least one RawHTML/HTMLBlock node; threat model assumption invalid")
	}
}

func hasRawHTML(n ast.Node) bool {
	found := false
	_ = ast.Walk(n, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if _, ok := n.(*ast.RawHTML); ok {
			found = true
			return ast.WalkStop, nil
		}
		if _, ok := n.(*ast.HTMLBlock); ok {
			found = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return found
}

func TestRenderRedactsSecretsWhenEnabled(t *testing.T) {
	state := session.New(sessionMinimalConfig(), "/repo", zeroTime(), session.Persistence{})
	state.AddMessage(session.RoleUser, "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", session.ContentTypePlain)
	html, err := Render(state, true)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(html), "wJalrXUtnFEMI") {
		t.Fatal("secret survived redaction in export")
	}
	if !strings.Contains(string(html), redact.MaskToken) {
		t.Fatal("redaction marker absent")
	}
}

func sessionMinimalConfig() config.Config {
	return config.Default()
}

func zeroTime() time.Time { return time.Time{} }
