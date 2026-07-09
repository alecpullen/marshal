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
