package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// Client is one JSON-RPC connection to one language server.
type Client struct {
	w io.Writer
	r *bufio.Reader

	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage

	writeMu sync.Mutex // serializes writes to c.w (server stdin)

	diagMu sync.Mutex
	diags  map[string][]Diagnostic

	closed chan struct{}
}

func newClient(w io.Writer, r io.Reader) *Client {
	c := &Client{
		w:       w,
		r:       bufio.NewReader(r),
		pending: map[int]chan json.RawMessage{},
		diags:   map[string][]Diagnostic{},
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Client) Close() { close(c.closed) }

// writeMessage serializes a framed message to the server's stdin.
func (c *Client) writeMessage(body []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeMessage(c.w, body)
}

func writeMessage(w io.Writer, body []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	var length int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
		if n, err := strconv.Atoi(trimHeader(line, "Content-Length:")); err == nil {
			length = n
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func trimHeader(line, key string) string {
	if len(line) >= len(key) && line[:len(key)] == key {
		v := line[len(key):]
		return trimSpace(v)
	}
	return ""
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[len(s)-1] == '\r' || s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		if s[0] == ' ' {
			s = s[1:]
			continue
		}
		s = s[:len(s)-1]
	}
	return s
}

func (c *Client) readLoop() {
	for {
		body, err := readMessage(c.r)
		if err != nil {
			return
		}
		var m struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			continue
		}
		if m.ID != nil {
			c.mu.Lock()
			ch := c.pending[*m.ID]
			delete(c.pending, *m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m.Result
			}
			continue
		}
		if m.Method == "textDocument/publishDiagnostics" {
			var p struct {
				URI         string       `json:"uri"`
				Diagnostics []Diagnostic `json:"diagnostics"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				c.diagMu.Lock()
				c.diags[p.URI] = p.Diagnostics
				c.diagMu.Unlock()
			}
		}
	}
}

// Request sends a request and waits for its response.
func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if err := c.writeMessage(body); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, fmt.Errorf("lsp client closed")
	case res := <-ch:
		return res, nil
	}
}

// Notify sends a notification (no response).
func (c *Client) Notify(method string, params any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	return c.writeMessage(body)
}

// Initialize performs the handshake.
func (c *Client) Initialize(ctx context.Context, rootURI string) error {
	_, err := c.Request(ctx, "initialize", map[string]any{
		"processId": nil,
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"documentSymbol": map[string]any{},
				"definition":     map[string]any{},
				"references":     map[string]any{},
				"hover":          map[string]any{},
			},
		},
	})
	if err != nil {
		return err
	}
	return c.Notify("initialized", map[string]any{})
}

// Diagnostics returns the last-published diagnostics for a URI.
func (c *Client) Diagnostics(uri string) []Diagnostic {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	return c.diags[uri]
}
