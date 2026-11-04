package tui

// boundaryAllowlist lists every TUI file that still crosses the seam, by
// import and by config function. Each P2 plan deletes its group's entries, and
// plan 9 empties this map.
//
// An entry is never added to make TestTUIStaysOnTheClientSeam pass. A new use
// belongs behind acpclient.
var boundaryAllowlist = map[string][]string{
	"agents/panel.go":                {"config.SaveProjectConfig", "marshal/internal/tools/registry"},
	"commands_dispatch.go":           {"marshal/internal/worktree"},
	"connect/connect.go":             {"marshal/internal/app/presetsetup", "marshal/internal/llm/modelcatalog", "marshal/internal/llm/provider", "marshal/internal/oauth"},
	"doctor_secret.go":               {"config.SaveUserConfigProviderAPIKey", "config.UserConfigPath"},
	"doctorpanel/doctorpanel.go":     {"marshal/internal/app/session"},
	"doctorpanel/intelligence.go":    {"marshal/internal/db", "marshal/internal/lsp"},
	"livestrip.go":                   {"marshal/internal/app/session"},
	"mcp.go":                         {"marshal/internal/commands", "marshal/internal/credentials", "marshal/internal/oauth"},
	"memory/entries.go":              {"marshal/internal/db"},
	"memory/panel.go":                {"marshal/internal/db"},
	"model.go":                       {"config.DataDir", "config.ProjectConfigPath", "config.SaveProjectConfig", "config.SaveUserConfigPresets", "config.SaveUserConfigProviders", "config.SaveUserConfigValue", "config.SaveVerifyCommands", "config.UserConfigPath", "marshal/internal/app/presetsetup", "marshal/internal/app/session", "marshal/internal/db", "marshal/internal/llm/provider", "marshal/internal/llm/provider/modelcache", "marshal/internal/pubsub", "marshal/internal/sddauthor", "marshal/internal/sddplans", "marshal/internal/skills", "marshal/internal/tools/registry", "marshal/internal/trust", "marshal/internal/watch"},
	"model_options.go":               {"config.SaveUserConfigPresets", "config.UserConfigPath", "marshal/internal/llm/provider"},
	"plugins/panel.go":               {"marshal/internal/plugins"},
	"presetflow/probe.go":            {"marshal/internal/llm/provider"},
	"probe/probe.go":                 {"marshal/internal/llm/provider"},
	"runoutcome.go":                  {"marshal/internal/worktree"},
	"sdd_command.go":                 {"marshal/internal/app/session"},
	"settings/browser.go":            {"config.SaveProjectConfig", "config.SaveUserConfigPresets", "config.SaveUserConfigProviders", "config.SaveUserConfigValue", "config.UserConfigPath", "marshal/internal/llm/provider"},
	"settings/frames_collections.go": {"marshal/internal/llm/provider", "marshal/internal/llm/provider/modelcache"},
	"settings/state.go":              {"marshal/internal/llm/provider"},
	"skills/panel.go":                {"config.SaveUserConfigValue", "config.UserConfigPath", "config.UserDir", "marshal/internal/app/session", "marshal/internal/skills"},
	"todos.go":                       {"marshal/internal/app/session", "marshal/internal/tools/native"},
	"transcript.go":                  {"marshal/internal/app/session"},
	"trustpanel/trustpanel.go":       {"marshal/internal/trust"},
	"viewport_render.go":             {"marshal/internal/app/session"},
}
