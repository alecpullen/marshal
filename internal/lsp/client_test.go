package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"
)

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
