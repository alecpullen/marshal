package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
)

// startTestAgent serves a server on the agent end of a pipe and returns the
// client's transport. setup registers the handlers before Serve starts, as
// the real host does. The connection ends with the test.
func startTestAgent(t *testing.T, setup func(*Server)) rpc.Transport {
	t.Helper()

	client, agent := rpc.Pipe()
	srv := NewServer(agent, agent, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	// The agent's end sets its own cap; neither end inherits the default (P3 R2).
	srv.MaxLineBytes = rpc.MaxFrameBytes
	if setup != nil {
		setup(srv)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		_ = agent.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})
	return client
}

func TestClientSeesServerAcpwireErrors(t *testing.T) {
	client := startTestAgent(t, func(srv *Server) {
		srv.Handle("turn", func(ctx context.Context, params json.RawMessage) (any, error) {
			return nil, &acpwire.Error{Code: acpwire.CodeBusy, Message: "A turn is running."}
		})
	})
	conn := rpc.NewConn(client, rpc.Handlers{})
	defer conn.Close()

	var result json.RawMessage
	err := conn.Call(context.Background(), "turn", nil, &result)
	if !acpclient.IsCode(err, acpwire.CodeBusy) {
		t.Fatalf("err = %v, want the busy code", err)
	}
	if err.Error() != "A turn is running." {
		t.Fatalf("err.Error() = %q, want %q", err.Error(), "A turn is running.")
	}
}

func TestCancelEndsACancellableHandler(t *testing.T) {
	ended := make(chan struct{})
	client := startTestAgent(t, func(srv *Server) {
		srv.Handle("slow", func(ctx context.Context, params json.RawMessage) (any, error) {
			cctx, cancel := cancellable(ctx)
			defer cancel()
			<-cctx.Done()
			close(ended)
			return nil, cctx.Err()
		})
	})
	conn := rpc.NewConn(client, rpc.Handlers{})
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() { callDone <- conn.Call(ctx, "slow", nil, nil) }()

	// Let the handler opt in before the cancel travels.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Call = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled call did not return")
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the cancellable handler did not end")
	}
}

func TestCancelLeavesOtherHandlersRunning(t *testing.T) {
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	client := startTestAgent(t, func(srv *Server) {
		// A handler that does not opt in sees no cancellation, as /mcp auth's
		// sign-in, which outlives its request, must not.
		srv.Handle("signin", func(ctx context.Context, params json.RawMessage) (any, error) {
			<-release
			close(handlerDone)
			return map[string]any{"ok": true}, nil
		})
		srv.Handle("after", func(ctx context.Context, params json.RawMessage) (any, error) {
			return map[string]any{"ok": true}, nil
		})
	})
	conn := rpc.NewConn(client, rpc.Handlers{})
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() { callDone <- conn.Call(ctx, "signin", nil, nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Call = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled call did not return")
	}

	select {
	case <-handlerDone:
		t.Fatal("a handler that did not opt in saw the cancel")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler did not finish after release")
	}

	// The late response is dropped, and the connection still carries calls.
	var res map[string]any
	if err := conn.Call(context.Background(), "after", nil, &res); err != nil {
		t.Fatalf("call after the dropped response: %v", err)
	}
}

func TestACancelBeforeTheHandlerOptsInIsKept(t *testing.T) {
	sawCancel := make(chan bool, 1)
	client := startTestAgent(t, func(srv *Server) {
		srv.Handle("slow", func(ctx context.Context, params json.RawMessage) (any, error) {
			// Opt in only after the cancel has already been sent.
			time.Sleep(100 * time.Millisecond)
			cctx, cancel := cancellable(ctx)
			defer cancel()
			select {
			case <-cctx.Done():
				sawCancel <- true
			default:
				sawCancel <- false
			}
			return nil, ctx.Err()
		})
	})
	conn := rpc.NewConn(client, rpc.Handlers{})
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = conn.Call(ctx, "slow", nil, nil) }()
	cancel()

	select {
	case got := <-sawCancel:
		if !got {
			t.Fatal("a cancel sent before the handler opted in was lost")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never opted in")
	}
}

func TestProgressPrecedesTheResponse(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	client := startTestAgent(t, func(srv *Server) {
		srv.Handle("show", func(ctx context.Context, params json.RawMessage) (any, error) {
			if err := reportProgress(ctx, map[string]any{"url": "https://example.test/auth"}); err != nil {
				return nil, err
			}
			if err := reportProgress(ctx, map[string]any{"url": "https://example.test/done"}); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		})
	})
	conn := rpc.NewConn(client, rpc.Handlers{})
	defer conn.Close()

	var res map[string]any
	err := conn.CallWithProgress(context.Background(), "show", nil, &res, func(value json.RawMessage) {
		var v struct {
			URL string `json:"url"`
		}
		if uerr := json.Unmarshal(value, &v); uerr != nil {
			t.Errorf("progress value: %v", uerr)
			return
		}
		mu.Lock()
		seen = append(seen, v.URL)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("CallWithProgress: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"https://example.test/auth", "https://example.test/done"}
	if len(seen) != len(want) {
		t.Fatalf("progress values = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("progress values = %v, want %v", seen, want)
		}
	}
}

func TestThePipeCarriesLargeFramesToTheServerAndBack(t *testing.T) {
	const size = 3 << 20
	client := startTestAgent(t, func(srv *Server) {
		srv.Handle("echo", func(ctx context.Context, params json.RawMessage) (any, error) {
			var p struct {
				Blob string `json:"blob"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			if len(p.Blob) != size {
				return nil, fmt.Errorf("the handler got %d bytes, want %d", len(p.Blob), size)
			}
			return map[string]any{"blob": p.Blob}, nil
		})
	})
	conn := rpc.NewConn(client, rpc.Handlers{})
	defer conn.Close()

	var res struct {
		Blob string `json:"blob"`
	}
	params := map[string]any{"blob": strings.Repeat("x", size)}
	if err := conn.Call(context.Background(), "echo", params, &res); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Blob) != size {
		t.Fatalf("the client got %d bytes back, want %d", len(res.Blob), size)
	}
}

func TestServerRequestsReachTheClient(t *testing.T) {
	client := startTestAgent(t, func(srv *Server) {
		srv.Handle("ask", func(ctx context.Context, params json.RawMessage) (any, error) {
			var res struct {
				N int `json:"n"`
			}
			if err := srv.Request(ctx, "client/choose", map[string]any{"n": 7}, &res); err != nil {
				return nil, err
			}
			return map[string]any{"n": res.N}, nil
		})
	})
	conn := rpc.NewConn(client, rpc.Handlers{
		Request: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != "client/choose" {
				return nil, fmt.Errorf("unexpected method %q", method)
			}
			return map[string]any{"n": 42}, nil
		},
	})
	defer conn.Close()

	var res struct {
		N int `json:"n"`
	}
	if err := conn.Call(context.Background(), "ask", nil, &res); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.N != 42 {
		t.Fatalf("the agent saw n = %d, want 42", res.N)
	}
}
