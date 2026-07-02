package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrInvalidTool   = errors.New("invalid tool")
	ErrDuplicateTool = errors.New("duplicate tool")
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
	if len(tool.Schema) > 0 && !json.Valid(tool.Schema) {
		return fmt.Errorf("%w: schema for %q is not valid JSON", ErrInvalidTool, tool.Name)
	}
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateTool, tool.Name)
	}

	r.tools[tool.Name] = tool
	return nil
}

func (r *Registry) Lookup(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) List() []Tool {
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}

	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})

	return tools
}
