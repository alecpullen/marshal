package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"

	"marshal/internal/sandbox/envutil"
)

// ErrClientClosed is returned by Call when the client has been Close()d.
var ErrClientClosed = errors.New("mcp: client closed")

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithClientLogger sets the logger on a Client. When nil (or unset), the
// client uses slog.Default().
func WithClientLogger(l *slog.Logger) ClientOption {
	return func(c *Client) { c.Logger = l }
}

type Client struct {
	Name    string
	Command string
	Args    []string
	Env     []string

	Logger *slog.Logger // nil → slog.Default()

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	wg     sync.WaitGroup

	mu      sync.Mutex
	nextID  int64
	pending map[json.Number]chan<- Response
	err     error
}

func NewClient(name, command string, args, env []string, opts ...ClientOption) *Client {
	c := &Client{
		Name:    name,
		Command: command,
		Args:    args,
		Env:     env,
		pending: make(map[json.Number]chan<- Response),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// log returns the client's logger, defaulting to slog.Default() when the
// Logger field is nil.
func (c *Client) log() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}
	return c.Logger
}

// buildChildEnv returns a safe environment for the MCP child process.
// It starts from the envutil allowlist (safe defaults, no parent secrets),
// then layers user-configured env entries, rejecting any that are dangerous
// or secret-bearing.
func (c *Client) buildChildEnv() []string {
	env := envutil.AllowList(os.Environ())
	for _, kv := range c.Env {
		k := envutil.EnvKey(kv)
		if k == "" {
			continue
		}
		if envutil.IsDangerousKey(k) || envutil.IsSecretKey(k) {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func (c *Client) Start(ctx context.Context) error {
	// Use exec.Command (not exec.CommandContext) so the child process is
	// NOT killed when ctx is cancelled. The ctx is used only for the
	// initialize handshake below (which should time out on cancel); the
	// child process lifecycle is managed by Close(). This decouples the
	// MCP server's lifetime from the caller's context (TOOLS-MOD-F5).
	c.cmd = exec.Command(c.Command, c.Args...)
	c.cmd.Env = c.buildChildEnv()

	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return err
	}

	c.stdin = stdin
	c.stdout = stdout

	if err := c.cmd.Start(); err != nil {
		return err
	}

	c.wg.Add(1)
	go c.readLoop()

	// Initialize Handshake
	var initRes InitializeResult
	initParams := InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      Implementation{Name: "marshal", Version: "1.0.0"},
	}
	if err := c.Call(ctx, "initialize", initParams, &initRes); err != nil {
		c.Close()
		return fmt.Errorf("initialize handshake: %w", err)
	}

	// Send initialized notification
	notification := Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	_ = c.write(notification)

	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.err == nil {
		c.err = ErrClientClosed
	}
	c.mu.Unlock()

	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.stdout != nil {
		_ = c.stdout.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	c.wg.Wait()
	c.log().Info("mcp client closed")
	return nil
}

func (c *Client) Call(ctx context.Context, method string, params interface{}, result interface{}) error {
	n := atomic.AddInt64(&c.nextID, 1)
	id := json.Number(strconv.FormatInt(n, 10))
	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	ch := make(chan Response, 1)
	c.mu.Lock()
	if errors.Is(c.err, ErrClientClosed) {
		c.mu.Unlock()
		return ErrClientClosed
	}
	if c.err != nil {
		c.mu.Unlock()
		return fmt.Errorf("mcp: call %s: %w", method, c.err)
	}
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(req); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-ch:
		if res.Error != nil {
			return fmt.Errorf("MCP error (%d): %s", res.Error.Code, res.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(res.Result, result)
		}
		return nil
	}
}

func (c *Client) write(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	_, err = fmt.Fprintf(c.stdin, "%s\n", data)
	return err
}

func (c *Client) readLoop() {
	defer c.wg.Done()
	scanner := bufio.NewScanner(c.stdout)
	// MCP tool results routinely exceed bufio's 64KB default token limit
	// (file contents, DOM dumps). Without this, one oversized line ends the
	// scan with ErrTooLong, poisons c.err, and kills the client permanently.
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		var res Response
		if err := json.Unmarshal(scanner.Bytes(), &res); err != nil {
			continue
		}
		if res.ID == "" {
			continue
		}
		c.log().Debug("mcp response", "id", res.ID)
		// ID is now json.Number — use it directly as the pending key.
		c.mu.Lock()
		ch, ok := c.pending[res.ID]
		c.mu.Unlock()

		if ok {
			ch <- res
		}
	}
	if err := scanner.Err(); err != nil {
		c.log().Warn("mcp readLoop ended", "err", err)
	} else {
		c.log().Info("mcp readLoop ended")
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = scanner.Err()
		if c.err == nil {
			c.err = io.EOF
		}
	}
	// Snapshot pending entries under the lock; send outside the lock so a
	// full per-id channel cannot block the read loop (F-CON-53). The
	// per-id channel buffer is 1; if it's already full, the response
	// was delivered and there's no one to notify.
	errMsg := ""
	if c.err != nil {
		errMsg = c.err.Error()
	}
	type pendingEntry struct {
		id json.Number
		ch chan<- Response
	}
	entries := make([]pendingEntry, 0, len(c.pending))
	for id, ch := range c.pending {
		entries = append(entries, pendingEntry{id: id, ch: ch})
	}
	c.pending = make(map[json.Number]chan<- Response)
	c.mu.Unlock()

	for _, e := range entries {
		select {
		case e.ch <- Response{Error: &Error{Message: errMsg}}:
		default:
		}
	}
}
