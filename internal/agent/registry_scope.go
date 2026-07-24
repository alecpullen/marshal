package agent

import "marshal/internal/tools/registry"

// DenylistView returns a new registry containing every tool in src
// except those whose Name is in deny. An empty denylist returns a copy
// of src (inherit-all semantics). Mirrors SubtaskScopeView's pattern.
func DenylistView(src *registry.Registry, deny []string) *registry.Registry {
	view := registry.New()
	if len(deny) == 0 {
		for _, tool := range src.List() {
			_ = view.Register(tool)
		}
		return view
	}
	denied := make(map[string]bool, len(deny))
	for _, n := range deny {
		denied[n] = true
	}
	for _, tool := range src.List() {
		if denied[tool.Name] {
			continue
		}
		_ = view.Register(tool)
	}
	return view
}
