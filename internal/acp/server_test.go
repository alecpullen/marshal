package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServerInitialize(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n")
	out := &bytes.Buffer{}
	srv := NewServer(in, out)
	srv.Handle("initialize", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]any{
			"protocolVersion": 1,
			"agent":           map[string]any{"name": "Marshal"},
		}, nil
	})
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scan.Scan() {
		t.Fatalf("no response line written; buf=%q", out.String())
	}
	var resp Response
	if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; line=%q", err, scan.Text())
	}
	if resp.ID == nil || string(*resp.ID) != "1" {
		t.Fatalf("resp.ID = %v, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error = %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("resp.Result type = %T", resp.Result)
	}
	if m["protocolVersion"] != float64(1) {
		t.Fatalf("protocolVersion = %v", m["protocolVersion"])
	}
	agent, ok := m["agent"].(map[string]any)
	if !ok || agent["name"] != "Marshal" {
		t.Fatalf("agent = %v", m["agent"])
	}
}

func TestServerNotificationNoResponse(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"event"}` + "\n")
	out := &bytes.Buffer{}
	srv := NewServer(in, out)
	srv.Handle("event", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output for notification, got %q", out.String())
	}
}

func TestServerUnknownMethod(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"nope"}` + "\n")
	out := &bytes.Buffer{}
	srv := NewServer(in, out)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scan.Scan() {
		t.Fatalf("expected error response line")
	}
	var resp Response
	if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error for unknown method, got %+v", resp)
	}
}
