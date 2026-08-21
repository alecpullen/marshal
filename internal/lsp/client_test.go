package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

func TestTrimHeaderTrimsWhitespace(t *testing.T) {
	cases := []struct {
		line, key, want string
	}{
		{"Content-Length: 42\r\n", "Content-Length:", "42"},
		{"Content-Length: 42", "Content-Length:", "42"},
		{"X: \r\n", "X:", ""},
		{"X: hello world\r\n", "X:", "hello world"},
	}
	for _, c := range cases {
		got := trimHeader(c.line, c.key)
		if got != c.want {
			t.Errorf("trimHeader(%q, %q) = %q, want %q", c.line, c.key, got, c.want)
		}
	}
}

type pipe struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func newPipe() pipe {
	r, w := io.Pipe()
	return pipe{r: r, w: w}
}

// fakeServer speaks the base protocol: it echoes an initialize result and
// answers a documentSymbol request with one symbol.
func fakeServer(t *testing.T, in io.Reader, out io.Writer) {
	r := bufio.NewReader(in)
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(msg, &req)
		if req.ID == nil {
			continue // notification
		}
		var result string
		switch req.Method {
		case "initialize":
			result = `{"capabilities":{}}`
		case "textDocument/documentSymbol":
			result = `[{"name":"Foo","kind":12,"range":{"start":{"line":3,"character":0},"end":{"line":5,"character":1}}}]`
		default:
			result = `null`
		}
		resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
		_ = writeMessage(out, []byte(resp))
	}
}

// countingServer is like fakeServer but records every parsed Content-Length
// and the total bytes received, so the test can assert no torn frames.
func countingServer(t *testing.T, in io.Reader, out io.Writer, frameCount *int, totalBytes *int64) {
	r := bufio.NewReader(in)
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}
		// Re-parse the Content-Length from the raw stream to verify framing.
		// We already consumed the message; count it.
		*frameCount++
		*totalBytes += int64(len(msg))

		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(msg, &req)
		if req.ID == nil {
			continue // notification
		}
		// Respond so the client's Request call unblocks.
		resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":null}`, req.ID)
		_ = writeMessage(out, []byte(resp))
	}
}

func TestClientConcurrentWrites(t *testing.T) {
	cToS := newPipe()
	sToC := newPipe()

	var frameCount int
	var totalBytes int64
	var serverWg sync.WaitGroup
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		countingServer(t, cToS.r, sToC.w, &frameCount, &totalBytes)
	}()

	c := newClient(cToS.w, sToC.r)
	defer c.Close()

	const numGoroutines = 8
	const iterationsPerGoroutine = 20
	expectedFrames := numGoroutines * iterationsPerGoroutine // each iteration sends 1 message

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var clientWg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		clientWg.Add(1)
		go func(id int) {
			defer clientWg.Done()
			for j := 0; j < iterationsPerGoroutine; j++ {
				// Alternate between Request and Notify to exercise both write paths.
				if (id+j)%2 == 0 {
					_, err := c.Request(ctx, "textDocument/documentSymbol", map[string]any{})
					if err != nil {
						t.Errorf("goroutine %d Request: %v", id, err)
						return
					}
				} else {
					if err := c.Notify("textDocument/didChange", map[string]any{}); err != nil {
						t.Errorf("goroutine %d Notify: %v", id, err)
						return
					}
				}
			}
		}(i)
	}
	clientWg.Wait()

	// Close the client-to-server pipe to signal the counting server to stop.
	cToS.w.Close()
	serverWg.Wait()

	if frameCount != expectedFrames {
		t.Fatalf("expected %d frames, got %d", expectedFrames, frameCount)
	}
	// Sanity: total bytes should be > 0 (each message has a body).
	if totalBytes == 0 {
		t.Fatal("total bytes received is 0, server likely saw no messages")
	}
}

func TestClientInitializeAndRequest(t *testing.T) {
	cToS := newPipe()
	sToC := newPipe()
	go fakeServer(t, cToS.r, sToC.w)

	c := newClient(cToS.w, sToC.r)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///tmp"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	raw, err := c.Request(ctx, "textDocument/documentSymbol", map[string]any{})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		t.Fatalf("empty documentSymbol result: %s", raw)
	}
}

// TestClientCloseIdempotent verifies that calling Close() twice does not
// panic (previously: close of closed channel).
func TestClientCloseIdempotent(t *testing.T) {
	cToS := newPipe()
	sToC := newPipe()
	go fakeServer(t, cToS.r, sToC.w)

	c := newClient(cToS.w, sToC.r)
	c.Close()
	// Second close must not panic.
	c.Close()
}
