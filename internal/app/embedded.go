package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/provider"
	"marshal/internal/trust"
)

// Embedded runs embedded mode's runtimes: one at a time, over options
// resolved once, on one project-host registry.
type Embedded struct {
	// ctx is the context every runtime's config reload runs under.
	ctx  context.Context
	opts options

	homeDir    string
	workingDir string
	firstRun   bool

	// created is the host registry this Embedded built, nil when the caller
	// injected one. Close closes only what it created.
	created *Hosts

	// rt is the current runtime, nil between a close and the next Start.
	rt *Runtime

	// trustDecide records the user's folder-trust decision, and
	// reloadForTrust reports whether one asked for a reload.
	trustDecide    func(trust.Decision)
	reloadForTrust bool

	// workers tracks the OAuth refresh workers of every runtime started so
	// far. They are cancelled through their runtime's workCtx and reclaimed by
	// process exit, so nothing waits on this group.
	workers sync.WaitGroup
}

// NewEmbedded applies opts once, after embedded mode's defaults (the
// template-rebinding config loader and the deferred trust prompt), and
// resolves the credential resolver, the directories, first run and the
// host registry. ctx is the context every runtime's config reload runs
// under.
func NewEmbedded(ctx context.Context, opts ...Option) (*Embedded, error) {
	runOpts := options{
		now: time.Now,
		configWithLayersLoader: func(lo config.LoadOptions) (config.Config, config.Layers, error) {
			cfg, layers, err := config.LoadWithLayers(lo)
			if err != nil {
				return cfg, layers, err
			}
			// Re-apply provider template capability defaults (e.g. an
			// opencode-go template that gained TemperatureLocked after a
			// user saved their entry). Merge-by-key lets a stale false
			// clobber the template's true permanently; the template is
			// authoritative about what the endpoint accepts.
			provider.RebindTemplateDefaults(&cfg)
			provider.RebindTemplateDefaults(&layers.Merged)
			return cfg, layers, nil
		},
	}
	for _, opt := range opts {
		opt(&runOpts)
	}
	runOpts.deferTrustPrompt = true

	e := &Embedded{ctx: ctx}

	// Resolve the credential/OAuth dependency ONCE, at the outer construction
	// boundary: an explicitly nil injection is rejected here, and an omitted
	// one becomes the production default stored back on the options. Every
	// startRuntime iteration in the resume/restart loop therefore hands the
	// very same object to provider construction, the refresh workers and the
	// MCP managers.
	authResolver, err := resolveAuthResolver(runOpts)
	if err != nil {
		return nil, err
	}
	runOpts.authResolver = authResolver
	runOpts.authResolverSet = true

	workingDir, err := resolveWorkingDir(runOpts.workingDir)
	if err != nil {
		return nil, err
	}

	// Define configReloader before startRuntime so it can be wired into
	// native.Options. The closure captures e by reference; e.rt is set
	// immediately after startRuntime returns.
	runOpts.configReloader = func(newCfg config.Config) error {
		return reloadAgentRuntime(ctx, newCfg, e.rt)
	}

	homeDir := runOpts.homeDir
	if homeDir == "" {
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}

	// First-run detection: when no config exists yet, open the connect
	// panel in the TUI instead of running a separate onboarding wizard.
	firstRun := !config.HasConfig(config.LoadOptions{HomeDir: homeDir, WorkingDir: workingDir})
	trustStoreDir := config.DataDir(homeDir)
	e.trustDecide = func(d trust.Decision) {
		switch d {
		case trust.DecisionTrustPermanent:
			abs := trust.Canonicalize(workingDir)
			hash, _ := trust.ConfigHashFor(workingDir)
			_ = trust.NewStore(trustStoreDir).SetTrust(abs, true, hash)
			e.reloadForTrust = true
		case trust.DecisionTrustSession:
			e.opts.sessionTrusted = true
			e.reloadForTrust = true
		}
	}

	// One project-host registry for the embedded process. A resume or a trust
	// reload closes the runtime and starts the next one on the same host
	// inside its linger, so the database, index and language servers carry
	// over. Injected workers (WithWorker) replace the host's index work.
	if runOpts.hosts == nil {
		hosts := NewHosts()
		hosts.workers = runOpts.workers
		runOpts.hosts = hosts
		e.created = hosts
	}

	e.opts = runOpts
	e.homeDir = homeDir
	e.workingDir = workingDir
	e.firstRun = firstRun
	return e, nil
}

// WorkingDir is the directory the runtimes start in.
func (e *Embedded) WorkingDir() string { return e.workingDir }

// HomeDir is the home directory the runtime resolves its config under.
func (e *Embedded) HomeDir() string { return e.homeDir }

// FirstRun reports whether no config existed when the Embedded was built.
func (e *Embedded) FirstRun() bool { return e.firstRun }

// Start starts the next runtime: a new session, the one Resume named, or
// the same session reloaded after a trust decision. It adds the embedding
// warning, starts the OAuth refresh workers, marks the state able to raise
// a secret prompt and installs the exit-analysis operation.
func (e *Embedded) Start(ctx context.Context) (*Runtime, error) {
	rt, err := startRuntime(ctx, e.opts)
	if err != nil {
		return nil, err
	}
	e.rt = rt
	e.workingDir = rt.WorkingDir
	logger := rt.Logger

	logger.Info("marshal started", "project", rt.Config.Project.Name, "working_dir", rt.WorkingDir)

	// ── Worker lifecycle ──────────────────────────────────────────────
	// The index work runs on the project host, which startRuntime attached
	// this session to; the language servers are the host's too. Only the
	// OAuth refresh workers stay per runtime.
	if len(e.opts.workers) == 0 {
		// Surface a hint when embedding indexing is enabled but the
		// embedding route cannot resolve — semantic search reports
		// "unavailable" until it does. embeddingStartupWarning
		// distinguishes "nothing configured" from a preset that is
		// named but unresolvable (e.g. its provider entry vanished),
		// so the hint points at the actual problem.
		if msg, ok := embeddingStartupWarning(rt.Config); ok {
			logger.Warn("embedding route not resolvable while use_embeddings is enabled")
			rt.State.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
		}
	}
	for _, w := range buildOAuthWorkers(rt.Config, logger, rt.AuthResolver) {
		startWorker(rt.workCtx, &e.workers, w, logger)
	}
	// ── End worker lifecycle ──────────────────────────────────────────

	// The TUI can raise a masked secret prompt; ACP/web cannot, so
	// shell.sudo refuses there rather than blocking on a prompt no
	// transport can answer.
	rt.State.SetSecretPromptCapable(true)

	// Exit analysis (Task 8, step 5): the closure's optional postmortem pass
	// runs as a SCOPED exit-analysis operation. The runtime opens a dedicated
	// work epoch for it and stamps the authorization onto the context it
	// hands this operation, so the pass may start only during the postmortem
	// phase, its commands and children inherit the epoch, and the epoch is
	// stopped and joined before closure whatever the outcome. It never
	// reopens ordinary conversation work, and it cannot recursively trigger
	// the active-work warning.
	//
	// RunExitAnalysis owns the temporary Act + System grant and restores it
	// after the epoch's work is joined. The transport and model never grant
	// access themselves.
	rt.SetExitAnalysisOperation(func(analysisCtx context.Context, scope *session.ExitAnalysisScope, goal string) error {
		runner := rt.TurnRunner()
		if runner == nil {
			return fmt.Errorf("exit analysis: no agent runner")
		}
		return runner.Run(analysisCtx, goal)
	})

	return rt, nil
}

// SessionDeps is the current runtime's ServiceDeps plus the startup trust
// prompt and its decision.
func (e *Embedded) SessionDeps(ctx context.Context) service.Deps {
	deps := e.rt.ServiceDeps(ctx)
	// The seam carries the folder-trust prompt: the directory whose
	// project config waits for a decision, or "" when none does. The
	// trust panel asks through it, and a decision recorded here reloads
	// the runtime with the project config in force.
	if e.rt.TrustPromptPending {
		deps.TrustPrompt = e.rt.WorkingDir
	}
	deps.DecideTrust = e.trustDecide
	return deps
}

// Resume closes the current runtime; the next Start opens sessionID.
func (e *Embedded) Resume(sessionID string) {
	_ = e.rt.Close(context.Background())
	e.rt = nil
	e.opts.sessionID = ""
	e.opts.existingSessionID = sessionID
}

// TakeTrustReload reports whether a trust decision asked for a reload. If
// so, it closes the current runtime and clears the request; the next Start
// reloads with the decision in force.
func (e *Embedded) TakeTrustReload() bool {
	if !e.reloadForTrust {
		return false
	}
	// Trust granted inline: tear down quietly and reinitialise via the
	// same startRuntime path as startup — now with the project config in
	// force. No Quiesce: no agent work ever ran.
	e.reloadForTrust = false
	// Use Background: the user may have cancelled ctx during the trust
	// prompt; Close covers the final runtime, this covers reload
	// iterations.
	_ = e.rt.Close(context.Background())
	e.rt = nil
	return true
}

// Finish ends the current runtime as embedded mode's exit does: quiesce
// (bounded to one second), mark the session ended, the shutdown hook, the
// spill files, close. It returns progErr joined with the quiesce and close
// errors.
func (e *Embedded) Finish(progErr error) error {
	rt := e.rt
	if rt == nil {
		return progErr
	}
	database := must[*db.DB](rt.DB)

	// Phase 1: quiesce — cancel and join active work/jobs without
	// closing persistence so the session can be marked ended.
	quiesceCtx, cancelQuiesce := context.WithTimeout(context.Background(), 1*time.Second)
	quiesceErr := rt.Quiesce(quiesceCtx)
	cancelQuiesce()

	// Workers are cancelled via rt.workCtx (cancelled in Quiesce
	// above) and reclaimed by process exit — no need to wait.

	// Phase 2: mark the session ended while the DB is still open.
	if err := database.EndSession(rt.SessionID, e.opts.now(), ""); err != nil {
		rt.Logger.Warn("mark session ended failed", "error", err, "session_id", rt.SessionID)
	}
	if e.opts.shutdownHook != nil {
		e.opts.shutdownHook(context.Background(), rt.State, database)
	}

	// Clean up spill files from this session.
	spillDir := filepath.Join(rt.WorkingDir, ".marshal", "tool-results")
	if err := agent.CleanupSpillFiles(spillDir); err != nil {
		rt.Logger.Warn("spill file cleanup failed", "error", err)
	}

	// Phase 3: close — tear down MCP, brokers, snapshots, DB, logger.
	closeErr := rt.Close(context.Background())
	e.rt = nil
	return errors.Join(progErr, quiesceErr, closeErr)
}

// Close closes the current runtime, if any, then the host registry it
// created.
func (e *Embedded) Close(ctx context.Context) error {
	var err error
	if e.rt != nil {
		err = e.rt.Close(ctx)
		e.rt = nil
	}
	if e.created != nil {
		// The registry closes after the last runtime, bounded as Run bounded
		// it.
		closeCtx, cancel := context.WithTimeout(context.Background(), hostCloseTimeout)
		defer cancel()
		err = errors.Join(err, e.created.Close(closeCtx))
		e.created = nil
	}
	return err
}

// programRunner is the runner Run calls: the program runner the options
// carry.
func (e *Embedded) programRunner() ProgramRunner { return e.opts.programRunner }
