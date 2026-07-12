package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app"
)

func TestRunInitializeCapabilities(t *testing.T) {
	t.Run("basic capabilities", func(t *testing.T) {
		in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n")
		out := &bytes.Buffer{}

		cfg := runConfig{
			startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
				return &app.Runtime{}, nil
			},
			closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
			shutdown:     0,
		}

		if err := runWithConfig(context.Background(), in, out, io.Discard, cfg); err != nil {
			t.Fatalf("runWithConfig() = %v", err)
		}

		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		if !scan.Scan() {
			t.Fatalf("no response; output=%q", out.String())
		}
		var resp Response
		if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("response error: %+v", resp.Error)
		}

		result, ok := resp.Result.(map[string]any)
		if !ok {
			t.Fatalf("result type = %T", resp.Result)
		}

		// protocolVersion
		if result["protocolVersion"] != float64(1) {
			t.Fatalf("protocolVersion = %v, want 1", result["protocolVersion"])
		}

		// agentCapabilities
		caps, ok := result["agentCapabilities"].(map[string]any)
		if !ok {
			t.Fatalf("agentCapabilities missing or wrong type: %T", result["agentCapabilities"])
		}
		if caps["loadSession"] != true {
			t.Fatalf("loadSession = %v, want true", caps["loadSession"])
		}

		// sessionCapabilities.close is an object (not null)
		sessionCaps, ok := caps["sessionCapabilities"].(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities missing or wrong type: %T", caps["sessionCapabilities"])
		}
		closeCap, ok := sessionCaps["close"]
		if !ok {
			t.Fatalf("sessionCapabilities.close missing")
		}
		closeObj, ok := closeCap.(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities.close is not an object: %T", closeCap)
		}
		if len(closeObj) != 0 {
			t.Fatalf("sessionCapabilities.close = %v, want empty object", closeObj)
		}

		// sessionCapabilities.list is an empty object.
		listCap, ok := sessionCaps["list"]
		if !ok {
			t.Fatalf("sessionCapabilities.list missing")
		}
		listObj, ok := listCap.(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities.list is not an object: %T", listCap)
		}
		if len(listObj) != 0 {
			t.Fatalf("sessionCapabilities.list = %v, want empty object", listObj)
		}

		// Must NOT have image/audio/embeddedContext/resume/delete/additionalDirectories/mcp
		forbidden := []string{"image", "audio", "embeddedContext", "resume", "delete", "additionalDirectories", "mcp"}
		for _, key := range forbidden {
			if _, exists := caps[key]; exists {
				t.Fatalf("unexpected capability: %s", key)
			}
		}

		// agentInfo
		info, ok := result["agentInfo"].(map[string]any)
		if !ok {
			t.Fatalf("agentInfo missing or wrong type: %T", result["agentInfo"])
		}
		if info["name"] != "marshal" {
			t.Fatalf("agentInfo.name = %v, want marshal", info["name"])
		}
		if info["title"] != "Marshal" {
			t.Fatalf("agentInfo.title = %v, want Marshal", info["title"])
		}

		// authMethods: empty array (not null)
		authMethods, ok := result["authMethods"].([]any)
		if !ok {
			t.Fatalf("authMethods wrong type: %T (want []any)", result["authMethods"])
		}
		if len(authMethods) != 0 {
			t.Fatalf("authMethods should be empty, got %v", authMethods)
		}
	})

	t.Run("missing version rejected", func(t *testing.T) {
		in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
		out := &bytes.Buffer{}

		cfg := runConfig{
			startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
				return &app.Runtime{}, nil
			},
			closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
			shutdown:     0,
		}

		if err := runWithConfig(context.Background(), in, out, io.Discard, cfg); err != nil {
			t.Fatalf("runWithConfig() = %v", err)
		}

		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		if !scan.Scan() {
			t.Fatalf("no response; output=%q", out.String())
		}
		var resp Response
		if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.Error == nil {
			t.Fatal("expected error for missing protocolVersion")
		}
		if resp.Error.Code != invalidParams {
			t.Fatalf("error code = %d, want %d", resp.Error.Code, invalidParams)
		}
	})

	t.Run("version negotiation", func(t *testing.T) {
		in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}` + "\n")
		out := &bytes.Buffer{}

		cfg := runConfig{
			startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
				return &app.Runtime{}, nil
			},
			closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
			shutdown:     0,
		}

		if err := runWithConfig(context.Background(), in, out, io.Discard, cfg); err != nil {
			t.Fatalf("runWithConfig() = %v", err)
		}

		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		if !scan.Scan() {
			t.Fatalf("no response; output=%q", out.String())
		}
		var resp Response
		if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("response error: %+v", resp.Error)
		}

		result, ok := resp.Result.(map[string]any)
		if !ok {
			t.Fatalf("result type = %T", resp.Result)
		}
		if result["protocolVersion"] != float64(1) {
			t.Fatalf("protocolVersion = %v, want 1", result["protocolVersion"])
		}
	})
}

func TestRunEOFClosesAllSessionsExactlyOnce(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}

	var (
		mu         sync.Mutex
		closeCalls []string
	)
	var idSeq atomic.Int64

	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			id := idSeq.Add(1)
			return &app.Runtime{SessionID: "sess_" + strconv.FormatInt(id, 10)}, nil
		},
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error {
			mu.Lock()
			closeCalls = append(closeCalls, rt.SessionID)
			mu.Unlock()
			return nil
		},
		shutdown: time.Second,
	}

	ctx := context.Background()
	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithConfig(ctx, pr, out, io.Discard, cfg)
	}()

	absCwd := t.TempDir()

	// Send two session/new requests
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/new",
		"params": map[string]any{"cwd": absCwd, "mcpServers": []any{}},
	})
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/new",
		"params": map[string]any{"cwd": absCwd, "mcpServers": []any{}},
	})

	// Wait for two session responses
	pollUntil(t, 2*time.Second, func() bool {
		return strings.Count(out.String(), `"sessionId"`) >= 2
	})

	// Close writer to produce EOF
	pw.Close()

	select {
	case err := <-runErr:
		if err != nil {
			t.Logf("runWithConfig returned: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithConfig did not return after pipe close")
	}

	// Verify close calls
	mu.Lock()
	defer mu.Unlock()

	if len(closeCalls) != 2 {
		t.Fatalf("expected 2 close calls, got %d: %v", len(closeCalls), closeCalls)
	}

	counts := map[string]int{}
	for _, id := range closeCalls {
		counts[id]++
	}
	for id, count := range counts {
		if count != 1 {
			t.Fatalf("session %s closed %d times, want 1", id, count)
		}
	}
}

func TestRunContextCancelClosesSessions(t *testing.T) {
	pr, pw := io.Pipe()
	out := &lockedBuffer{}

	var (
		mu         sync.Mutex
		closeCalls int
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return &app.Runtime{SessionID: "sess_1"}, nil
		},
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error {
			mu.Lock()
			closeCalls++
			mu.Unlock()
			return nil
		},
		shutdown: time.Second,
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithConfig(ctx, pr, out, io.Discard, cfg)
	}()

	absCwd := t.TempDir()

	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/new",
		"params": map[string]any{"cwd": absCwd, "mcpServers": []any{}},
	})

	// Wait for session response
	pollUntil(t, 2*time.Second, func() bool {
		return strings.Contains(out.String(), `"sessionId"`)
	})

	// Cancel context
	cancel()

	select {
	case err := <-runErr:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("runWithConfig error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithConfig did not return after context cancellation")
	}

	mu.Lock()
	if closeCalls != 1 {
		t.Fatalf("closeCalls = %d, want 1", closeCalls)
	}
	mu.Unlock()
}

func TestRunReturnsJoinedServeAndCleanupErrors(t *testing.T) {
	writeSentinel := errors.New("write failure")
	closeSentinel := errors.New("close failure")

	pr, pw := io.Pipe()
	wf := &writeFailer{err: writeSentinel}

	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return &app.Runtime{SessionID: "sess_1"}, nil
		},
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error {
			return closeSentinel
		},
		shutdown: time.Second,
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- runWithConfig(context.Background(), pr, wf, io.Discard, cfg)
	}()

	absCwd := t.TempDir()

	// Send session/new — the response write will fail, causing Serve to exit
	// with writeSentinel, then CloseAll will return closeSentinel.
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/new",
		"params": map[string]any{"cwd": absCwd, "mcpServers": []any{}},
	})

	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("expected joined error, got nil")
		}
		if !errors.Is(err, writeSentinel) {
			t.Fatalf("err does not contain writeSentinel: %v", err)
		}
		if !errors.Is(err, closeSentinel) {
			t.Fatalf("err does not contain closeSentinel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithConfig did not return after write failure")
	}

	_ = pw.Close()
}

func TestRunRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, bytes.NewBuffer(nil), &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
}
