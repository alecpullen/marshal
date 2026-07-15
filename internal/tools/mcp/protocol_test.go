package mcp

import (
	"encoding/json"
	"testing"
)

func TestJSONRPCSerialization(t *testing.T) {
	req := Request{
		JSONRPC: "2.0",
		ID:      json.Number("1"),
		Method:  "initialize",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back Request
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Method != "initialize" || back.ID != json.Number("1") {
		t.Errorf("roundtrip failed: %+v", back)
	}
}
