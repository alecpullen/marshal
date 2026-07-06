package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

type Client struct {
	Name    string
	Command string
	Args    []string
	Env     []string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	wg     sync.WaitGroup

	mu      sync.Mutex
	nextID  int64
	pending map[interface{}]chan<- Response
	err     error
}

func NewClient(name, command string, args, env []string) *Client {
	return &Client{
		Name:    name,
		Command: command,
		Args:    args,
		Env:     env,
		pending: make(map[interface{}]chan<- Response),
	}
}

func (c *Client) Start(ctx context.Context) error {
	c.cmd = exec.CommandContext(ctx, c.Command, c.Args...)
	c.cmd.Env = append(c.cmd.Env, c.Env...)

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
		c.err = fmt.Errorf("client closed")
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
	return nil
}

func (c *Client) Call(ctx context.Context, method string, params interface{}, result interface{}) error {
	id := atomic.AddInt64(&c.nextID, 1)
	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	ch := make(chan Response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
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
	for scanner.Scan() {
		var res Response
		if err := json.Unmarshal(scanner.Bytes(), &res); err != nil {
			continue
		}
		if res.ID == nil {
			continue
		}
		// Handle JSON numeric type float64 vs int64
		var key interface{}
		switch v := res.ID.(type) {
		case float64:
			key = int64(v)
		default:
			key = v
		}

		c.mu.Lock()
		ch, ok := c.pending[key]
		c.mu.Unlock()

		if ok {
			ch <- res
		}
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = scanner.Err()
		if c.err == nil {
			c.err = io.EOF
		}
	}
	// Fail all pending
	for id, ch := range c.pending {
		ch <- Response{Error: &Error{Message: c.err.Error()}}
		delete(c.pending, id)
	}
	c.mu.Unlock()
}
