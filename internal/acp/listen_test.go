package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app"
)

// dialAndInitialize opens a connection, sends initialize, and returns the
// decoded result.
func dialAndInitialize(t *testing.T, addr string) map[string]any {
	t.Helper()
	conn, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var frame struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	return frame.Result
}

func testRunConfig(t *testing.T) runConfig {
	t.Helper()
	return runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return nil, fmt.Errorf("no runtime in tests")
		},
		shutdown: time.Second,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestListenAndServeAcceptsSuccessiveConnections(t *testing.T) {
	// Unix socket paths are limited to ~104 bytes on macOS, so use a short
	// directory rather than t.TempDir() (whose path is far longer).
	dir, err := os.MkdirTemp("", "acp")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)
	addr := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- listenAndServeWithConfig(ctx, ln, testRunConfig(t))
	}()

	// First connection: initialize, then hang up.
	if got := dialAndInitialize(t, addr); got["protocolVersion"] == nil {
		t.Fatalf("first connection: no protocolVersion in %v", got)
	}

	// The host must survive the hangup and accept a second connection.
	if got := dialAndInitialize(t, addr); got["protocolVersion"] == nil {
		t.Fatalf("second connection: no protocolVersion in %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("listenAndServeWithConfig did not return after cancel")
	}
}
