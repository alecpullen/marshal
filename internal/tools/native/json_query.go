package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/itchyny/gojq"

	"marshal/internal/tools/registry"
)

const (
	defaultJSONQueryLimit = 100
	hardJSONQueryLimit    = 1000
)

type jsonQueryArgs struct {
	Path  string `json:"path"`
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (t *toolSet) jsonQueryTool() registry.Tool {
	tool := registry.Tool{
		Name:        "json.query",
		Description: "Query a workspace JSON file with a jq expression (e.g. `.dependencies | keys`, `.users[] | select(.active)`). Returns each result value as JSON, one per line. Prefer this over shelling out to jq or python.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"query":{"type":"string"},"limit":{"type":"integer"}},"required":["path","query"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[jsonQueryArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Path == "" {
			return registry.ToolResult{}, fmt.Errorf("json.query path is required")
		}
		if args.Query == "" {
			return registry.ToolResult{}, fmt.Errorf("json.query query is required")
		}

		query, err := gojq.Parse(args.Query)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("json.query invalid jq expression %q: %w", args.Query, err)
		}

		path, err := resolveWorkspacePathMulti(t.activeRoot(), t.additionalRoots, args.Path)
		if err != nil {
			return registry.ToolResult{}, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("read %s: %w", args.Path, err)
		}

		var input any
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber() // keep integers exact through gojq's number normalization
		if err := dec.Decode(&input); err != nil {
			return registry.ToolResult{}, fmt.Errorf("%s is not valid JSON: %w", args.Path, err)
		}

		limit := args.Limit
		if limit <= 0 {
			limit = defaultJSONQueryLimit
		}
		if limit > hardJSONQueryLimit {
			limit = hardJSONQueryLimit
		}

		var out bytes.Buffer
		count := 0
		truncated := false
		iter := query.RunWithContext(ctx, input)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, isErr := v.(error); isErr {
				return registry.ToolResult{}, fmt.Errorf("json.query evaluation failed: %w", err)
			}
			if count >= limit {
				truncated = true
				break
			}
			b, err := json.Marshal(v)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("json.query could not encode result %d: %w", count+1, err)
			}
			out.Write(b)
			out.WriteByte('\n')
			count++
		}

		summary := fmt.Sprintf("%d result(s)", count)
		if truncated {
			summary += fmt.Sprintf(" (limited to %d)", limit)
		}
		return registry.ToolResult{
			Summary: summary,
			Content: limitOutput(out.String(), t.maxOutputBytes),
		}, nil
	}
	return tool
}
