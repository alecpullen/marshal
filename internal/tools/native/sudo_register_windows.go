//go:build windows

package native

import "marshal/internal/tools/registry"

// sudoTools is empty on Windows: there is no sudo, and privileged execution
// is a non-goal (spec §Scope).
func (t *toolSet) sudoTools() []registry.Tool { return nil }
