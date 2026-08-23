package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrInvalidTool   = errors.New("invalid tool")
	ErrDuplicateTool = errors.New("duplicate tool")
	ErrToolNotFound  = errors.New("tool not found")
)

type Registry struct {
	tools map[string]Tool
}

func New() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

func (r *Registry) Register(tool Tool) error {
	if strings.TrimSpace(tool.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidTool)
	}
	if tool.Handler == nil {
		return fmt.Errorf("%w: handler is required for %q", ErrInvalidTool, tool.Name)
	}
	if !tool.Risk.Valid() {
		return fmt.Errorf("%w: unknown risk level %q for %q", ErrInvalidTool, tool.Risk, tool.Name)
	}
	// Compile the schema rather than merely checking it is JSON. A schema
	// that cannot compile would otherwise sit in the registry advertising
	// constraints that are never enforced — fail closed instead.
	validator, err := CompileSchema(tool.Name, tool.Schema)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTool, err)
	}
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateTool, tool.Name)
	}

	stored := cloneTool(tool)
	stored.validator = validator
	r.tools[tool.Name] = stored
	return nil
}

// Replace upserts a tool: any existing registration of the same name is
// removed, then the tool is registered fresh (all Register validation
// applies). Used to rebind a scoped view's tool to a different session —
// e.g. a subagent's todo.write bound to the child's own state.
func (r *Registry) Replace(tool Tool) error {
	delete(r.tools, tool.Name)
	return r.Register(tool)
}

func (r *Registry) Lookup(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	if !ok {
		return Tool{}, false
	}
	return cloneTool(tool), true
}

// Dispatch looks up a tool, validates the call's arguments against its
// schema, and invokes the handler. This is the authoritative enforcement
// point: a handler reached through Dispatch has never seen arguments that
// violate its declared schema.
func (r *Registry) Dispatch(ctx context.Context, call ToolCall) (ToolResult, error) {
	tool, ok := r.Lookup(call.Name)
	if !ok {
		return ToolResult{}, fmt.Errorf("%w: %q", ErrToolNotFound, call.Name)
	}
	if err := ValidateArgs(tool, call.Args); err != nil {
		return ToolResult{}, err
	}
	return tool.Handler(ctx, call)
}

func (r *Registry) List() []Tool {
	return r.listWhere(nil)
}

// ListDeferred returns all registered tools flagged with Deferred=true,
// sorted by name. Used by the prompt builder and disclosure tools to
// enumerate deferred tools (native config.*/data tools and MCP tools)
// hidden from the default tool list.
func (r *Registry) ListDeferred() []Tool {
	return r.listWhere(func(t Tool) bool { return t.Deferred })
}

// ListLoaded returns the subset of all tools whose name appears in the
// provided loaded-names set, sorted by name. Names that are not registered
// are silently skipped; the prompt builder uses this to expand the visible
// tool list once the agent opts in to specific deferred tools (config.*,
// data tools, or MCP tools) via tools.select.
func (r *Registry) ListLoaded(names []string) []Tool {
	if len(names) == 0 {
		return nil
	}
	loaded := make(map[string]bool, len(names))
	for _, name := range names {
		loaded[name] = true
	}
	return r.listWhere(func(t Tool) bool { return loaded[t.Name] })
}

// listWhere returns clones of all registered tools passing keep (nil keeps
// all), sorted by name.
func (r *Registry) listWhere(keep func(Tool) bool) []Tool {
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		if keep != nil && !keep(tool) {
			continue
		}
		tools = append(tools, cloneTool(tool))
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})
	return tools
}

func cloneTool(tool Tool) Tool {
	cloned := tool
	if len(tool.Schema) > 0 {
		cloned.Schema = append(json.RawMessage(nil), tool.Schema...)
	}
	// cloned.validator intentionally shares the compiled schema: it is
	// immutable after compilation and safe for concurrent use.
	return cloned
}
