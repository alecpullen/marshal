package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/desktop/browser"
	"marshal/internal/tools/registry"
)

type toolSet struct {
	cfg            config.DesktopConfig
	backendFactory func() (browser.BrowserBackend, error)
	session        *browser.Session
	sessionMu      sync.Mutex
	sessionState   *session.State
}

func (ts *toolSet) getSession(ctx context.Context) (*browser.Session, error) {
	ts.sessionMu.Lock()
	defer ts.sessionMu.Unlock()
	if ts.session != nil {
		return ts.session, nil
	}
	backend, err := ts.backendFactory()
	if err != nil {
		return nil, fmt.Errorf("create browser backend: %w", err)
	}
	ts.session = browser.NewSession(backend)
	ts.setActive(false, "")
	return ts.session, nil
}

// setActive marks the browser session busy/idle around a tool call,
// merging into the current state so the URL/Title recorded by navigate
// (and click/submit) survive. SetBrowserInfo replaces the whole struct,
// so writing a partial literal would wipe those fields.
func (ts *toolSet) setActive(active bool, toolName string) {
	if ts.sessionState == nil {
		return
	}
	info := ts.sessionState.BrowserInfo()
	info.SessionOpen = true
	info.Active = active
	info.ToolName = toolName
	info.Mode = ts.cfg.Mode
	info.UpdatedAt = time.Now()
	ts.sessionState.SetBrowserInfo(info)
}

// withPage runs fn with the current browser page, marking the session
// active around the call and restoring idle state afterwards. It
// centralises the getSession/Page prologue shared by every browser tool.
func (ts *toolSet) withPage(ctx context.Context, toolName string, fn func(page browser.PageHandle) (registry.ToolResult, error)) (registry.ToolResult, error) {
	sess, err := ts.getSession(ctx)
	if err != nil {
		return registry.ToolResult{}, err
	}
	page, err := sess.Page(ctx)
	if err != nil {
		return registry.ToolResult{}, err
	}
	ts.setActive(true, toolName)
	res, err := fn(page)
	ts.setActive(false, "")
	return res, err
}

// setLocation records the current page URL/title, merging into existing
// browser state so Active/SessionOpen survive (same merge discipline as
// setActive).
func (ts *toolSet) setLocation(url, title string) {
	if ts.sessionState == nil {
		return
	}
	info := ts.sessionState.BrowserInfo()
	info.SessionOpen = true
	info.URL = url
	info.Title = title
	info.Mode = ts.cfg.Mode
	info.UpdatedAt = time.Now()
	ts.sessionState.SetBrowserInfo(info)
}

func RegisterAll(reg *registry.Registry, opts Options) (func(), error) {
	if !opts.Config.Enabled {
		return nil, nil
	}
	ts := &toolSet{
		cfg:            opts.Config,
		backendFactory: opts.BackendFactory,
		sessionState:   opts.SessionState,
	}
	tools := []registry.Tool{
		ts.navigateTool(),
		ts.readTool(),
		ts.clickTool(),
		ts.fillTool(),
		ts.submitTool(),
		ts.screenshotTool(),
	}
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			return nil, fmt.Errorf("register %s: %w", tool.Name, err)
		}
	}
	closer := func() {
		ts.sessionMu.Lock()
		sess := ts.session
		ts.session = nil
		ts.sessionMu.Unlock()
		if sess != nil {
			_ = sess.Close()
		}
	}
	return closer, nil
}

func (ts *toolSet) navigateTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.navigate",
		Description: "Navigate the browser to a URL. Requires approval (network risk). Subject to URL allow/deny list policy.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","minLength":1,"description":"The URL to navigate to"}},"required":["url"],"additionalProperties":false}`),
		Risk:        registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			URL string `json:"url"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		if err := urlAllowed(args.URL, ts.cfg.URLAllowlist, ts.cfg.URLDenylist); err != nil {
			return registry.ToolResult{}, err
		}
		return ts.withPage(ctx, "browser.navigate", func(page browser.PageHandle) (registry.ToolResult, error) {
			ts.setLocation(args.URL, "")
			if err := page.Navigate(ctx, args.URL); err != nil {
				return registry.ToolResult{}, fmt.Errorf("navigate: %w", err)
			}
			title, _ := page.Title(ctx)
			currentURL, _ := page.URL(ctx)
			ts.setLocation(currentURL, title)
			return registry.ToolResult{
				Summary: fmt.Sprintf("Navigated to %s", args.URL),
				Content: fmt.Sprintf(`{"url":%q,"title":%q}`, args.URL, title),
			}, nil
		})
	}
	return tool
}

func (ts *toolSet) readTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.read",
		Description: "Read simplified readable text from the current page. Optional selector targets a specific element.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"selector":{"type":"string","description":"CSS selector for a specific element. Omit for full page."}},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Selector string `json:"selector"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		return ts.withPage(ctx, "browser.read", func(page browser.PageHandle) (registry.ToolResult, error) {
			var text string
			var err error
			if args.Selector != "" {
				text, err = page.Text(ctx, args.Selector)
			} else {
				text, err = page.ReadableText(ctx)
			}
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("read: %w", err)
			}
			return registry.ToolResult{
				Summary: fmt.Sprintf("Read page: %d chars", len(text)),
				Content: text,
			}, nil
		})
	}
	return tool
}

func (ts *toolSet) clickTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.click",
		Description: "Click an element by CSS selector. Uses Playwright auto-waiting.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"selector":{"type":"string","minLength":1,"description":"CSS selector for the element to click"}},"required":["selector"],"additionalProperties":false}`),
		Risk:        registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Selector string `json:"selector"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		if args.Selector == "" {
			return registry.ToolResult{}, fmt.Errorf("selector is required")
		}
		return ts.withPage(ctx, "browser.click", func(page browser.PageHandle) (registry.ToolResult, error) {
			if err := page.Click(ctx, args.Selector); err != nil {
				return registry.ToolResult{}, fmt.Errorf("click %s: %w", args.Selector, err)
			}
			clickURL, _ := page.URL(ctx)
			clickTitle, _ := page.Title(ctx)
			ts.setLocation(clickURL, clickTitle)
			return registry.ToolResult{
				Summary: fmt.Sprintf("Clicked %s", args.Selector),
			}, nil
		})
	}
	return tool
}

func (ts *toolSet) fillTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.fill",
		Description: "Type text into an input or textarea element. Clears existing content by default.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"selector":{"type":"string","minLength":1,"description":"CSS selector for the input element"},"value":{"type":"string","description":"Text to type"},"clear":{"type":"boolean","description":"Clear field before filling (default true)"}},"required":["selector","value"],"additionalProperties":false}`),
		Risk:        registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Selector string `json:"selector"`
			Value    string `json:"value"`
			Clear    *bool  `json:"clear"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		if args.Selector == "" {
			return registry.ToolResult{}, fmt.Errorf("selector is required")
		}
		if args.Value == "" && args.Clear == nil {
			return registry.ToolResult{}, fmt.Errorf("value is required")
		}
		return ts.withPage(ctx, "browser.fill", func(page browser.PageHandle) (registry.ToolResult, error) {
			if err := page.Fill(ctx, args.Selector, args.Value); err != nil {
				return registry.ToolResult{}, fmt.Errorf("fill %s: %w", args.Selector, err)
			}
			return registry.ToolResult{
				Summary: fmt.Sprintf("Filled %s with %q", args.Selector, args.Value),
			}, nil
		})
	}
	return tool
}

func (ts *toolSet) submitTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.submit",
		Description: "Submit a form by clicking a submit button selector, or pressing Enter on the focused element if no selector given.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"selector":{"type":"string","description":"CSS selector for submit button. Omit to press Enter on focused element."}},"additionalProperties":false}`),
		Risk:        registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Selector string `json:"selector"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		return ts.withPage(ctx, "browser.submit", func(page browser.PageHandle) (registry.ToolResult, error) {
			if args.Selector != "" {
				if err := page.Submit(ctx, args.Selector); err != nil {
					return registry.ToolResult{}, fmt.Errorf("submit %s: %w", args.Selector, err)
				}
			} else {
				if err := page.PressKey(ctx, "Enter"); err != nil {
					return registry.ToolResult{}, fmt.Errorf("press Enter: %w", err)
				}
			}
			submitURL, _ := page.URL(ctx)
			submitTitle, _ := page.Title(ctx)
			ts.setLocation(submitURL, submitTitle)
			if args.Selector != "" {
				return registry.ToolResult{
					Summary: fmt.Sprintf("Submitted %s", args.Selector),
				}, nil
			}
			return registry.ToolResult{
				Summary: "Submitted (pressed Enter)",
			}, nil
		})
	}
	return tool
}

func (ts *toolSet) screenshotTool() registry.Tool {
	tool := registry.Tool{
		Name:        "browser.screenshot",
		Description: "Capture a screenshot of the current page. Returns metadata (dimensions, timestamp). In a future vision milestone, returns image bytes.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"full_page":{"type":"boolean","description":"Capture full scrollable page (default false)"}},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			FullPage bool `json:"full_page"`
		}
		if err := decodeArgs(tool, call.Args, &args); err != nil {
			return registry.ToolResult{}, err
		}
		return ts.withPage(ctx, "browser.screenshot", func(page browser.PageHandle) (registry.ToolResult, error) {
			_, err := page.Screenshot(ctx, browser.ScreenshotOpts{FullPage: args.FullPage, Format: ts.cfg.ScreenshotFormat})
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("screenshot: %w", err)
			}
			return registry.ToolResult{
				Summary: fmt.Sprintf("Screenshot captured (full_page=%v, format=%s)", args.FullPage, ts.cfg.ScreenshotFormat),
				Content: fmt.Sprintf(`{"full_page":%v,"format":%q}`, args.FullPage, ts.cfg.ScreenshotFormat),
			}, nil
		})
	}
	return tool
}

func decodeArgs(tool registry.Tool, raw json.RawMessage, target any) error {
	if err := registry.ValidateArgs(tool, raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode %s arguments: %w", tool.Name, err)
	}
	return nil
}
