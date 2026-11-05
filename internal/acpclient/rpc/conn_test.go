package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// fake is one driven connection: the client's Conn and the agent's byte
// stream, which the test reads with a scanner and writes raw frames to.
type fake struct {
	conn  *Conn
	agent io.ReadWriteCloser
	sc    *bufio.Scanner
}

// start creates a piped connection with h and cleans both ends up.
func start(t *testing.T, h Handlers) *fake {
	t.Helper()
	client, agent := Pipe()
	conn := NewConn(client, h)
	t.Cleanup(func() {
		conn.Close()
		agent.Close()
	})
	return &fake{conn: conn, agent: agent, sc: bufio.NewScanner(agent)}
}

// send writes one raw frame to the agent's stream.
func (f *fake) send(t *testing.T, frame string) {
	t.Helper()
	if err := writeLine(f.agent, []byte(frame)); err != nil {
		t.Fatalf("write agent frame: %v", err)
	}
}

// recv reads one frame the client sent and decodes it.
func (f *fake) recv(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	if !f.sc.Scan() {
		t.Fatalf("read client frame: %v", f.sc.Err())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(f.sc.Bytes(), &m); err != nil {
		t.Fatalf("decode client frame %q: %v", f.sc.Bytes(), err)
	}
	return m
}

// rawString returns a frame value as a plain string: a JSON string's
// contents, or a non-string value's compact JSON (a numeric request ID).
func rawString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("frame value is missing")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return compactID(raw)
}

// TestCallRoundTrip checks that two calls get IDs 1 and 2, that a nil params
// sends no params, and that each result decodes.
func TestCallRoundTrip(t *testing.T) {
	f := start(t, Handlers{})

	var first struct {
		Name string `json:"name"`
	}
	errCh := make(chan error, 1)
	go func() { errCh <- f.conn.Call(context.Background(), "initialize", nil, &first) }()

	req := f.recv(t)
	if got := rawString(t, req["jsonrpc"]); got != "2.0" {
		t.Fatalf("jsonrpc = %q, want 2.0", got)
	}
	if got := rawString(t, req["id"]); got != "1" {
		t.Fatalf("first call id = %q, want 1", got)
	}
	if got := rawString(t, req["method"]); got != "initialize" {
		t.Fatalf("first call method = %q, want initialize", got)
	}
	if _, ok := req["params"]; ok {
		t.Fatalf("first call sent params %s, want none", req["params"])
	}
	f.send(t, `{"jsonrpc":"2.0","id":1,"result":{"name":"alpha"}}`)
	if err := <-errCh; err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first.Name != "alpha" {
		t.Fatalf("first result = %+v, want name alpha", first)
	}

	var second struct {
		Count int `json:"count"`
	}
	go func() {
		errCh <- f.conn.Call(context.Background(), "session/new", map[string]any{"cwd": "/tmp"}, &second)
	}()

	req = f.recv(t)
	if got := rawString(t, req["id"]); got != "2" {
		t.Fatalf("second call id = %q, want 2", got)
	}
	if _, ok := req["params"]; !ok {
		t.Fatal("second call sent no params, want some")
	}
	f.send(t, `{"jsonrpc":"2.0","id":2,"result":{"count":7}}`)
	if err := <-errCh; err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second.Count != 7 {
		t.Fatalf("second result = %+v, want count 7", second)
	}
}

// TestCallReturnsAcpwireErrors checks the error mapping: a -32000 whose data
// is a coded *acpwire.Error becomes that error, and any other response error
// becomes an *Error.
func TestCallReturnsAcpwireErrors(t *testing.T) {
	call := func(t *testing.T, frame string) error {
		t.Helper()
		f := start(t, Handlers{})
		errCh := make(chan error, 1)
		go func() { errCh <- f.conn.Call(context.Background(), "x", nil, nil) }()
		if req := f.recv(t); rawString(t, req["id"]) != "1" {
			t.Fatalf("call id = %q, want 1", req["id"])
		}
		f.send(t, frame)
		return <-errCh
	}

	t.Run("busy", func(t *testing.T) {
		err := call(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"server error: A turn is running.","data":{"code":"busy","message":"A turn is running."}}}`)
		if !acpclient.IsCode(err, acpwire.CodeBusy) {
			t.Fatalf("err = %v, want an acpwire busy error", err)
		}
		if err.Error() != "A turn is running." {
			t.Fatalf("err.Error() = %q, want %q", err.Error(), "A turn is running.")
		}
	})

	t.Run("notice", func(t *testing.T) {
		err := call(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"server error: failed","data":{"code":"provider_error","message":"failed","data":{"notice":{"category":"provider","severity":"error","message":"Log in to continue."}}}}}`)
		notice, ok := acpclient.NoticeOf(err)
		if !ok {
			t.Fatalf("NoticeOf(%v) found no notice", err)
		}
		if notice.Message != "Log in to continue." {
			t.Fatalf("notice message = %q, want %q", notice.Message, "Log in to continue.")
		}
	})

	t.Run("plain", func(t *testing.T) {
		err := call(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`)
		var rerr *Error
		if !errors.As(err, &rerr) {
			t.Fatalf("err = %T %v, want *Error", err, err)
		}
		if rerr.Code != -32602 || rerr.Message != "Invalid params" {
			t.Fatalf("*Error = %+v, want code -32602 and message %q", rerr, "Invalid params")
		}
	})
}

// TestCallSendsCancelWhenTheContextEnds checks that a cancelled call returns
// context.Canceled, sends $/cancel_request, drops the late response, and
// leaves the connection usable.
func TestCallSendsCancelWhenTheContextEnds(t *testing.T) {
	f := start(t, Handlers{})

	// Read the client's frames from a goroutine so a blocked write never
	// stalls the call's return.
	frames := make(chan map[string]json.RawMessage, 8)
	go func() {
		for f.sc.Scan() {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(f.sc.Bytes(), &m); err != nil {
				continue
			}
			frames <- m
		}
		close(frames)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- f.conn.Call(ctx, "initialize", nil, nil) }()

	select {
	case err := <-errCh:
		t.Fatalf("call returned %v before the context ended", err)
	default:
	}

	// The request frame arrives first.
	var req map[string]json.RawMessage
	select {
	case req = <-frames:
	case <-time.After(time.Second):
		t.Fatal("no request frame within a second")
	}
	if got := rawString(t, req["id"]); got != "1" {
		t.Fatalf("call id = %q, want 1", got)
	}

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled call returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled call did not return within a second")
	}

	var cancelFrame map[string]json.RawMessage
	select {
	case cancelFrame = <-frames:
	case <-time.After(time.Second):
		t.Fatal("no cancel frame within a second")
	}
	if got := rawString(t, cancelFrame["method"]); got != acpwire.MethodCancelRequest {
		t.Fatalf("cancel method = %q, want %q", got, acpwire.MethodCancelRequest)
	}
	var params acpwire.CancelRequestParams
	if err := json.Unmarshal(cancelFrame["params"], &params); err != nil {
		t.Fatalf("decode cancel params: %v", err)
	}
	if got := rawString(t, params.ID); got != "1" {
		t.Fatalf("cancel id = %q, want 1", got)
	}

	// A late response to the cancelled id is dropped.
	f.send(t, `{"jsonrpc":"2.0","id":1,"result":{"stale":true}}`)

	// The next call gets id 2 and succeeds.
	var next struct {
		OK bool `json:"ok"`
	}
	errCh = make(chan error, 1)
	go func() { errCh <- f.conn.Call(context.Background(), "next", nil, &next) }()

	var req2 map[string]json.RawMessage
	select {
	case req2 = <-frames:
	case <-time.After(time.Second):
		t.Fatal("no second request frame within a second")
	}
	if got := rawString(t, req2["id"]); got != "2" {
		t.Fatalf("second call id = %q, want 2", got)
	}
	f.send(t, `{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`)
	if err := <-errCh; err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !next.OK {
		t.Fatalf("second result = %+v, want ok true", next)
	}
}

// TestProgressArrivesBeforeTheResult checks that each matching $/progress
// value reaches the function, in order, before the call returns, and that a
// value for another id is dropped.
func TestProgressArrivesBeforeTheResult(t *testing.T) {
	f := start(t, Handlers{})

	var got []string
	var result struct {
		Done bool `json:"done"`
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- f.conn.CallWithProgress(context.Background(), "run", nil, &result, func(value json.RawMessage) {
			got = append(got, rawString(t, value))
		})
	}()

	if req := f.recv(t); rawString(t, req["id"]) != "1" {
		t.Fatalf("call id = %q, want 1", req["id"])
	}
	f.send(t, `{"jsonrpc":"2.0","method":"$/progress","params":{"id":1,"value":"one"}}`)
	f.send(t, `{"jsonrpc":"2.0","method":"$/progress","params":{"id":1,"value":"two"}}`)
	f.send(t, `{"jsonrpc":"2.0","method":"$/progress","params":{"id":99,"value":"other"}}`)
	f.send(t, `{"jsonrpc":"2.0","id":1,"result":{"done":true}}`)

	if err := <-errCh; err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("progress values = %v, want [one two]", got)
	}
	if !result.Done {
		t.Fatalf("result = %+v, want done true", result)
	}
}

// TestNotificationsBeforeAResponseAreHandledFirst checks that a notification
// is handled before the response that follows it.
func TestNotificationsBeforeAResponseAreHandledFirst(t *testing.T) {
	var notifies []string
	f := start(t, Handlers{
		Notify: func(method string, params json.RawMessage) {
			notifies = append(notifies, method)
		},
	})

	errCh := make(chan error, 1)
	go func() { errCh <- f.conn.Call(context.Background(), "initialize", nil, nil) }()

	if req := f.recv(t); rawString(t, req["id"]) != "1" {
		t.Fatalf("call id = %q, want 1", req["id"])
	}
	f.send(t, `{"jsonrpc":"2.0","method":"session/update","params":{"x":1}}`)
	f.send(t, `{"jsonrpc":"2.0","id":1,"result":{}}`)

	if err := <-errCh; err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(notifies) != 1 || notifies[0] != "session/update" {
		t.Fatalf("notifications = %v, want [session/update] before the call returned", notifies)
	}
}

// TestAgentRequestsAreAnswered checks that the client answers an agent request
// with its raw ID, whether the handler returns a result, an *acpwire.Error, or
// is absent, and that a blocked handler does not stall a call.
func TestAgentRequestsAreAnswered(t *testing.T) {
	const agentID = "marshal-out-1"
	// agentRequest is a request whose ID is the string agentID.
	agentRequest := `{"jsonrpc":"2.0","id":"` + agentID + `","method":"session/request_permission","params":{}}`

	t.Run("result", func(t *testing.T) {
		f := start(t, Handlers{
			Request: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
				return map[string]any{"outcome": "allow"}, nil
			},
		})
		f.send(t, agentRequest)
		frame := f.recv(t)
		if got := rawString(t, frame["id"]); got != agentID {
			t.Fatalf("answer id = %q, want %q", got, agentID)
		}
		var result struct {
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal(frame["result"], &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if result.Outcome != "allow" {
			t.Fatalf("result = %+v, want outcome allow", result)
		}
	})

	t.Run("acpwire error", func(t *testing.T) {
		f := start(t, Handlers{
			Request: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
				return nil, &acpwire.Error{
					Code:    acpwire.CodeProviderError,
					Message: "boom",
					Data:    json.RawMessage(`{"notice":{"category":"provider","severity":"error","message":"Log in."}}`),
				}
			},
		})
		f.send(t, agentRequest)
		frame := f.recv(t)
		if got := rawString(t, frame["id"]); got != agentID {
			t.Fatalf("answer id = %q, want %q", got, agentID)
		}
		var wire struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(frame["error"], &wire); err != nil {
			t.Fatalf("decode answer error: %v", err)
		}
		if wire.Code != -32000 {
			t.Fatalf("answer error code = %d, want -32000", wire.Code)
		}
		if wire.Message != "boom" {
			t.Fatalf("answer error message = %q, want %q", wire.Message, "boom")
		}
		var data acpwire.Error
		if err := json.Unmarshal(wire.Data, &data); err != nil {
			t.Fatalf("decode answer error data: %v", err)
		}
		if data.Code != acpwire.CodeProviderError || data.Message != "boom" {
			t.Fatalf("answer error data = %+v, want a provider_error", data)
		}
	})

	t.Run("no handler", func(t *testing.T) {
		f := start(t, Handlers{})
		f.send(t, agentRequest)
		frame := f.recv(t)
		if got := rawString(t, frame["id"]); got != agentID {
			t.Fatalf("answer id = %q, want %q", got, agentID)
		}
		var wire struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(frame["error"], &wire); err != nil {
			t.Fatalf("decode answer error: %v", err)
		}
		if wire.Code != -32601 {
			t.Fatalf("answer error code = %d, want -32601", wire.Code)
		}
		if wire.Message != "method not found: session/request_permission" {
			t.Fatalf("answer error message = %q, want %q", wire.Message, "method not found: session/request_permission")
		}
	})

	t.Run("blocked handler", func(t *testing.T) {
		release := make(chan struct{})
		blocked := make(chan struct{})
		f := start(t, Handlers{
			Request: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
				close(blocked)
				<-release
				return nil, nil
			},
		})
		f.send(t, agentRequest)
		select {
		case <-blocked:
		case <-time.After(time.Second):
			t.Fatal("handler never started")
		}

		// A call made while the handler blocks must still complete.
		var result struct {
			OK bool `json:"ok"`
		}
		errCh := make(chan error, 1)
		go func() { errCh <- f.conn.Call(context.Background(), "ping", nil, &result) }()

		if req := f.recv(t); rawString(t, req["id"]) != "1" {
			t.Fatalf("ping id = %q, want 1", req["id"])
		}
		f.send(t, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("ping: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("call did not complete while the handler blocked")
		}

		close(release)
		// The blocked handler's answer follows with the agent's raw ID.
		answer := f.recv(t)
		if got := rawString(t, answer["id"]); got != agentID {
			t.Fatalf("blocked answer id = %q, want %q", got, agentID)
		}
	})
}

// TestConnectionEndFailsPendingCalls checks that ending the connection fails a
// waiting call with ErrClosed, closes Done, and refuses later calls.
func TestConnectionEndFailsPendingCalls(t *testing.T) {
	client, agent := Pipe()
	conn := NewConn(client, Handlers{})
	defer conn.Close()

	sc := bufio.NewScanner(agent)
	errCh := make(chan error, 1)
	go func() { errCh <- conn.Call(context.Background(), "never", nil, nil) }()

	if !sc.Scan() {
		t.Fatalf("read request: %v", sc.Err())
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("agent.Close: %v", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("pending call error = %v, want an error wrapping ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call did not fail within a second")
	}

	select {
	case <-conn.Done():
	case <-time.After(time.Second):
		t.Fatal("Done was not closed within a second")
	}

	if err := conn.Call(context.Background(), "again", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after the end = %v, want an error wrapping ErrClosed", err)
	}
}

// TestCloseReportsErrClosed checks that Close leaves Err() equal to ErrClosed.
func TestCloseReportsErrClosed(t *testing.T) {
	f := start(t, Handlers{})
	if err := f.conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := f.conn.Err(); err != ErrClosed {
		t.Fatalf("Err() = %v, want %v", err, ErrClosed)
	}
}

// TestAnOversizedFrameEndsTheConnection checks that a frame over the stream's
// cap ends the connection with both ErrClosed and bufio.ErrTooLong.
func TestAnOversizedFrameEndsTheConnection(t *testing.T) {
	pr, pw := io.Pipe()
	tr := NewStream(pr, io.Discard, 1<<10)
	conn := NewConn(tr, Handlers{})
	defer conn.Close()

	go func() {
		_, _ = pw.Write(append(bytes.Repeat([]byte("a"), 2<<10), '\n'))
	}()

	select {
	case <-conn.Done():
	case <-time.After(time.Second):
		t.Fatal("connection did not end within a second")
	}

	err := conn.Err()
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Err() = %v, want an error wrapping ErrClosed", err)
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("Err() = %v, want an error wrapping bufio.ErrTooLong", err)
	}
}
