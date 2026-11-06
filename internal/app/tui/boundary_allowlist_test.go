package tui

// boundaryAllowlist lists every TUI file that still crosses the seam, by
// import and by config function. Each P2 plan deletes its group's entries, and
// plan 9 empties this map.
//
// An entry is never added to make TestTUIStaysOnTheClientSeam pass. A new use
// belongs behind acpclient.
var boundaryAllowlist = map[string][]string{
	"commands_dispatch.go": {"marshal/internal/worktree"},
	"memory/entries.go":    {"marshal/internal/db"},
	"memory/panel.go":      {"marshal/internal/db"},
	"model.go":             {"marshal/internal/app/session", "marshal/internal/db", "marshal/internal/pubsub", "marshal/internal/sddauthor", "marshal/internal/sddplans", "marshal/internal/skills"},
	"plugins/panel.go":     {"marshal/internal/plugins"},
	"runoutcome.go":        {"marshal/internal/worktree"},
	"skills/panel.go":      {"config.SaveUserConfigValue", "config.UserConfigPath", "config.UserDir", "marshal/internal/app/session", "marshal/internal/skills"},
	"todos.go":             {"marshal/internal/app/session", "marshal/internal/tools/native"},
}
