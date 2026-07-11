package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// JSON-RPC error codes.
const (
	parseError       = -32700
	invalidRequest   = -32600
	methodNotFound   = -32601
	invalidParams    = -32602
	internalError    = -32603
	requestCancelled = -32800
	serverError      = -32000
)

type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// jsonRPCError is a typed error that carries a JSON-RPC error code.
// Use errors.As to unwrap it from wrapped errors.
type jsonRPCError struct {
	Code    int
	Message string
}

func (e *jsonRPCError) Error() string { return e.Message }

// codeFor extracts the JSON-RPC error code from an error, unwrapping as
// needed. Returns internalError (-32603) for unrecognised errors and
// requestCancelled (-32800) for context.Canceled.
func codeFor(err error) int {
	var rpcErr *jsonRPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code
	}
	if errors.Is(err, context.Canceled) {
		return requestCancelled
	}
	return internalError
}

func invalidParamsError(format string, args ...any) error {
	return &jsonRPCError{Code: invalidParams, Message: fmt.Sprintf(format, args...)}
}

func serverErrorf(format string, args ...any) error {
	return &jsonRPCError{Code: serverError, Message: fmt.Sprintf(format, args...)}
}

// MarshalJSON ensures a success response always includes "result"
// (even when nil) and an error response never includes "result".
func (r Response) MarshalJSON() ([]byte, error) {
	if r.Error != nil {
		return json.Marshal(struct {
			JSONRPC string           `json:"jsonrpc"`
			ID      *json.RawMessage `json:"id,omitempty"`
			Error   *Error           `json:"error"`
		}{
			JSONRPC: r.JSONRPC,
			ID:      r.ID,
			Error:   r.Error,
		})
	}
	return json.Marshal(struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id,omitempty"`
		Result  any              `json:"result"`
	}{
		JSONRPC: r.JSONRPC,
		ID:      r.ID,
		Result:  r.Result,
	})
}

// ContentBlock is an ACP v1 content block. Supported types are "text" and
// "resource_link". The ACP layer never dereferences a resource-link URI.
type ContentBlock struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	URI         string `json:"uri,omitempty"`
	Name        string `json:"name,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

// SessionUpdateParams is the envelope for session/update notifications.
type SessionUpdateParams struct {
	SessionID string         `json:"sessionId"`
	Update    map[string]any `json:"update"`
}

// normalizePrompt converts ACP content blocks into a flat prompt string.
// Only "text" and "resource_link" blocks are accepted; unsupported types,
// empty content, nil blocks, and missing required resource_link fields
// return an invalidParamsError.
func normalizePrompt(blocks []ContentBlock) (string, error) {
	if len(blocks) == 0 {
		return "", invalidParamsError("prompt must contain at least one block")
	}
	var parts []string
	for i, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) == "" {
				return "", invalidParamsError("text block %d has empty text", i)
			}
			parts = append(parts, block.Text)
		case "resource_link":
			if strings.TrimSpace(block.Name) == "" {
				return "", invalidParamsError("resource_link block %d is missing name", i)
			}
			if strings.TrimSpace(block.URI) == "" {
				return "", invalidParamsError("resource_link block %d is missing uri", i)
			}
			parts = append(parts, fmt.Sprintf("Resource link: %s (%s)", block.Name, block.URI))
		default:
			return "", invalidParamsError("unsupported block type: %s", block.Type)
		}
	}
	if len(parts) == 0 {
		return "", invalidParamsError("prompt must contain at least one valid block")
	}
	return strings.Join(parts, "\n\n"), nil
}
