package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	stdctx "context"

	"github.com/alecpullen/marshal/internal/context"
	"github.com/alecpullen/marshal/pkg/protocol"
	"github.com/zeebo/blake3"
)

// Context is an alias for standard context.Context
type Context = stdctx.Context

// Registry manages tool adapters for the agent.
type Registry struct {
	tools     map[string]Tool
	readSet   ReadSetTracker
	enforce   bool
	store     *context.Store
}

// ReadSetTracker is the interface the registry needs from the agent's read-set.
type ReadSetTracker interface {
	HasRead(path string) bool
	RecordRead(path string, hash string, readVia string)
	GetHash(path string) (string, bool)
}

// NewRegistry creates a tool registry with the given inner tools.
func NewRegistry(readSet ReadSetTracker, enforce bool, store *context.Store) *Registry {
	return &Registry{
		tools:   make(map[string]Tool),
		readSet: readSet,
		enforce: enforce,
		store:   store,
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool Tool) {
	r.tools[tool.Name()] = tool
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tool names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Adapter wraps a tool with enforcement capabilities.
type Adapter struct {
	inner   Tool
	readSet ReadSetTracker
	enforce bool
	store   *context.Store
}

// NewAdapter creates a wrapped tool with enforcement.
func NewAdapter(inner Tool, readSet ReadSetTracker, enforce bool, store *context.Store) *Adapter {
	return &Adapter{
		inner:   inner,
		readSet: readSet,
		enforce: enforce,
		store:   store,
	}
}

// Name returns the tool name.
func (a *Adapter) Name() string {
	return a.inner.Name()
}

// Description returns the tool description.
func (a *Adapter) Description() string {
	return a.inner.Description()
}

// Schema returns the tool schema.
func (a *Adapter) Schema() json.RawMessage {
	return a.inner.Schema()
}

// IsReadOperation returns true if this tool reads files.
func (a *Adapter) IsReadOperation() bool {
	return a.inner.IsReadOperation()
}

// IsMutating returns true if this tool modifies files.
func (a *Adapter) IsMutating() bool {
	return a.inner.IsMutating()
}

// RequiresReadBeforeEdit returns true for mutating tools.
func (a *Adapter) RequiresReadBeforeEdit() bool {
	return a.inner.IsMutating()
}

// Invoke executes the tool with enforcement checks.
func (a *Adapter) Invoke(ctx Context, args json.RawMessage) (*Result, error) {
	// Extract path from args if present
	path, hasPath := extractPath(args)

	// Check read-before-edit for mutating tools
	if a.inner.IsMutating() && a.enforce && hasPath {
		if !a.readSet.HasRead(path) {
			return nil, &ToolError{
				Code:    "read_before_edit",
				Message: fmt.Sprintf("Cannot modify %s: file was not read first", path),
				Hint:    fmt.Sprintf("Use read_file or ctx_fetch on %s before editing", path),
				Details: map[string]any{
					"path":       path,
					"suggestion": fmt.Sprintf("read_file path=%s", path),
				},
			}
		}

		// Verify file hasn't changed since read (staleness detection)
		if expectedHash, ok := a.readSet.GetHash(path); ok {
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, &ToolError{
					Code:    "file_not_found",
					Message: fmt.Sprintf("Cannot verify %s: %v", path, err),
					Hint:    "Check that the file exists and is accessible",
				}
			}

			actualHash := hashContent(content)
			if expectedHash != actualHash {
				return nil, &ToolError{
					Code:    "stale_hash",
					Message: fmt.Sprintf("File %s changed since last read", path),
					Hint:    fmt.Sprintf("Re-read the file with read_file path=%s, then retry the edit", path),
					Details: map[string]any{
						"path":                 path,
						"expected_hash_prefix": truncateHash(expectedHash),
						"actual_hash_prefix":   truncateHash(actualHash),
					},
				}
			}
		}
	}

	// Execute the actual tool
	result, err := a.inner.Invoke(ctx, args)
	if err != nil {
		// Check if it's already a ToolError
		if toolErr, ok := err.(*ToolError); ok {
			return nil, toolErr
		}
		// Wrap as ToolError
		return nil, &ToolError{
			Code:    "execution_failed",
			Message: err.Error(),
			Hint:    "The tool execution failed unexpectedly",
		}
	}

	// Track reads in read-set
	if a.inner.IsReadOperation() && hasPath {
		// Get content hash for tracking
		content, err := os.ReadFile(path)
		if err == nil {
			hash := hashContent(content)
			a.readSet.RecordRead(path, hash, a.inner.Name())
			result.ReadPath = path
			result.ReadHash = hash
			result.ContentSize = len(content)
		}
	}

	// Store to context store if appropriate
	if a.store != nil && a.inner.IsReadOperation() && hasPath {
		ref, err := a.storeReadResult(ctx, path, result)
		if err == nil {
			result.ContextRef = ref
		}
	}

	return result, nil
}

// storeReadResult stores a read operation result to the context store.
func (a *Adapter) storeReadResult(ctx Context, path string, result *Result) (protocol.ContextRef, error) {
	content := []byte(result.Content)
	key := protocol.NewEntryKey(protocol.EntryKindFile, path)

	ref, err := a.store.Put(content, key, a.inner.Name(), nil, 0)
	return protocol.ContextRef(ref), err
}

// Helper functions
func extractPath(args json.RawMessage) (string, bool) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", false
	}
	return params.Path, params.Path != ""
}

func hashContent(content []byte) string {
	hash := blake3.Sum256(content)
	return fmt.Sprintf("%x", hash)
}

func truncateHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// Check if path escapes repo root
func isPathSafe(repoRoot, path string) bool {
	absPath := filepath.Join(repoRoot, filepath.Clean(path))
	return strings.HasPrefix(absPath, repoRoot)
}
