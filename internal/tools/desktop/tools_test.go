package desktop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/desktop/browser"
	"marshal/internal/tools/registry"
)

func newTestToolSet(t *testing.T) (*registry.Registry, *browser.FakeBackend, *browser.FakePage) {
	t.Helper()
	reg := registry.New()
	backend := &browser.FakeBackend{}
	opts := Options{
		Config: config.DesktopConfig{
			Enabled:          true,
			Mode:             "standalone",
			Headless:         false,
			DefaultTimeout:   30_000_000_000,
			ScreenshotFormat: "png",
		},
		BackendFactory: func() (browser.BrowserBackend, error) {
			return backend, nil
		},
	}
	if closer, err := RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	} else {
		_ = closer
	}
	page := &browser.FakePage{TitleVal: "Test", URLVal: "https://example.com", ReadableTextVal: "page content here"}
	backend.Page = page
	return reg, backend, page
}

func toolByName(t *testing.T, reg *registry.Registry, name string) registry.Tool {
	t.Helper()
	tools := reg.List()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not registered", name)
	return registry.Tool{}
}

func TestRegisterAllRegistersSixTools(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	expected := []string{
		"browser.navigate", "browser.read", "browser.click",
		"browser.fill", "browser.submit", "browser.screenshot",
	}
	tools := reg.List()
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, exp := range expected {
		if !names[exp] {
			t.Errorf("tool %q not registered", exp)
		}
	}
}

func TestBrowserNavigateRiskNetwork(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.navigate")
	if tool.Risk != registry.RiskNetwork {
		t.Errorf("browser.navigate risk = %s, want network", tool.Risk)
	}
}

func TestBrowserReadRiskReadOnly(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.read")
	if tool.Risk != registry.RiskReadOnly {
		t.Errorf("browser.read risk = %s, want read_only", tool.Risk)
	}
}

func TestBrowserScreenshotRiskReadOnly(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.screenshot")
	if tool.Risk != registry.RiskReadOnly {
		t.Errorf("browser.screenshot risk = %s, want read_only", tool.Risk)
	}
}

func TestBrowserNavigateExecutes(t *testing.T) {
	reg, _, page := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.navigate")
	args, _ := json.Marshal(map[string]any{"url": "https://example.com"})
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if page.NavigatedTo != "https://example.com" {
		t.Errorf("navigated to %q, want https://example.com", page.NavigatedTo)
	}
	if !strings.Contains(res.Summary, "https://example.com") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestBrowserNavigateBlockedByDenylist(t *testing.T) {
	reg := registry.New()
	backend := &browser.FakeBackend{}
	opts := Options{
		Config: config.DesktopConfig{
			Enabled:        true,
			URLDenylist:    []string{"blocked.com"},
			DefaultTimeout: 30_000_000_000,
		},
		BackendFactory: func() (browser.BrowserBackend, error) {
			return backend, nil
		},
	}
	if closer, err := RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	} else {
		_ = closer
	}
	tool := toolByName(t, reg, "browser.navigate")
	args, _ := json.Marshal(map[string]any{"url": "https://blocked.com"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected denylist block")
	}
	if !strings.Contains(err.Error(), "blocked by policy") {
		t.Fatalf("error should mention policy: %v", err)
	}
}

func TestBrowserReadReturnsContent(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.read")
	args, _ := json.Marshal(map[string]any{})
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Content, "page content here") {
		t.Errorf("content = %q", res.Content)
	}
}

func TestBrowserClickExecutes(t *testing.T) {
	reg, _, page := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.click")
	args, _ := json.Marshal(map[string]any{"selector": "#submit-btn"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(page.ClickedSelectors) != 1 || page.ClickedSelectors[0] != "#submit-btn" {
		t.Errorf("clicked %v, want [#submit-btn]", page.ClickedSelectors)
	}
}

func TestBrowserFillExecutes(t *testing.T) {
	reg, _, page := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.fill")
	args, _ := json.Marshal(map[string]any{"selector": "#name", "value": "hello world"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if page.FilledInputs["#name"] != "hello world" {
		t.Errorf("filled %v", page.FilledInputs)
	}
}

func TestBrowserSubmitExecutes(t *testing.T) {
	reg, _, page := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.submit")
	args, _ := json.Marshal(map[string]any{"selector": "form#login"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if page.SubmittedSel != "form#login" {
		t.Errorf("submitted %q, want form#login", page.SubmittedSel)
	}
}

func TestBrowserScreenshotReturnsMetadata(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.screenshot")
	args, _ := json.Marshal(map[string]any{})
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "screenshot") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestBrowserFillRequiresSelector(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.fill")
	args, _ := json.Marshal(map[string]any{"value": "no selector"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for missing selector")
	}
}

func TestBrowserNavigateRequiresURL(t *testing.T) {
	reg, _, _ := newTestToolSet(t)
	tool := toolByName(t, reg, "browser.navigate")
	args, _ := json.Marshal(map[string]any{})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for missing url")
	}
}

func TestRegisterAllDisabledDoesNotRegister(t *testing.T) {
	reg := registry.New()
	opts := Options{
		Config: config.DesktopConfig{Enabled: false},
		BackendFactory: func() (browser.BrowserBackend, error) {
			return &browser.FakeBackend{}, nil
		},
	}
	if closer, err := RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	} else {
		_ = closer
	}
	if len(reg.List()) != 0 {
		t.Fatalf("expected 0 tools when disabled, got %d", len(reg.List()))
	}
}

// TestBrowserReadPreservesURLAndTitle: post-op state updates from
// read/fill/screenshot must merge with existing state, not replace it —
// replacing wipes the URL/Title that navigate recorded (the TUI browser
// bar loses the current page after any read).
func TestBrowserReadPreservesURLAndTitle(t *testing.T) {
	reg := registry.New()
	backend := &browser.FakeBackend{}
	page := &browser.FakePage{TitleVal: "Example", URLVal: "https://example.com", ReadableTextVal: "content"}
	backend.Page = page
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	opts := Options{
		Config:         config.DesktopConfig{Enabled: true, Mode: "standalone", ScreenshotFormat: "png"},
		BackendFactory: func() (browser.BrowserBackend, error) { return backend, nil },
		SessionState:   state,
	}
	if _, err := RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	navArgs, _ := json.Marshal(map[string]any{"url": "https://example.com"})
	if _, err := toolByName(t, reg, "browser.navigate").Handler(context.Background(), registry.ToolCall{Args: navArgs}); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if _, err := toolByName(t, reg, "browser.read").Handler(context.Background(), registry.ToolCall{Args: []byte(`{}`)}); err != nil {
		t.Fatalf("read: %v", err)
	}

	info := state.BrowserInfo()
	if info.URL != "https://example.com" || info.Title != "Example" {
		t.Fatalf("after read: URL/Title = %q/%q, want https://example.com/Example preserved", info.URL, info.Title)
	}
	if info.Active {
		t.Fatal("Active should be false after read completes")
	}
}
