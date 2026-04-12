// Package prompts embeds the base system-prompt files for the four model roles.
package prompts

import _ "embed"

//go:embed base/executor.md
var Executor string

//go:embed base/critic.md
var Critic string

//go:embed base/marshal.md
var Marshal string

//go:embed base/compactor.md
var Compactor string

//go:embed security/security.md
var Security string
