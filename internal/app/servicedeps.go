package app

import (
	"context"

	"marshal/internal/acp/service"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/lsp"
	"marshal/internal/pubsub"
	"marshal/internal/skills"
	"marshal/internal/tools/native"
	"marshal/internal/trust"
	"marshal/internal/watch"
)

// ServiceDeps builds the client seam's service deps from the runtime, as
// Run built them: the brokers, the project, the reference finder, the
// access and interaction calls, the commands, the credential resolver,
// the model cache, the config reload and layer reload, the trust refresh,
// the home directory, the tools, the skill index, the file index, work
// when the provider is ready, and the turn calls (bindTurnDeps). ctx
// becomes Deps.Context. It sets no trust prompt: that belongs to the
// embedding.
func (rt *Runtime) ServiceDeps(ctx context.Context) service.Deps {
	state := rt.State
	database := must[*db.DB](rt.DB)
	projectID := rt.ProjectID
	jobBroker := must[*pubsub.Broker[native.JobEvent]](rt.JobBroker)
	watchBroker := must[*pubsub.Broker[watch.Event]](rt.WatchBroker)
	steeringBroker := must[*pubsub.Broker[session.SteeringEvent]](rt.SteeringBroker)
	workspaceBroker := must[*pubsub.Broker[session.WorkspaceEvent]](rt.WorkspaceBroker)
	subagentBroker := must[*pubsub.Broker[session.SubagentEvent]](rt.SubagentBroker)

	// The brokers exist whatever the provider's state, so the session's
	// events flow from the first frame: a start whose initial provider
	// build failed still repaints on workspace, subagent, job, watch and
	// steering events.
	// The seam serves work exactly when the runtime's runner is attached:
	// a start whose provider build failed serves no work either.
	providerReady := true
	if n, ok := state.Notice(); ok && n.Category == session.NoticeProvider {
		providerReady = false
	}
	// The seam answers Callers through the runtime's one LSP handle. A
	// runtime without one leaves the finder nil rather than assigning a
	// nil *lsp.SymbolQuery to the interface.
	var refFinder service.ReferenceFinder
	if rt.LSPManager != nil {
		refFinder = lsp.NewSymbolQuery(rt.LSPManager, nil, 0)
	}
	deps := service.Deps{
		State:           state,
		Context:         ctx,
		Jobs:            jobBroker,
		Watches:         watchBroker,
		Steering:        steeringBroker,
		Workspace:       workspaceBroker,
		Subagents:       subagentBroker,
		ProjectID:       projectID,
		ReferenceFinder: refFinder,
		AccessRequest:   rt.AccessRequest,
		ResolveAccess:   rt.ResolveAccess,
		SetInteraction:  rt.SetInteraction,
		SetEffort:       rt.SetSessionEffort,
		Commands:        rt.CommandRegistry,
		// The adapter builds every provider client and sign-in the TUI
		// asks for, so an injected resolver must reach it: otherwise a
		// probe or login under an isolated runtime would reach the real
		// OS keychain.
		Auth:          rt.AuthResolver,
		ModelCacheDir: rt.DataDir,
		// The config writes go through the seam, so the runtime's reload
		// path, the layer reloader and the trust refresh reach it here.
		ReloadConfig: rt.ConfigReloader,
		ReloadLayers: layerReloaderFor(rt.HomeDir, rt.WorkingDir, state.Trusted),
		TrustRefresh: trustRefreshFor(rt.HomeDir),
		HomeDir:      rt.HomeDir,
		Tools:        rt.ToolNames,
		// The skill index LoadSkill checks against: the runtime's own,
		// read under its lock as the legacy-session closure reads State,
		// so a swap keeps the same Deps working.
		SkillIndex: func() *skills.Index {
			rt.mu.Lock()
			defer rt.mu.Unlock()
			return rt.SkillIndex
		},
		// The @ completion popup's index is read through the session, so
		// the eager seed and the first @-keystroke share one path.
		FileIndex: func() ([]string, error) { return loadFileIndexPaths(database, projectID) },
	}
	// A session whose marshal client answers the trust question carries the
	// prompt and the in-place apply (P4 D8). Embedded mode sets TrustPrompt
	// itself afterwards and never sees ApplyTrust, because its runtimes are
	// not client-trust ones.
	if rt.clientTrust && rt.TrustPromptPending {
		deps.TrustPrompt = rt.WorkingDir
		deps.ApplyTrust = rt.applyClientTrust
	}
	if providerReady {
		deps.Work = rt
	}
	bindTurnDeps(&deps, rt)
	return deps
}

// trustRefreshFor returns the trust refresh the seam performs after a project
// save: it advances the permanent-trust record's config hash for dir.
// RefreshConfigHash is a no-op when the project has no permanent-trust record,
// so external or agent-made config edits (which never hit this path) still
// force re-trust.
func trustRefreshFor(homeDir string) func(dir string) {
	trustStoreDir := config.DataDir(homeDir)
	return func(dir string) {
		abs := trust.Canonicalize(dir)
		hash, err := trust.ConfigHashFor(dir)
		if err != nil {
			return
		}
		_ = trust.NewStore(trustStoreDir).RefreshConfigHash(abs, hash)
	}
}
