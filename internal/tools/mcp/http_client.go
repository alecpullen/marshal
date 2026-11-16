package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"marshal/internal/oauth"
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

// WithHTTPClient installs the *http.Client whose Do sends this client's
// requests. It replaces only where the bytes are written — a Unix-socket
// dialer, a pinned certificate pool, a timeout — and changes nothing else: the
// endpoint is still whatever URL the caller configured, the token-source rules
// below still apply, and ValidateRemoteServer remains the gate for a configured
// remote server. A nil client is ignored, leaving the default transport in
// place.
func WithHTTPClient(hc *http.Client) HTTPClientOption {
	return func(c *HTTPClient) {
		if hc != nil {
			c.http = hc
		}
	}
}

// WithHTTPOAuth installs a token source that supplies an OAuth bearer token
// for each request. src is consulted once per POST so a refreshed token is
// picked up without rebuilding the client.
//
// When src returns a non-empty token, the request carries
// "Authorization: Bearer <token>". Precedence: the token source wins over a
// static Authorization entry in the client's Headers map, because the token
// is per-request and freshly sourced while Headers is fixed at construction.
//
// When the source yields no token (empty string or an error), the client
// reports *oauth.ErrAuthRequired instead of sending the request, so callers
// can drive an authorization flow.
func WithHTTPOAuth(src func(ctx context.Context) (string, error)) HTTPClientOption {
	return func(c *HTTPClient) { c.tokenSource = src }
}

// WithHTTPRequestHeaders installs a source of extra request headers consulted
// once per POST, beside the token source and for the same reason: the value is
// per-request while Headers is fixed at construction.
//
// The source never overrides Authorization and never replaces the client's own
// headers, and it is consulted only while a request is being built — it is not
// recorded, logged, described to a model, or kept after the request. A nil
// source is ignored.
func WithHTTPRequestHeaders(src func(ctx context.Context) map[string]string) HTTPClientOption {
	return func(c *HTTPClient) {
		if src != nil {
			c.requestHeaders = src
		}
	}
}

// WithHTTPRevalidator installs the hook a request failure re-runs discovery and
// enrollment through. It is consulted at most once per call, and only for a 401
// (a credential the server no longer accepts) or a transport failure (nothing
// answered at all): a request that failed for any other reason is returned as
// it is.
//
// The retry that follows re-sends the *same* JSON-RPC request — same id, same
// params — so an uncertain outcome is reconciled by the server's own
// idempotency instead of becoming a second action. A failed revalidation
// returns the original error: the caller's session keeps working without the
// endpoint.
func WithHTTPRevalidator(fn func(ctx context.Context) error) HTTPClientOption {
	return func(c *HTTPClient) {
		if fn != nil {
			c.revalidate = fn
		}
	}
}

// WithUserDirectionConfirm installs the confirmer for a built-in operation the
// server refused because it acts on a person's instruction. header names the
// request header such a receipt travels in; confirm is called at most once per
// call, and only when a tools/call result carries code
// "user_direction_required" — no other failure is retried.
//
// confirm returns the receipt to present; the client then re-sends the same
// request (same id, same params) once, with the receipt in header. An empty
// receipt or an error from confirm ends the attempt: the caller sees the
// refusal, and nothing is retried.
func WithUserDirectionConfirm(header string, confirm func(ctx context.Context, challenge UserDirectionChallenge) (string, error)) HTTPClientOption {
	return func(c *HTTPClient) {
		if strings.TrimSpace(header) == "" || confirm == nil {
			return
		}
		c.userDirection = &userDirectionConfirm{header: strings.TrimSpace(header), confirm: confirm}
	}
}

// UserDirectionChallenge is one operation a built-in endpoint refused for want
// of a person's direction, with the opaque receipt it minted for exactly that
// operation. Receipt and ExpiresAt are the endpoint's; Tool and Arguments are
// the operation the caller asked for, so the confirmation it produces names one
// operation rather than the surface as a whole.
type UserDirectionChallenge struct {
	Receipt   string
	ExpiresAt time.Time
	Tool      string
	Arguments map[string]any
}

// userDirectionConfirm is WithUserDirectionConfirm's configured hook.
type userDirectionConfirm struct {
	header  string
	confirm func(ctx context.Context, challenge UserDirectionChallenge) (string, error)
}

// MCPError is a JSON-RPC error the server answered with. Code is the protocol
// code, so a caller can tell an unknown method or tool from a refusal without
// parsing the message.
type MCPError struct {
	Code    int
	Message string
}

func (e *MCPError) Error() string {
	return fmt.Sprintf("MCP error (%d): %s", e.Code, e.Message)
}

// tokenSourceError couples an *oauth.ErrAuthRequired with the underlying
// failure from the token source. errors.Is reaches the sentinel through Is
// and the source error through Unwrap.
type tokenSourceError struct {
	auth  *oauth.ErrAuthRequired
	cause error
}

func (e *tokenSourceError) Error() string { return e.auth.Error() }

func (e *tokenSourceError) Unwrap() error { return e.cause }

// Is reports whether target matches the wrapped authentication error. Cause
// matching falls through to Unwrap, so errors.Is still reaches the token
// source's own error.
func (e *tokenSourceError) Is(target error) bool { return errors.Is(e.auth, target) }

// As forwards to the wrapped authentication error so errors.As can extract an
// *oauth.ErrAuthRequired; Unwrap alone points at the token source's cause.
func (e *tokenSourceError) As(target any) bool { return errors.As(e.auth, target) }

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

	// tokenSource, when non-nil, supplies an OAuth bearer token for every
	// outgoing request. It is nil unless WithHTTPOAuth was supplied.
	tokenSource func(ctx context.Context) (string, error)

	// requestHeaders, when non-nil, yields extra headers for one POST. It is
	// nil unless WithHTTPRequestHeaders was supplied.
	requestHeaders func(ctx context.Context) map[string]string

	// revalidate, when non-nil, re-runs discovery and enrollment after a 401
	// or a transport failure, at most once per call. It is nil unless
	// WithHTTPRevalidator was supplied.
	revalidate func(ctx context.Context) error

	// userDirection, when non-nil, admits one refused built-in operation per
	// call after the caller's confirmer produces the receipt. It is nil unless
	// WithUserDirectionConfirm was supplied.
	userDirection *userDirectionConfirm

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
//
// A client configured with WithHTTPRevalidator or WithUserDirectionConfirm
// applies those rules here, and only here: a 401 or a transport failure
// revalidates once, a tool result that asks for a person's direction confirms
// once, and every retry re-sends this same request — the id and params the
// caller's call produced — so an uncertain outcome is reconciled rather than
// repeated as a new action.
func (c *HTTPClient) Call(ctx context.Context, method string, params, result any) error {
	n := atomic.AddInt64(&c.nextID, 1)
	id := json.Number(strconv.FormatInt(n, 10))
	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	// extra carries the headers this attempt alone presents (a user-direction
	// receipt). It is per-call and never recorded on the client.
	extra := map[string]string{}
	res, err := c.roundTrip(ctx, req, extra)
	if err != nil {
		if c.revalidate == nil || !recoverableFailure(err) {
			return err
		}
		// Re-run discovery and enrollment once, then re-send the same request.
		if rerr := c.revalidate(ctx); rerr != nil {
			return err
		}
		if res, err = c.roundTrip(ctx, req, extra); err != nil {
			return err
		}
	}
	if res.Error != nil {
		return &MCPError{Code: res.Error.Code, Message: res.Error.Message}
	}
	if err := decodeResult(res, result); err != nil {
		return err
	}
	if c.userDirection == nil || method != toolsCallMethod {
		return nil
	}
	challenge, ok := userDirectionChallengeOf(params, result)
	if !ok {
		return nil
	}
	receipt, cerr := c.userDirection.confirm(ctx, challenge)
	if cerr != nil || receipt == "" {
		// The person did not direct the operation: the caller keeps the
		// refusal it already has, and nothing is retried.
		return nil
	}
	extra[c.userDirection.header] = receipt
	res, err = c.roundTrip(ctx, req, extra)
	if err != nil {
		return err
	}
	if res.Error != nil {
		return &MCPError{Code: res.Error.Code, Message: res.Error.Message}
	}
	return decodeResult(res, result)
}

// toolsCallMethod is the one method the user-direction rule observes.
const toolsCallMethod = "tools/call"

// userDirectionRequiredCode is the tool-failure code an endpoint answers with
// when an operation needs a person's direction before it may run.
const userDirectionRequiredCode = "user_direction_required"

// decodeResult unmarshals a response's result into the caller's value.
func decodeResult(res Response, result any) error {
	if result == nil {
		return nil
	}
	return json.Unmarshal(res.Result, result)
}

// roundTrip POSTs one request and reads its JSON-RPC answer. extra holds the
// per-attempt headers; the request's id and params are never rewritten, so a
// retry is a reconciliation of the same operation.
func (c *HTTPClient) roundTrip(ctx context.Context, req Request, extra map[string]string) (Response, error) {
	resp, cleanup, err := c.post(ctx, req, extra)
	if err != nil {
		return Response{}, err
	}
	defer cleanup()
	defer resp.Body.Close()
	if isEventStream(resp.Header.Get("Content-Type")) {
		return readSSEResponse(resp.Body, req.ID)
	}
	return readJSONResponse(resp.Body, req.ID)
}

// recoverableFailure reports whether err is the class a revalidation can fix: a
// credential the server no longer accepts, or nothing answering at all. Every
// other failure — a refusal, a protocol error, a bad payload — is returned as
// it is rather than repeated.
func recoverableFailure(err error) bool {
	if err == nil {
		return false
	}
	var auth *oauth.ErrAuthRequired
	if errors.As(err, &auth) {
		return true
	}
	var transport *url.Error
	return errors.As(err, &transport)
}

// userDirectionChallengeOf reads the challenge out of a refused tools/call
// result. Only a tool that reported an error and whose text carries the code
// qualifies: a successful result whose text happens to mention the code is not
// a refusal and is never retried.
func userDirectionChallengeOf(params, result any) (UserDirectionChallenge, bool) {
	call, ok := params.(CallToolParams)
	if !ok {
		return UserDirectionChallenge{}, false
	}
	res, ok := result.(*CallToolResult)
	if !ok || res == nil || !res.IsError {
		return UserDirectionChallenge{}, false
	}
	for _, content := range res.Content {
		if content.Type != "text" {
			continue
		}
		var payload struct {
			Code      string `json:"code"`
			Receipt   string `json:"receipt"`
			ExpiresAt string `json:"expiresAt"`
		}
		if err := json.Unmarshal([]byte(content.Text), &payload); err != nil {
			continue
		}
		if payload.Code != userDirectionRequiredCode || payload.Receipt == "" {
			continue
		}
		challenge := UserDirectionChallenge{Receipt: payload.Receipt, Tool: call.Name}
		if payload.ExpiresAt != "" {
			if expiresAt, err := time.Parse(time.RFC3339, payload.ExpiresAt); err == nil {
				challenge.ExpiresAt = expiresAt
			}
		}
		if len(call.Arguments) > 0 {
			_ = json.Unmarshal(call.Arguments, &challenge.Arguments)
		}
		return challenge, true
	}
	return UserDirectionChallenge{}, false
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (c *HTTPClient) notify(ctx context.Context, method string, params any) error {
	req := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	resp, cleanup, err := c.post(ctx, req, nil)
	if err != nil {
		return err
	}
	defer cleanup()
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// post marshals req, POSTs it to the endpoint, and returns the raw HTTP
// response. extra holds the headers this attempt alone presents (a
// user-direction receipt). The returned cleanup must be invoked once the
// response body has been consumed; it releases the per-request timeout
// context. The session id lock is never held across the round-trip.
func (c *HTTPClient) post(ctx context.Context, req Request, extra map[string]string) (*http.Response, func(), error) {
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
	// Headers yielded per request come next, so a credential-bearing static
	// header cannot be replaced by one, and the token source below still wins
	// over everything. The source is consulted while the request is built and
	// its values are never recorded on the client.
	if c.requestHeaders != nil {
		for k, v := range c.requestHeaders(ctx) {
			httpReq.Header.Set(k, v)
		}
	}
	// A configured token source wins over a static Authorization header:
	// the token is per-request and freshly sourced, while c.Headers is fixed
	// at construction. The header is set on the outgoing request only; the
	// client's static Headers map is never mutated.
	if c.tokenSource != nil {
		token, tokErr := c.tokenSource(ctx)
		if tokErr != nil {
			cleanup()
			return nil, nil, &tokenSourceError{
				auth: &oauth.ErrAuthRequired{
					ServerName: c.Name,
					Reason:     fmt.Sprintf("token source failed: %v", tokErr),
				},
				cause: tokErr,
			}
		}
		if token == "" {
			cleanup()
			return nil, nil, &oauth.ErrAuthRequired{ServerName: c.Name, Reason: "no stored tokens"}
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		httpReq.Header.Set(mcpSessionIDHeader, sessionID)
	}
	// Last, the headers this one attempt presents — a user-direction receipt
	// that admits exactly the operation the request carries.
	for k, v := range extra {
		httpReq.Header.Set(k, v)
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

	// A 401 means the server rejected our credentials. When a token source is
	// configured that is a re-authorization signal (there is no auto-retry);
	// without one we keep the generic HTTP error text unchanged.
	if resp.StatusCode == http.StatusUnauthorized && c.tokenSource != nil {
		resp.Body.Close()
		cleanup()
		return nil, nil, &oauth.ErrAuthRequired{
			ServerName: c.Name,
			Reason:     "server rejected credentials (HTTP 401)",
		}
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
