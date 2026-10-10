//go:build !windows

package native

import "marshal/internal/tools/registry"

// sudoTools returns the privileged-execution tools. Empty on platforms
// without sudo; see sudo_register_windows.go.
func (t *toolSet) sudoTools() []registry.Tool {
	return []registry.Tool{t.sudoTool()}
}
