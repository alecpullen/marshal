package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func TestResponseMarshalSuccessNilIncludesResultNull(t *testing.T) {
	id := json.RawMessage(`1`)
	b, err := json.Marshal(Response{JSONRPC: "2.0", ID: &id, Result: nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"jsonrpc":"2.0","id":1,"result":null}` {
		t.Fatalf("response = %s", b)
	}
}

func TestResponseMarshalErrorOmitsResult(t *testing.T) {
	id := json.RawMessage(`1`)
	b, err := json.Marshal(Response{JSONRPC: "2.0", ID: &id, Error: &Error{Code: -32602, Message: "bad"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"error"`) {
		t.Fatalf("response missing error: %s", b)
	}
	if strings.Contains(string(b), `"result"`) {
		t.Fatalf("response should not contain result: %s", b)
	}
	var decoded struct {
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.Code != -32602 {
		t.Fatalf("unexpected error: %+v", decoded.Error)
	}
}

func TestCodeForWrappedJSONRPCError(t *testing.T) {
	wrapped := fmt.Errorf("decode: %w", &jsonRPCError{Code: -32602, Message: "test"})
	if got := codeFor(wrapped); got != -32602 {
		t.Fatalf("codeFor(wrapped) = %d, want -32602", got)
	}
}

func TestNormalizePrompt(t *testing.T) {
	tests := []struct {
		name    string
		blocks  []ContentBlock
		want    string
		wantErr bool
		errCode int
	}{
		{
			name: "two text blocks",
			blocks: []ContentBlock{
				{Type: "text", Text: "first"},
				{Type: "text", Text: "second"},
			},
			want: "first\n\nsecond",
		},
		{
			name: "text/resource-link/text",
			blocks: []ContentBlock{
				{Type: "text", Text: "inspect"},
				{Type: "resource_link", Name: "README", URI: "file:///repo/README.md"},
				{Type: "text", Text: "done"},
			},
			want: "inspect\n\nResource link: README (file:///repo/README.md)\n\ndone",
		},
		{
			name:    "nil blocks",
			blocks:  nil,
			wantErr: true,
			errCode: invalidParams,
		},
		{
			name: "empty text",
			blocks: []ContentBlock{
				{Type: "text", Text: ""},
			},
			wantErr: true,
			errCode: invalidParams,
		},
		{
			name: "missing resource name",
			blocks: []ContentBlock{
				{Type: "resource_link", URI: "file:///repo/README.md"},
			},
			wantErr: true,
			errCode: invalidParams,
		},
		{
			name: "missing URI",
			blocks: []ContentBlock{
				{Type: "resource_link", Name: "README"},
			},
			wantErr: true,
			errCode: invalidParams,
		},
		{
			name: "image type",
			blocks: []ContentBlock{
				{Type: "image"},
			},
			wantErr: true,
			errCode: invalidParams,
		},
		{
			name: "audio type",
			blocks: []ContentBlock{
				{Type: "audio"},
			},
			wantErr: true,
			errCode: invalidParams,
		},
		{
			name: "resource type",
			blocks: []ContentBlock{
				{Type: "resource"},
			},
			wantErr: true,
			errCode: invalidParams,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizePrompt(tt.blocks)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizePrompt(%v) = %q, want error", tt.blocks, got)
				}
				var rpcErr *jsonRPCError
				if !errors.As(err, &rpcErr) {
					t.Fatalf("normalizePrompt error is not a jsonRPCError: %v", err)
				}
				if rpcErr.Code != tt.errCode {
					t.Fatalf("normalizePrompt error code = %d, want %d", rpcErr.Code, tt.errCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizePrompt(%v) = _, %v, want %q", tt.blocks, err, tt.want)
			}
			if got != tt.want {
				t.Fatalf("normalizePrompt(%v) = %q, want %q", tt.blocks, got, tt.want)
			}
		})
	}
}
