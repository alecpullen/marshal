package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// ScratchpadEntry is the tool-level alias for the persisted scratchpad type.
type ScratchpadEntry = db.ScratchpadEntry

// ScratchpadStore is the subset of session.State the scratchpad tools need.
type ScratchpadStore interface {
	SetScratchpadEntry(key, content, format string) error
	DeleteScratchpadEntry(key string) error
	Scratchpad() []ScratchpadEntry
	ScratchpadEntry(key string) (ScratchpadEntry, bool)
}

type scratchpadWriteArgs struct {
	Key     string `json:"key"`
	Content string `json:"content"`
	Format  string `json:"format"`
}

type scratchpadReadArgs struct {
	Key string `json:"key"`
}

type scratchpadDeleteArgs struct {
	Key string `json:"key"`
}

func estimateTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}

func (t *toolSet) scratchpadWriteTool() registry.Tool {
	tool := registry.Tool{
		Name:        "scratchpad.write",
		Description: "Store or update a key-value entry in the session scratchpad. Use this to park structured intermediate state (audit results, file lists, decision logs) that should survive context compaction but is too large for the context budget. The full content is retrievable via scratchpad.read; a compact projection is injected into the context pack automatically.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","description":"A short identifier for the entry, e.g. 'audit-results' or 'files-to-fix'."},"content":{"type":"string","description":"The full content to store."},"format":{"type":"string","enum":["text","json","table"],"description":"Hint for rendering. Defaults to text."}},"required":["key","content"],"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[scratchpadWriteArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if strings.TrimSpace(args.Key) == "" {
			return registry.ToolResult{}, fmt.Errorf("key must not be empty")
		}
		if strings.TrimSpace(args.Content) == "" {
			return registry.ToolResult{}, fmt.Errorf("content must not be empty")
		}
		format := args.Format
		if format == "" {
			format = "text"
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("scratchpad store not available")
		}
		store := ScratchpadStore(t.sessionState)
		if err := store.SetScratchpadEntry(args.Key, args.Content, format); err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("scratchpad entry %q written", args.Key),
			Content: fmt.Sprintf("Entry %q stored (%s format, %d bytes).", args.Key, format, len(args.Content)),
		}, nil
	}
	return tool
}

func (t *toolSet) scratchpadReadTool() registry.Tool {
	tool := registry.Tool{
		Name:        "scratchpad.read",
		Description: "Read the full content of a scratchpad entry by key. Use this when the projection in the context pack indicates an entry exists but you need the complete content.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","description":"The key of the entry to read."}},"required":["key"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[scratchpadReadArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if strings.TrimSpace(args.Key) == "" {
			return registry.ToolResult{}, fmt.Errorf("key must not be empty")
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("scratchpad store not available")
		}
		store := ScratchpadStore(t.sessionState)
		entry, ok := store.ScratchpadEntry(args.Key)
		if !ok {
			return registry.ToolResult{}, fmt.Errorf("no scratchpad entry with key %q", args.Key)
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("scratchpad entry %q (%d bytes)", entry.Key, len(entry.Content)),
			Content: entry.Content,
		}, nil
	}
	return tool
}

func (t *toolSet) scratchpadListTool() registry.Tool {
	tool := registry.Tool{
		Name:        "scratchpad.list",
		Description: "List all scratchpad entries: keys, formats, and estimated token counts. Use this to see what intermediate state is currently parked.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("scratchpad store not available")
		}
		store := ScratchpadStore(t.sessionState)
		entries := store.Scratchpad()
		if len(entries) == 0 {
			return registry.ToolResult{
				Summary: "scratchpad is empty",
				Content: "No scratchpad entries.",
			}, nil
		}
		var lines []string
		for _, e := range entries {
			tokens := estimateTokens(e.Content)
			fmtVal := e.Format
			if fmtVal == "" {
				fmtVal = "text"
			}
			lines = append(lines, fmt.Sprintf("%s (%s, ~%d tokens)", e.Key, fmtVal, tokens))
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("%d scratchpad entries", len(entries)),
			Content: strings.Join(lines, "\n"),
		}, nil
	}
	return tool
}

func (t *toolSet) scratchpadDeleteTool() registry.Tool {
	tool := registry.Tool{
		Name:        "scratchpad.delete",
		Description: "Delete a scratchpad entry by key. Idempotent — returns success even if the key does not exist.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","description":"The key of the entry to delete."}},"required":["key"],"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[scratchpadDeleteArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if strings.TrimSpace(args.Key) == "" {
			return registry.ToolResult{}, fmt.Errorf("key must not be empty")
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("scratchpad store not available")
		}
		store := ScratchpadStore(t.sessionState)
		if err := store.DeleteScratchpadEntry(args.Key); err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("scratchpad entry %q deleted", args.Key),
			Content: fmt.Sprintf("Entry %q deleted.", args.Key),
		}, nil
	}
	return tool
}
