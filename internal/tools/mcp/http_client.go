package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// mcpSessionIDHeader is the request/response header used by the Streamable
// HTTP transport to correlate a session across calls.
const mcpSessionIDHeader = "Mcp-Session-Id"

// httpClientTimeout is the default per-request timeout applied when the
// caller's context carries no deadline. It is a var (not const) so tests
// can override it.
var httpClientTimeout = 30 * time.Second

// maxHTTPErrorBodyBytes bounds how much of a non-2xx response body is
// echoed back in the returned error.
const maxHTTPErrorBodyBytes = 4 << 10

// HTTPClientOption configures an HTTPClient.
type HTTPClientOption func(*HTTPClient)

// WithHTTPClientLogger sets the logger on an HTTPClient. When nil (or unset),
// the client uses slog.Default().
func WithHTTPClientLogger(l *slog.Logger) HTTPClientOption {
	return func(c *HTTPClient) { c.Logger = l }
}

// HTTPClient is a Streamable-HTTP MCP client. It speaks JSON-RPC over HTTP
// POST and accepts either a single JSON response body or an SSE-framed
// response stream, matching the single-endpoint Streamable HTTP model.
//
// It satisfies the caller interface, so RegisterTools works unchanged.
type HTTPClient struct {
	Name    string
	URL     string
	Headers map[string]string

	Logger *slog.Logger // nil → slog.Default()

	http *http.Client

	mu        sync.Mutex
	closed    bool
	sessionID string

	nextID int64
}

// NewHTTPClient returns a Streamable-HTTP MCP client for a remote endpoint.
// headers are already env-resolved by the caller.
func NewHTTPClient(name, url string, headers map[string]string, opts ...HTTPClientOption) *HTTPClient {
	// Clone the default transport so this client's timeouts are its own. The
	// assertion is guarded: a wrapped DefaultTransport (a test or embedding
	// host may install one) falls back to a fresh transport rather than
	// panicking.
	transport := &http.Transport{ResponseHeaderTimeout: httpClientTimeout}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
		transport.ResponseHeaderTimeout = httpClientTimeout
	}

	c := &HTTPClient{
		Name:    name,
		URL:     url,
		Headers: make(map[string]string, len(headers)),
		http:    &http.Client{Transport: transport},
	}
	for k, v := range headers {
		c.Headers[k] = v
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ServerName returns the configured server name, satisfying the caller
// interface so the manager can address clients generically.
func (c *HTTPClient) ServerName() string { return c.Name }

// log returns the client's logger, defaulting to slog.Default() when the
// Logger field is nil.
func (c *HTTPClient) log() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}
	return c.Logger
}

// Start performs the initialize handshake and then sends the
// notifications/initialized notification. The notification is
// fire-and-forget: its error is ignored, matching the stdio client.
func (c *HTTPClient) Start(ctx context.Context) error {
	var initRes InitializeResult
	initParams := InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      Implementation{Name: "marshal", Version: "1.0.0"},
	}
	if err := c.Call(ctx, "initialize", initParams, &initRes); err != nil {
		return fmt.Errorf("initialize handshake: %w", err)
	}
	_ = c.notify(ctx, "notifications/initialized", nil)
	return nil
}

// Close marks the client closed and releases idle connections. It is
// idempotent and does not block.
func (c *HTTPClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	c.http.CloseIdleConnections()
	c.log().Info("mcp http client closed", "name", c.Name)
	return nil
}

// Call sends a JSON-RPC request and decodes the matching response into
// result. It is safe for concurrent use.
func (c *HTTPClient) Call(ctx context.Context, method string, params, result any) error {
	n := atomic.AddInt64(&c.nextID, 1)
	id := json.Number(strconv.FormatInt(n, 10))
	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	resp, cleanup, err := c.post(ctx, req)
	if err != nil {
		return err
	}
	defer cleanup()
	defer resp.Body.Close()

	var res Response
	if isEventStream(resp.Header.Get("Content-Type")) {
		res, err = readSSEResponse(resp.Body, id)
	} else {
		res, err = readJSONResponse(resp.Body, id)
	}
	if err != nil {
		return err
	}
	if res.Error != nil {
		return fmt.Errorf("MCP error (%d): %s", res.Error.Code, res.Error.Message)
	}
	if result != nil {
		return json.Unmarshal(res.Result, result)
	}
	return nil
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (c *HTTPClient) notify(ctx context.Context, method string, params any) error {
	req := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	resp, cleanup, err := c.post(ctx, req)
	if err != nil {
		return err
	}
	defer cleanup()
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// post marshals req, POSTs it to the endpoint, and returns the raw HTTP
// response. The returned cleanup must be invoked once the response body has
// been consumed; it releases the per-request timeout context. The session id
// lock is never held across the round-trip.
func (c *HTTPClient) post(ctx context.Context, req Request) (*http.Response, func(), error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrClientClosed
	}
	sessionID := c.sessionID
	c.mu.Unlock()

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}

	// Apply the default per-request timeout only when the caller has not
	// supplied a deadline of their own.
	cleanup := func() {}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, httpClientTimeout)
		cleanup = cancel
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.Headers {
		httpReq.Header.Set(k, v)
	}
	if sessionID != "" {
		httpReq.Header.Set(mcpSessionIDHeader, sessionID)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	// Capture a session id handed out by the server (normally on the
	// initialize response) so later calls can echo it back.
	if sid := resp.Header.Get(mcpSessionIDHeader); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPErrorBodyBytes))
		resp.Body.Close()
		cleanup()
		return nil, nil, fmt.Errorf("mcp: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	return resp, cleanup, nil
}

// isEventStream reports whether a Content-Type denotes an SSE stream.
func isEventStream(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

// readJSONResponse decodes a single JSON-RPC response body and checks that it
// answers the request we sent, matching the SSE path's id check.
func readJSONResponse(r io.Reader, id json.Number) (Response, error) {
	var res Response
	if err := json.NewDecoder(r).Decode(&res); err != nil {
		return Response{}, fmt.Errorf("mcp: decode response: %w", err)
	}
	if res.ID != id {
		return Response{}, fmt.Errorf("mcp: response id %s does not match request id %s", res.ID, id)
	}
	return res, nil
}

// readSSEResponse reads an SSE stream and returns the first JSON-RPC message
// whose id matches. Unrelated messages (notifications, responses to other
// requests) are skipped. Multi-line data: fields are joined with newlines per
// the SSE spec. The function returns as soon as the matching message is seen,
// so a stream that stays open does not block the caller.
func readSSEResponse(r io.Reader, id json.Number) (Response, error) {
	scanner := bufio.NewScanner(r)
	// MCP tool results routinely exceed bufio's 64KB default token limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)

	var data []string
	dispatch := func() (Response, bool) {
		if len(data) == 0 {
			return Response{}, false
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		var res Response
		if err := json.Unmarshal([]byte(payload), &res); err != nil {
			return Response{}, false
		}
		if res.ID != id {
			return Response{}, false
		}
		return res, true
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			// Blank line terminates an event.
			if res, ok := dispatch(); ok {
				return res, nil
			}
		case strings.HasPrefix(line, ":"):
			// Comment / keep-alive.
		case strings.HasPrefix(line, "data:"):
			v := strings.TrimPrefix(line, "data:")
			v = strings.TrimPrefix(v, " ")
			data = append(data, v)
		default:
			// event:, id:, retry: and unknown fields are ignored.
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, fmt.Errorf("mcp: read event stream: %w", err)
	}
	// Flush a trailing event that was not terminated by a blank line.
	if res, ok := dispatch(); ok {
		return res, nil
	}
	return Response{}, fmt.Errorf("mcp: no response for id %s in event stream", id)
}
