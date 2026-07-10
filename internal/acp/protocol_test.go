package acp

import (
	"encoding/json"
	"testing"
)

func TestDecodeRequest(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.ID == nil || req.Method != "initialize" {
		t.Fatalf("req = %+v", req)
	}
}

func TestDecodeRequest_Notification(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","method":"notify"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.ID != nil {
		t.Fatalf("notification should have nil ID, got %v", *req.ID)
	}
	if req.Method != "notify" {
		t.Fatalf("method = %q, want %q", req.Method, "notify")
	}
}
