package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// DefaultTools returns the standard tool set available to executor agents.
// repoRoot is used only for documentation in descriptions; actual sandboxing
// happens inside each tool's implementation.
func DefaultTools(_ string) []Definition {
	return []Definition{
		{
			Name:        "read_file",
			Description: "Read the contents of a file in the repository. Returns the full file text.",
			Parameters: ParameterSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"path": {Type: "string", Description: "Path to the file, relative to the repository root."},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "write_file",
			Description: "Write or overwrite a file in the repository with new content.",
			Parameters: ParameterSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"path":    {Type: "string", Description: "Path to the file, relative to the repository root."},
					"content": {Type: "string", Description: "Full content to write to the file."},
				},
				Required: []string{"path", "content"},
			},
		},
		{
			Name:        "edit_file",
			Description: "Replace a range of lines in a file. Lines are 1-indexed and the range is inclusive.",
			Parameters: ParameterSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"path":        {Type: "string", Description: "Path to the file, relative to the repository root."},
					"start_line":  {Type: "integer", Description: "First line to replace (1-indexed)."},
					"end_line":    {Type: "integer", Description: "Last line to replace (1-indexed, inclusive)."},
					"new_content": {Type: "string", Description: "Replacement text for the specified line range."},
				},
				Required: []string{"path", "start_line", "end_line", "new_content"},
			},
		},
		{
			Name:        "run_command",
			Description: "Run a build or test command in the repository root. Allowed executables: go, make, npm, npx, python, python3, pytest, cargo, rg.",
			Parameters: ParameterSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"args": {Type: "string", Description: "Space-separated command and arguments, e.g. \"go test ./...\" or \"make build\"."},
				},
				Required: []string{"args"},
			},
		},
		{
			Name:        "search_code",
			Description: "Search the repository for lines matching a regular expression. Returns file:line:content for each match.",
			Parameters: ParameterSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"pattern": {Type: "string", Description: "Regular expression to search for."},
					"glob":    {Type: "string", Description: "Optional filename glob to restrict the search, e.g. \"*.go\" or \"*.ts\". Leave empty to search all files."},
				},
				Required: []string{"pattern"},
			},
		},
		{
			Name:        "list_directory",
			Description: "List the files and subdirectories in a directory.",
			Parameters: ParameterSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"path": {Type: "string", Description: "Path to the directory, relative to the repository root. Use \".\" for the root."},
				},
				Required: []string{"path"},
			},
		},
	}
}

// Execute dispatches a tool Call to its implementation and returns the Result.
// All file and command operations are sandboxed to repoRoot.
func Execute(ctx context.Context, call Call, repoRoot string) Result {
	content, err := dispatch(ctx, call, repoRoot)
	if err != nil {
		return Result{CallID: call.ID, Content: err.Error(), IsError: true}
	}
	return Result{CallID: call.ID, Content: content}
}

func dispatch(ctx context.Context, call Call, repoRoot string) (string, error) {
	args := call.Arguments

	strArg := func(key string) (string, error) {
		v, ok := args[key]
		if !ok {
			return "", fmt.Errorf("missing required argument %q", key)
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("argument %q must be a string", key)
		}
		return s, nil
	}

	intArg := func(key string) (int, error) {
		v, ok := args[key]
		if !ok {
			return 0, fmt.Errorf("missing required argument %q", key)
		}
		switch n := v.(type) {
		case float64:
			return int(n), nil
		case int:
			return n, nil
		case string:
			i, err := strconv.Atoi(n)
			if err != nil {
				return 0, fmt.Errorf("argument %q is not an integer: %w", key, err)
			}
			return i, nil
		default:
			return 0, fmt.Errorf("argument %q must be an integer", key)
		}
	}

	switch call.ToolName {
	case "read_file":
		path, err := strArg("path")
		if err != nil {
			return "", err
		}
		return readFile(repoRoot, path)

	case "write_file":
		path, err := strArg("path")
		if err != nil {
			return "", err
		}
		content, err := strArg("content")
		if err != nil {
			return "", err
		}
		return "", writeFile(repoRoot, path, content)

	case "edit_file":
		path, err := strArg("path")
		if err != nil {
			return "", err
		}
		startLine, err := intArg("start_line")
		if err != nil {
			return "", err
		}
		endLine, err := intArg("end_line")
		if err != nil {
			return "", err
		}
		newContent, err := strArg("new_content")
		if err != nil {
			return "", err
		}
		return "", editFile(repoRoot, path, startLine, endLine, newContent)

	case "run_command":
		argsStr, err := strArg("args")
		if err != nil {
			return "", err
		}
		cmdArgs := strings.Fields(argsStr)
		return runCommand(ctx, repoRoot, cmdArgs)

	case "search_code":
		pattern, err := strArg("pattern")
		if err != nil {
			return "", err
		}
		glob, _ := args["glob"].(string) // optional
		return searchCode(repoRoot, pattern, glob)

	case "list_directory":
		path, err := strArg("path")
		if err != nil {
			return "", err
		}
		return listDirectory(repoRoot, path)

	default:
		return "", fmt.Errorf("unknown tool %q", call.ToolName)
	}
}
