package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"marshal/internal/tools/patch"
	"marshal/internal/tools/registry"
)

type fileEditArgs struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

func (t *toolSet) fileEditTool() registry.Tool {
	tool := registry.Tool{
		Name: "file.edit",
		Description: "Replace one exact occurrence of old_string with new_string in a file you have read this session. " +
			"old_string must match the file byte-for-byte (including indentation) and be unique; include surrounding lines to disambiguate. " +
			"Use file.write to create a file or replace most of it. Edit several files with several calls.",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":` +
			t.pathDescription("file to edit, relative to the workspace") +
			`},"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["path","old_string","new_string"],"additionalProperties":false}`),
		Risk: registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileEditArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.OldString == "" {
			return registry.ToolResult{}, errors.New("file.edit: old_string is required; use file.write to create or overwrite a whole file")
		}
		if args.OldString == args.NewString {
			return registry.ToolResult{}, errors.New("file.edit: old_string and new_string are identical; nothing to change")
		}
		res, err := t.applyFilePatches(ctx, []patch.FilePatch{{
			Path:   args.Path,
			Chunks: []patch.PatchChunk{{Search: args.OldString, Replace: args.NewString}},
		}}, nil, "use file.write to create a new file")
		if err != nil {
			return registry.ToolResult{}, err
		}
		res.Summary = fmt.Sprintf("Edited %s", args.Path)
		return res, nil
	}
	return tool
}
