package acp

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"marshal/web/bridge/contract"
)

// TestForwardedRepliesDecodeIntoTheContract proves a reply this package
// produces for a forwarded request decodes strictly into the bridge's
// contract type, with nothing silently dropped.
func TestForwardedRepliesDecodeIntoTheContract(t *testing.T) {
	cases := []struct {
		name  string
		local any
		into  func() any
	}{
		{
			name: "diff",
			local: DiffResult{
				Files: []DiffFile{
					{Path: "internal/acp/worktree.go", Added: 12, Removed: 3},
					{Path: "internal/acp/contract_test.go", Added: 48, Removed: 2},
				},
				Diff: "--- a/internal/acp/worktree.go\n+++ b/internal/acp/worktree.go\n",
			},
			into: func() any { return &contract.DiffResult{} },
		},
		{
			name: "merge refusal",
			local: MergeResult{
				Merged:    false,
				Branch:    "p5-1-contract",
				Target:    "main",
				Reason:    ReasonConflicts,
				Conflicts: []string{"internal/acp/worktree.go", "internal/acp/exit.go"},
			},
			into: func() any { return &contract.MergeResult{} },
		},
		{
			name:  "commit draft",
			local: map[string]any{"message": "the commit draft is a map today"},
			into:  func() any { return &contract.CommitDraft{} },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.local)
			if err != nil {
				t.Fatalf("marshal local reply: %v", err)
			}

			target := tc.into()
			dec := json.NewDecoder(bytes.NewReader(before))
			dec.DisallowUnknownFields()
			if err := dec.Decode(target); err != nil {
				t.Fatalf("decode into the contract with %s: %v", before, err)
			}
			if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
				t.Fatalf("decode after the JSON value: got %v, want io.EOF", err)
			}

			after, err := json.Marshal(target)
			if err != nil {
				t.Fatalf("marshal contract reply: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("re-encode differs:\n local: %s\ncontract: %s", before, after)
			}
		})
	}
}
