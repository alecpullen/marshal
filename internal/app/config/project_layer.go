package config

import "reflect"

// applyProjectLayer removes sections from file that the project layer does
// not contribute. When the merged value equals the user-layer value, the
// setting originated in the user config or the defaults, so it must not be
// baked into the project file. Sections absent from the file mirror are
// left alone (they were not going to be written anyway).
//
// [providers] and [models.presets] need no handling here: they are
// user-global only, so writeSections never emits them into a project save.
func applyProjectLayer(file *configFile, merged Config, layers Layers) {
	// A zero Layers means the caller has no load-time snapshot (tests and
	// unusual callers): preserve the historical write-everything behaviour.
	if reflect.DeepEqual(layers, Layers{}) {
		return
	}
	user := layers.User
	if file.Project != nil && reflect.DeepEqual(merged.Project, user.Project) {
		file.Project = nil
	}
	if file.Commands != nil && merged.Commands == user.Commands {
		file.Commands = nil
	}
	if file.Profile != nil && reflect.DeepEqual(merged.Profile, user.Profile) {
		file.Profile = nil
	}
	if file.Agent != nil && reflect.DeepEqual(merged.Agent, user.Agent) {
		file.Agent = nil
	}
	if file.Privacy != nil && reflect.DeepEqual(merged.Privacy, user.Privacy) {
		file.Privacy = nil
	}
	if file.Indexing != nil && reflect.DeepEqual(merged.Indexing, user.Indexing) {
		file.Indexing = nil
	}
	if file.Tools != nil && reflect.DeepEqual(merged.Tools, user.Tools) {
		file.Tools = nil
	}
	if file.Web != nil && reflect.DeepEqual(merged.Web, user.Web) {
		file.Web = nil
	}
	if file.Desktop != nil && reflect.DeepEqual(merged.Desktop, user.Desktop) {
		file.Desktop = nil
	}
	if file.Swarm != nil && reflect.DeepEqual(merged.Swarm, user.Swarm) {
		file.Swarm = nil
	}
	if file.SDD != nil && reflect.DeepEqual(merged.SDD, user.SDD) {
		file.SDD = nil
	}
	if file.MCP != nil && reflect.DeepEqual(merged.MCP, user.MCP) {
		file.MCP = nil
	}
	if file.Snapshots != nil && merged.Snapshots == user.Snapshots {
		file.Snapshots = nil
	}
	if file.History != nil && merged.History == user.History {
		file.History = nil
	}
	if file.TUI != nil && reflect.DeepEqual(merged.TUI, user.TUI) {
		file.TUI = nil
	}
	if file.Permissions != nil && reflect.DeepEqual(merged.Permissions, user.Permissions) {
		file.Permissions = nil
	}
	if file.Diagnostics != nil && reflect.DeepEqual(merged.Diagnostics, user.Diagnostics) {
		file.Diagnostics = nil
	}
	if file.Hooks != nil && reflect.DeepEqual(merged.Hooks, user.Hooks) {
		file.Hooks = nil
	}
	if file.Session != nil && reflect.DeepEqual(merged.Session, user.Session) {
		file.Session = nil
	}
	if file.Skills != nil && reflect.DeepEqual(merged.Skills, user.Skills) {
		file.Skills = nil
	}
	if file.AgentProfilesRaw != nil && reflect.DeepEqual(merged.AgentProfiles, user.AgentProfiles) {
		file.AgentProfilesRaw = nil
	}
	if file.CustomAgents != nil && reflect.DeepEqual(merged.CustomAgents, user.CustomAgents) {
		file.CustomAgents = nil
	}
	if file.Agents != nil && reflect.DeepEqual(merged.Agents, user.Agents) {
		file.Agents = nil
	}
	if file.LSP != nil && reflect.DeepEqual(merged.LSP, user.LSP) {
		file.LSP = nil
	}
	if file.Scratchpad != nil && reflect.DeepEqual(merged.Scratchpad, user.Scratchpad) {
		file.Scratchpad = nil
	}
}
