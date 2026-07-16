package native

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"marshal/internal/tools/registry"
)

const (
	defaultWebFetchTimeout = 30 * time.Second
)

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

type webFetchArgs struct {
	URL string `json:"url"`
}

type webSearchArgs struct {
	Query string `json:"query"`
}

func (t *toolSet) webFetchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "web.fetch",
		Description: "Fetch a URL and return its content as plain text. Only available when web tools are enabled.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`),
		Risk:        registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		decoded, err := decodeArgs[webFetchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if !t.webEnabled {
			return registry.ToolResult{}, fmt.Errorf("web tools are disabled")
		}
		target := strings.TrimSpace(decoded.URL)
		if target == "" {
			return registry.ToolResult{}, fmt.Errorf("url is required")
		}
		parsed, err := url.Parse(target)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return registry.ToolResult{}, fmt.Errorf("only http(s) URLs are allowed")
		}
		if t.ssrfCheck(parsed) {
			return registry.ToolResult{}, fmt.Errorf("URL resolves to a private or link-local address")
		}

		timeout := t.webFetchTimeout
		if timeout <= 0 {
			timeout = defaultWebFetchTimeout
		}
		client := t.webHTTPClient
		if client == nil {
			client = &http.Client{
				Timeout: timeout,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if len(via) >= 5 {
						return fmt.Errorf("too many redirects")
					}
					u, err := url.Parse(req.URL.String())
					if err != nil {
						return fmt.Errorf("invalid redirect target: %w", err)
					}
					if t.ssrfCheck(u) {
						return fmt.Errorf("redirect to private or link-local address blocked: %s", req.URL.String())
					}
					return nil
				},
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return registry.ToolResult{}, err
		}
		req.Header.Set("User-Agent", "marshal/0.1")
		resp, err := client.Do(req)
		if err != nil {
			return registry.ToolResult{}, err
		}
		defer resp.Body.Close()

		cap := int64(t.maxOutputBytes) + 1
		body, err := io.ReadAll(io.LimitReader(resp.Body, cap))
		if err != nil {
			return registry.ToolResult{}, err
		}
		text := htmlToText(string(body))
		summary := fmt.Sprintf("fetched %s (%d bytes)", target, len(text))
		return registry.ToolResult{
			Summary: summary,
			Content: limitOutput(text, t.maxOutputBytes),
		}, nil
	}
	return tool
}

func (t *toolSet) webSearchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "web.search",
		Description: "Search the web using the configured JSON search provider and return top results as JSON. Only available when both web tools are enabled and a search provider URL is configured.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		Risk:        registry.RiskNetwork,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		decoded, err := decodeArgs[webSearchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if !t.webEnabled {
			return registry.ToolResult{}, fmt.Errorf("web tools are disabled")
		}
		if t.webSearchURL == "" {
			return registry.ToolResult{}, fmt.Errorf("web search provider is not configured")
		}
		query := strings.TrimSpace(decoded.Query)
		if query == "" {
			return registry.ToolResult{}, fmt.Errorf("query is required")
		}

		timeout := t.webFetchTimeout
		if timeout <= 0 {
			timeout = defaultWebFetchTimeout
		}
		client := t.webHTTPClient
		if client == nil {
			client = &http.Client{Timeout: timeout}
		} else if client.Timeout == 0 {
			client2 := *client
			client2.Timeout = timeout
			client = &client2
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.webSearchURL, strings.NewReader("query="+url.QueryEscape(query)))
		if err != nil {
			return registry.ToolResult{}, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "marshal/0.1")
		if t.webSearchKey != "" {
			req.Header.Set("Authorization", "Bearer "+t.webSearchKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return registry.ToolResult{}, err
		}
		defer resp.Body.Close()

		cap := int64(t.maxOutputBytes) + 1
		body, err := io.ReadAll(io.LimitReader(resp.Body, cap))
		if err != nil {
			return registry.ToolResult{}, err
		}
		text := strings.TrimSpace(string(body))
		summary := fmt.Sprintf("searched %q (%d bytes)", query, len(text))
		return registry.ToolResult{
			Summary: summary,
			Content: limitOutput(text, t.maxOutputBytes),
		}, nil
	}
	return tool
}

func isPrivateURL(u *url.URL) bool {
	host := u.Hostname()
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate()
	}
	return false
}

func htmlToText(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(s)
}
