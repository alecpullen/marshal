package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/hooks"
	"marshal/internal/index"
	"marshal/internal/knowledge"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/lsp"
	"marshal/internal/pipeline"
	"marshal/internal/pubsub"
	"marshal/internal/repo"
	"marshal/internal/rollover"
	"marshal/internal/sandbox"
	"marshal/internal/sddauthor"
	"marshal/internal/skills"
	"marshal/internal/snapshot"
	"marshal/internal/tools/desktop"
	"marshal/internal/tools/desktop/browser"
	"marshal/internal/tools/mcp"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/trust"
	"marshal/internal/worker"
	"marshal/internal/worktree"
)

// ProgramResult is the value returned by a ProgramRunner. It carries the
// bubbletea program outcome plus an optional request to resume a different
// session.
type ProgramResult struct {
	Err           error
	ResumeSession string // non-empty => tear down and restart WithExistingSession
}

// ProgramRunner runs the TUI program. The default runner adapts tea.Program;
// tests inject a runner that returns a ProgramResult directly.
type ProgramRunner func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult
type configLoader func(config.LoadOptions) (config.Config, error)

// must asserts raw to T, panicking with a descriptive message on mismatch.
// A nil raw yields the zero T. It replaces per-type assert-and-panic blocks.
func must[T any](raw any) T {
	if raw == nil {
		var zero T
		return zero
	}
	v, ok := raw.(T)
	if !ok {
		panic(fmt.Sprintf("runtime: unexpected type %T", raw))
	}
	return v
}

type options struct {
	now           func() time.Time
	configLoader  configLoader
	layersLoader  func(config.LoadOptions) (config.Layers, error)
	programRunner ProgramRunner
	trustResolver trust.Resolver
	// deferTrustPrompt moves the folder-trust question into the TUI (the
	// interactive path). sessionTrusted carries an inline session-trust
	// answer across the reload loop.
	deferTrustPrompt  bool
	sessionTrusted    bool
	workingDir        string
	sessionID         string
	existingSessionID string
	additionalDirs    []string
	knowledgeHook     func(ctx context.Context, state *session.State, database *db.DB)
	workers           []worker.Worker
	configReloader    func(config.Config) error
}

type Option func(*options)

var (
	shutdownKnowledgeTimeout = 5 * time.Second

	// agentApprovalTimeout is the ceiling for approval/question requests on
	// interactive, swarm, and SDD runners. It bounds only the TUI wait, not
	// the model chat call itself — see agent.Runner.ChatTimeout, which is
	// left unset here so those runners get agent.Runner's own longer
	// default, appropriate for large-context cloud models.
	agentApprovalTimeout = 60 * time.Second
)

func WithNow(now func() time.Time) Option {
	return func(opts *options) {
		opts.now = now
	}
}

func WithProgramRunner(runner ProgramRunner) Option {
	return func(opts *options) {
		if runner == nil {
			return
		}
		opts.programRunner = runner
	}
}

func WithConfigLoader(loader configLoader) Option {
	return func(opts *options) {
		if loader == nil {
			return
		}
		opts.configLoader = loader
	}
}

func WithTrustResolver(r trust.Resolver) Option {
	return func(opts *options) {
		opts.trustResolver = r
	}
}

// withDeferTrustPrompt enables the inline trust flow in tests; Run sets it
// unconditionally in production.
func withDeferTrustPrompt() Option {
	return func(opts *options) { opts.deferTrustPrompt = true }
}

// WithWorkingDir overrides the working directory used for .marshal, the
// database, and config loading. The default is the process's current
// working directory. Intended for tests and headless transports (e.g. ACP)
// that bootstrap from a caller-supplied project path.
func WithWorkingDir(dir string) Option {
	return func(opts *options) {
		opts.workingDir = dir
	}
}

// WithKnowledgeHook registers a callback that is invoked after knowledge
// EndSession completes but before runtime Close. Tests use this hook to
// observe session state and database availability during the knowledge
// finalization window.
func WithKnowledgeHook(hook func(ctx context.Context, state *session.State, database *db.DB)) Option {
	return func(opts *options) {
		opts.knowledgeHook = hook
	}
}

// WithWorker registers a background worker started shutdown-aware by Run.
// Primarily a test seam; production wiring constructs the index watcher.
func WithWorker(w worker.Worker) Option {
	return func(o *options) { o.workers = append(o.workers, w) }
}

// startWorker launches a worker goroutine bound to ctx, tracked by wg.
func startWorker(ctx context.Context, wg *sync.WaitGroup, w worker.Worker, log *slog.Logger) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := w.Run(ctx); err != nil {
			log.Warn("worker exited", "worker", w.Name(), "err", err)
		}
	}()
}

// WithSessionID pins the session identifier used when the runtime creates a
// new database session. When empty, StartRuntime generates a sess_<unixnano>
// id. Useful for headless transports that want a stable id derived from
// the client (e.g. ACP session id).
func WithSessionID(id string) Option {
	return func(opts *options) {
		opts.sessionID = id
	}
}

// WithExistingSession loads an existing agent session instead of creating a
// new one. The runtime will open the database, locate the project by the
// working directory, verify that the session belongs to that project, load
// the persisted transcript, and abort startup if the state reports a
// LoadError. WithExistingSession is mutually exclusive with WithSessionID.
func WithExistingSession(id string) Option {
	return func(opts *options) {
		opts.existingSessionID = id
	}
}

func WithAdditionalDirectories(dirs []string) Option {
	return func(opts *options) {
		if len(dirs) == 0 {
			return
		}
		opts.additionalDirs = append(opts.additionalDirs, dirs...)
	}
}

func logPath(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "marshal.log")
}

type routedProviderResolver struct {
	router    *routing.StaticRouter
	cfg       config.Config
	dataDir   string
	mu        sync.Mutex // guards providers; swarm may resolve roles from concurrent paths
	providers map[string]provider.Provider
}

// dbMemoryProvider adapts stored project memories for context-pack
// injection, excluding memories that have been marked stale.
type dbMemoryProvider struct {
	db *db.DB
}

func newRoutedProviderResolver(cfg config.Config, dataDir string) *routedProviderResolver {
	return &routedProviderResolver{
		router:    routing.NewStaticRouter(cfg.RoutingConfig()),
		cfg:       cfg,
		dataDir:   dataDir,
		providers: make(map[string]provider.Provider),
	}
}

// rolloverRunnerAdapter wraps a native.CommandRunner so it satisfies
// rollover.CommandRunner. The two interfaces are structurally identical in
// spirit but use different request/result types because internal/rollover
// must not import internal/tools/native (see filesstate.go for the import
// cycle rationale). Only the fields FilesState actually uses are mapped;
// any io.Writer / callback fields on native.CommandRequest are dropped here
// because FilesState never sets them.
type rolloverRunnerAdapter struct {
	inner native.CommandRunner
}

func newRolloverRunnerAdapter(inner native.CommandRunner) *rolloverRunnerAdapter {
	return &rolloverRunnerAdapter{inner: inner}
}

func (a *rolloverRunnerAdapter) Run(ctx context.Context, req rollover.CommandRequest) (rollover.CommandResult, error) {
	res, err := a.inner.Run(ctx, native.CommandRequest{
		Command: req.Command,
		Dir:     req.Dir,
		Timeout: req.Timeout,
	})
	return rollover.CommandResult{
		Stdout:   res.Stdout,
		ExitCode: res.ExitCode,
	}, err
}

func (r *routedProviderResolver) Resolve(class string) (routing.Route, provider.Provider, error) {
	route, err := r.router.Resolve(class)
	if err != nil {
		return routing.Route{}, nil, err
	}
	p, err := r.providerFor(route)
	if err != nil {
		return routing.Route{}, nil, err
	}
	return route, p, nil
}

// ResolveRole is Resolve for an explicit swarm role instead of a task class.
func (r *routedProviderResolver) ResolveRole(role routing.AgentRole) (routing.Route, provider.Provider, error) {
	route, err := r.router.ResolveRole(role)
	if err != nil {
		return routing.Route{}, nil, err
	}
	p, err := r.providerFor(route)
	if err != nil {
		return routing.Route{}, nil, err
	}
	return route, p, nil
}

func (r *routedProviderResolver) providerFor(route routing.Route) (provider.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.providers[route.Preset.Provider]; ok {
		return existing, nil
	}
	providerConfig, ok := r.cfg.Providers[route.Preset.Provider]
	if !ok {
		return nil, fmt.Errorf("routing provider %q is not configured", route.Preset.Provider)
	}
	p, err := provider.NewFromConfig(route.Preset.Provider, providerConfig, r.dataDir, r.cfg.Privacy.RemoteLimitDiscovery)
	if err != nil {
		return nil, err
	}
	r.providers[route.Preset.Provider] = p
	return p, nil
}

func (p *dbMemoryProvider) Memories(projectID int64) ([]contextpack.MemoryNote, error) {
	memories, err := p.db.GetMemories(projectID)
	if err != nil {
		return nil, err
	}
	notes := make([]contextpack.MemoryNote, 0, len(memories))
	for _, m := range memories {
		if m.Confidence == db.MemoryConfidenceStale {
			continue
		}
		notes = append(notes, contextpack.MemoryNote{Kind: m.Kind, Content: m.Content})
	}
	return notes, nil
}

// metricsRecorder returns a MetricsObserver that persists each turn's
// metrics. Failures are logged and swallowed: telemetry must never break a
// turn.
func metricsRecorder(database *db.DB, projectID int64, sessionID string, logger *slog.Logger) func(agent.TurnMetrics) {
	return func(m agent.TurnMetrics) {
		_, err := database.InsertTurnMetrics(db.TurnMetricsRow{
			ProjectID:        projectID,
			SessionID:        sessionID,
			StartedAt:        m.StartedAt,
			DurationMs:       m.DurationMs,
			Class:            m.Class,
			Role:             m.Role,
			Provider:         m.Provider,
			Model:            m.Model,
			Goal:             m.Goal,
			Iterations:       m.Iterations,
			ToolCalls:        m.ToolCalls,
			ToolErrors:       m.ToolErrors,
			CacheHits:        m.CacheHits,
			ParseFailures:    m.ParseFailures,
			HardStalls:       m.HardStalls,
			Outcome:          m.Outcome,
			SalvageReason:    m.SalvageReason,
			PromptTokens:     m.PromptTokens,
			CompletionTokens: m.CompletionTokens,
		})
		if err != nil && logger != nil {
			logger.Warn("failed to persist turn metrics", "error", err)
		}
	}
}

// rolloverPolicyFromConfig translates a config.RolloverConfig into a
// rollover.Policy. An empty or "auto" policy string defaults to
// context_percent mode. Unknown policy strings are passed through unchanged.
func rolloverPolicyFromConfig(cfg config.RolloverConfig) rollover.Policy {
	pol := rollover.Policy{
		ContextPercent: cfg.ContextPercentThreshold,
		TurnCount:      cfg.TurnCountThreshold,
	}
	if cfg.Policy == "" || cfg.Policy == "auto" {
		pol.Mode = "context_percent"
		return pol
	}
	pol.Mode = cfg.Policy
	return pol
}

// minimalDigestProvider implements rollover.DigestProvider by returning a
// minimal placeholder digest. It is used as a safe fallback when no LLM-based
// digest provider is configured, ensuring Controller.Digest is never nil.
type minimalDigestProvider struct{}

func (minimalDigestProvider) Digest(_ context.Context, h rollover.GenerationHandle) (string, string, error) {
	return rollover.MinimalDigest(h.Seq), rollover.SourceMinimal, nil
}

// NewRolloverController creates a rollover.Controller from config. When
// rollover is disabled, it returns nil, nil (no generation rows are created).
// modelContextWindow is the model's full context window in tokens; when >0,
// the controller uses it for context_percent calculations instead of the
// per-turn compaction budget.
// digestProvider is the DigestProvider to use; when nil, minimalDigestProvider
// is used as a safe fallback. usageCounter, when non-nil, is passed to
// ResolveCounter so the "usage" counter can observe provider-reported tokens.
func NewRolloverController(sessionID string, cfg config.RolloverConfig, database *db.DB, modelContextWindow int, digestProvider rollover.DigestProvider, usageCounter *rollover.UsageCounter) (*rollover.Controller, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	pol := rolloverPolicyFromConfig(cfg)
	counter := rollover.ResolveCounter(cfg.TokenCounter, usageCounter)
	digest := digestProvider
	if digest == nil {
		digest = minimalDigestProvider{}
	}
	ctrl := &rollover.Controller{
		SessionID:          sessionID,
		Store:              database,
		Counter:            counter,
		Digest:             digest,
		Policy:             pol,
		BlobThreshold:      cfg.BlobThresholdBytes,
		ModelContextWindow: modelContextWindow,
		Now:                time.Now,
		NewID:              func() string { return uuid.New().String() },
	}
	return ctrl, nil
}

func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, dataDir string, additionalDirs []string, jobBroker *pubsub.Broker[native.JobEvent], configReloader func(config.Config) error, homeDir string) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Rooted, *native.JobManager, func(), agent.SubagentRunnerFactory, *lsp.Handle, func(planPath string) tui.AgentRunner, sddauthor.Factory, error) {
	resolver := newRoutedProviderResolver(cfg, dataDir)
	route, resolvedProvider, err := resolver.Resolve("edit")
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
	}

	reg := registry.New()

	// Milestone Q: construct the sandboxed command runner from the shell
	// config. sandbox.New falls back gracefully (container -> restricted
	// when no runtime is detected), so app startup never hard-fails because
	// of a missing sandbox dependency. A nil sandbox leaves the default
	// non-sandboxed execRunner in place (used by tests).
	sbCfg := sandbox.FromConfig(cfg.Tools.Shell)
	commandRunner, sbErr := sandbox.New(sbCfg, state.Logger())
	if sbErr != nil {
		// Unknown backend string: surface as a startup error rather than
		// silently downgrading — the user should fix their config.
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("build sandbox: %w", sbErr)
	}
	caps := commandRunner.Capabilities()
	state.SetSandboxInfo(session.SandboxInfo{
		Backend:          caps.Backend,
		NetworkIsolation: caps.NetworkIsolation,
	})

	// Milestone Q: construct the JobManager from the sandboxed command
	// runner so background jobs honour the configured sandbox backend,
	// output limit, and concurrency limits.
	jobManager := native.NewJobManager(
		ctx,
		commandRunner,
		state.WorkingDir,
		cfg.Tools.Shell.MaxBackgroundJobs,
		cfg.Tools.Shell.BackgroundRetention,
		cfg.Tools.Shell.MaxOutputBytes,
	)
	// Background jobs start in the session's active root, so they land in
	// the worktree while the session is isolated.
	jobManager.SetDirFunc(func() string { return state.Workspace().ActiveRoot })
	// Roll back partially-built resources on any later failure. Each
	// resource appends its cleanup as it comes up; the deferred func runs
	// them in reverse order, but only when a failure return set buildErr.
	// This replaces per-branch Close calls that leaked the MCP manager
	// when a registration after mcpMgr.Start failed.
	var buildErr error
	var cleanup []func()
	defer func() {
		if buildErr == nil {
			return
		}
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
	}()
	cleanup = append(cleanup, func() {
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		jobManager.Shutdown(sc)
	})

	var fileTracker native.FileTracker
	if database != nil {
		fileTracker = filetrack.New(database.SQLDB(), state.SessionID())
	}

	pol := policy.NewEngine(&cfg, state.SessionRules())
	if state.Logger() != nil {
		pol.SetLogger(state.Logger())
	}
	pol.WithRegistry(reg)
	pol.SetApprovalMode(parseApprovalMode(cfg.Agent.ApprovalMode))

	nativeOpts := native.Options{
		WorkspaceRoot:  state.WorkingDir,
		CommandRunner:  commandRunner,
		TestCommand:    cfg.Commands.Test,
		MaxOutputBytes: cfg.Tools.Shell.MaxOutputBytes,
		SessionState:   state,
		DB:             database,
		ProjectID:      projectID,
		FileTracker:    fileTracker,
		Config:         cfg,
		JobManager:     jobManager,
		JobBroker:      jobBroker,
		Guardrail:      func(cmd string) error { return pol.GuardrailCheck(cmd) },
		ConfigPath:     config.ProjectConfigPath(state.WorkingDir),
		UserConfigPath: config.UserConfigPath(homeDir),
		ConfigReloader: configReloader,
	}
	if len(additionalDirs) > 0 {
		nativeOpts.AdditionalRoots = additionalDirs
	}
	// Build LSP manager and wire adapters BEFORE RegisterAll so the
	// toolSet and diagnostics checker receive non-nil LSP fields.
	var lspHandle *lsp.Handle
	if lspEnabled(cfg.LSP) {
		servers := lsp.DetectServers(toServerSpecs(cfg.LSP.Servers), disabledLangs(cfg.LSP.Servers))
		if len(servers) > 0 {
			lspHandle = lsp.NewHandle(lsp.NewManager(state.Workspace().ActiveRoot, servers, state.Logger()), servers, state.Logger())
		}
	}
	if lspHandle != nil {
		nativeOpts.LSP = lsp.NewQueryAdapter(lspHandle)
		nativeOpts.LSPSource = lsp.NewDiagnosticsAdapter(lspHandle)
		nativeOpts.LSPIndex = lsp.NewSymbolAdapter(lspHandle)
	}
	if err := native.RegisterAll(reg, nativeOpts); err != nil {
		buildErr = err
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
	}

	skills.RegisterTool(reg, skillIndex, state)

	var mcpMgr *mcp.Manager
	if len(cfg.MCP.Servers) > 0 {
		mcpMgr = mcp.NewManager(&cfg, mcp.WithManagerLogger(state.Logger()))
		if err := mcpMgr.Start(ctx); err != nil {
			buildErr = err
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
		}
		cleanup = append(cleanup, func() { _ = mcpMgr.Close() })
		if err := mcpMgr.RegisterTools(reg); err != nil {
			buildErr = err
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
		}
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	if err := reg.Register(agent.NewSubagentTool(
		buildSubagentFactory(cfg, state, resolvedProvider, reg, pol, route.Preset.Model, router, database, projectID),
		reg,
		state,
	)); err != nil {
		buildErr = err
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.run: %w", err)
	}
	runner := agent.NewRunner(resolvedProvider, reg, pol, state, route.Preset.Model)
	runner.SkillIndex = skillIndex
	runner.RouteResolver = resolver
	runner.MemoryProvider = &dbMemoryProvider{db: database}
	runner.ProjectID = projectID
	runner.MetricsObserver = metricsRecorder(database, projectID, state.SessionID(), state.Logger())
	runner.Pricing = pricing.Lookup(route.Preset)

	// Give the runner the merged limit table so a preset with no explicit
	// context_window still resolves a real window instead of falling
	// straight through to the eleven-entry local catalog. Cache-only unless
	// remote limit discovery is enabled; a missing cache leaves it nil and
	// resolution degrades gracefully.
	if dataDir != "" {
		if cfg.Privacy.RemoteLimitDiscovery {
			if t, err := limits.LoadTable(ctx, dataDir, limits.DefaultTTL); err == nil {
				runner.LimitsTable = &t
			}
		} else if c, err := limits.Load(dataDir); err == nil && len(c.Table) > 0 {
			t := limits.NewTable(c.Table)
			runner.LimitsTable = &t
		}
	}

	// Defensive: align runner mode with policy engine in case the runner
	// ever copies the engine instead of sharing it.
	if runner.Policy != nil && runner.Policy.ApprovalMode() != parseApprovalMode(cfg.Agent.ApprovalMode) {
		runner.SetApprovalMode(parseApprovalMode(cfg.Agent.ApprovalMode))
	}

	// T17: wire UsageCounter so the rollover controller can use
	// provider-reported prompt_tokens as the numerator for context_percent
	// (branch review finding #2). Create the counter before the UsageObserver
	// so the observer can feed it.
	var usageCounter *rollover.UsageCounter
	if cfg.Session.Rollover.Enabled && cfg.Session.Rollover.TokenCounter == "usage" {
		usageCounter = rollover.NewUsageCounter()
	}
	runner.UsageObserver = func(usage schema.TokenUsage) {
		state.SetTurnUsage(usage.PromptTokens + usage.CompletionTokens)
		if usageCounter != nil {
			usageCounter.Observe(usage.PromptTokens)
		}
	}

	// Calibration harness: when enabled, record a paired estimator-vs-provider
	// observation for every turn that reports provider usage. The insert is
	// best-effort — telemetry never breaks a turn.
	if cfg.Session.Rollover.Calibration.Enabled {
		estCounter := rollover.EstimatorCounter{}
		prov := route.Preset.Provider
		model := route.Preset.Model
		sid := state.SessionID()
		runner.CalibrationObserver = func(wire []schema.ChatMessage, promptTokens int) {
			est, err := estCounter.CountTokens(context.Background(), wire)
			if err != nil {
				return
			}
			if _, err := database.InsertCalibrationSample(db.CalibrationSample{
				ProjectID:       projectID,
				SessionID:       sid,
				Provider:        prov,
				Model:           model,
				EstimatorTokens: est,
				ProviderTokens:  promptTokens,
				CreatedAt:       time.Now(),
			}); err != nil && state.Logger() != nil {
				state.Logger().Warn("calibration sample insert failed", "error", err)
			}
		}
	}

	// T17: set DigestModel on the runner so DigestChat can use a different
	// model for digest generation than the primary turn model.
	if cfg.Session.Rollover.DigestModel != "" {
		runner.DigestModel = cfg.Session.Rollover.DigestModel
	}

	decoding := resolveActionDecoding(route.Preset.ToolCalling, resolvedProvider.Capabilities(ctx))
	runner.NativeTools = decoding.Native
	runner.ResponseFormat = decoding.ResponseFormat
	if cfg.Agent.MaxToolIterations > 0 {
		runner.MaxToolIterations = cfg.Agent.MaxToolIterations
	}
	if cfg.Agent.MaxRetries > 0 {
		runner.MaxRetries = cfg.Agent.MaxRetries
	}
	if cfg.Agent.MaxTurnContextTokens > 0 {
		runner.MaxTurnContextTokens = cfg.Agent.MaxTurnContextTokens
	}
	if cfg.Agent.MaxToolResultChars > 0 {
		runner.MaxToolResultChars = cfg.Agent.MaxToolResultChars
	}
	runner.PlanFirst = cfg.Agent.PlanFirst
	runner.SuppressParseRepairFeedback = !cfg.Agent.ParseRepairFeedbackEnabled()
	if runner.ApprovalTimeout == 0 {
		runner.ApprovalTimeout = agentApprovalTimeout
	}

	// T17: wire rollover controller into the runner when enabled.
	// Pass the model's full context window so context_percent fires against
	// the correct denominator (branch review finding #7).
	// Use LLMSummaryProvider as the primary digest provider, falling back to
	// minimalDigestProvider only when the runner's provider is not available
	// (branch review finding #1).
	modelCtxWindow, _ := agent.ResolveModelLimits(route.Preset, runner.LimitsTable)
	var digestProvider rollover.DigestProvider
	switch cfg.Session.Rollover.DigestProvider {
	case "files":
		// Structured digest from on-disk state: zero LLM cost. Uses the
		// same sandboxed CommandRunner as the native git tool so it
		// inherits the workspace's command policy.
		// rollover.FilesState needs its own narrow CommandRunner interface
		// (plain Go types) so internal/rollover can avoid importing
		// internal/tools/native and breaking the import cycle. Wrap the
		// sandboxed native.CommandRunner in a tiny adapter that maps
		// native.CommandRequest/Result to rollover.CommandRequest/Result.
		rolloverRunner := newRolloverRunnerAdapter(commandRunner)
		fileState := rollover.NewFilesState(database.SQLDB(), state.SessionID(), rolloverRunner, state.WorkingDir)
		digestProvider = rollover.NewFilesDigestProvider(fileState)
	case "minimal":
		digestProvider = minimalDigestProvider{}
	default: // "llm_summary" and any unset value
		if runner.Provider != nil {
			digestProvider = rollover.NewLLMSummaryProvider(runner, rollover.SummaryDirective)
		}
	}
	if rolloverCtrl, rerr := NewRolloverController(state.SessionID(), cfg.Session.Rollover, database, modelCtxWindow, digestProvider, usageCounter); rerr != nil {
		buildErr = rerr
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("new rollover controller: %w", rerr)
	} else if rolloverCtrl != nil {
		runner.Rollover = &agent.Rollover{
			Controller: rolloverCtrl,
			State:      state,
		}
		// Start generation 0.
		if err := rolloverCtrl.Start(ctx); err != nil {
			buildErr = err
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("rollover start: %w", err)
		}
		// Record generation 0 in session state.
		genID, genSeq, genSeed := rolloverCtrl.Current()
		state.BeginGeneration(genID, genSeq, genSeed)
	}

	var snapSvc *snapshot.Rooted
	if dataDir != "" && cfg.Snapshots.Enabled {
		snapSvc = snapshot.NewRooted(dataDir, state.WorkingDir,
			func() string { return state.Workspace().ActiveRoot },
			int64(cfg.Snapshots.MaxFileBytes), cfg.Indexing.Ignore, state.Logger())
		state.SetSnapshotter(snapSvc)
		runner.Snapshotter = snapSvc
		runner.SnapshotRecorder = database
	}

	state.SetActiveRoute(session.RouteInfo{
		Role:      route.Role,
		Profile:   route.Profile,
		Preset:    route.Preset.Name,
		Provider:  route.Preset.Provider,
		Model:     route.Preset.Model,
		LocalOnly: route.Preset.LocalOnly,
		Active:    true,
	})

	// F13: wire the fire-and-forget title generator. Route through the title
	// role (falls back to implementer preset when unconfigured), but skip the
	// generator if it would target the same provider+model as the active turn
	// route — a single-model local backend cannot serve two concurrent calls.
	if titleRoute, titleProvider, titleErr := resolver.ResolveRole(routing.RoleTitle); titleErr == nil && titleRoute.Preset.Model != "" {
		if titleRoute.Preset.Provider == route.Preset.Provider && titleRoute.Preset.Model == route.Preset.Model {
			runner.TitleGenerator = nil
		} else if titleProvider != nil {
			runner.TitleGenerator = agent.NewTitleGenerator(titleProvider, titleRoute.Preset.Model, state)
		}
	}
	swarmRunner := buildSwarmRunner(cfg, state, reg, pol, resolver, database, projectID, skillIndex)

	var desktopCloser func()
	if cfg.Desktop.Enabled {
		desktopOpts := desktop.Options{
			Config: cfg.Desktop,
			BackendFactory: func() (browser.BrowserBackend, error) {
				return newDesktopBackend(cfg.Desktop)
			},
			SessionState: state,
		}
		closer, err := desktop.RegisterAll(reg, desktopOpts)
		if err != nil {
			buildErr = err
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register desktop tools: %w", err)
		}
		desktopCloser = closer
	}

	subagentFactory := buildSubagentFactory(cfg, state, resolvedProvider, reg, pol, route.Preset.Model, router, database, projectID)
	pipelineFactory := func(planPath string) tui.AgentRunner {
		return buildPipelineController(cfg, state, reg, pol, resolver, database, projectID, skillIndex, commandRunner, planPath)
	}
	planAuthorFactory := buildPlanAuthorFactory(cfg, state, reg, pol, resolver, database, projectID, skillIndex, commandRunner)
	return runner, reg, swarmRunner, mcpMgr, snapSvc, jobManager, desktopCloser, subagentFactory, lspHandle, pipelineFactory, planAuthorFactory, nil
}

// roleRunnerSpec holds the dependencies shared by the swarm and SDD
// role-runner factories. The final three fields are the intentional
// differences between the two orchestrators; everything else is shared.
type roleRunnerSpec struct {
	cfg         config.Config
	state       *session.State
	pol         *policy.PolicyEngine
	resolver    *routedProviderResolver
	reg         *registry.Registry
	readOnlyReg *registry.Registry
	testerReg   *registry.Registry // nil for SDD: ScopeTester falls back to reg
	skillIndex  *skills.Index
	memory      *dbMemoryProvider
	projectID   int64

	writeGate        agent.WriteGate         // swarm only; nil for SDD
	metricsObserver  func(agent.TurnMetrics) // swarm only; nil for SDD
	applyAgentLimits bool                    // swarm copies retries/ctx tokens/plan-first
	childSession     bool                    // SDD pipeline: each role runner gets a fresh child session
}

// newRunner builds one role runner. It satisfies swarm.RunnerFactory (and,
// by alias, pipeline.RunnerFactory).
func (s roleRunnerSpec) newRunner(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
	route, p, err := s.resolver.ResolveRole(role)
	if err != nil {
		return nil, err
	}
	toolReg := s.reg
	switch scope {
	case swarm.ScopeReadOnly:
		toolReg = s.readOnlyReg
	case swarm.ScopeTester:
		if s.testerReg != nil {
			toolReg = s.testerReg
		}
	}
	// Pipeline role runners stream into a fresh child session so their turn
	// and tool noise stays out of the orchestrator transcript; the parent
	// shows a drillable summary card instead. Depth is parent+1 so any
	// nested subagent attempt is rejected by the child's own depth guard.
	runnerState := s.state
	pol := s.pol
	if s.childSession {
		runnerState = session.New(s.state.Config, s.state.WorkingDir, time.Now(), session.Persistence{}, session.WithDepth(s.state.SubagentDepth()+1))
		// No UI watches a child session's pending approvals: an interactive
		// Confirm would wait out ApprovalTimeout and fail the turn with
		// "agent: request timed out". Unattended runners therefore evaluate
		// under an auto-approving clone of the shared engine (guardrails and
		// the git-push floor still apply), leaving the parent mode intact.
		pol = s.pol.Clone()
		pol.SetApprovalMode(policy.ModeAuto)
	}
	r := agent.NewRunner(p, toolReg, pol, runnerState, route.Preset.Model)
	r.Role = role
	r.WriteGate = s.writeGate
	r.SkillIndex = s.skillIndex
	r.MemoryProvider = s.memory
	r.ProjectID = s.projectID
	r.MetricsObserver = s.metricsObserver
	r.ApprovalTimeout = agentApprovalTimeout
	// Role prompts embed the shared plan, so skip the per-turn
	// classify/plan pass (class "question" bypasses planning).
	r.SetForceClass("question")
	decoding := resolveActionDecoding(route.Preset.ToolCalling, p.Capabilities(context.Background()))
	r.NativeTools = decoding.Native
	r.ResponseFormat = decoding.ResponseFormat
	if cap := roleToolIterations(s.cfg, role); cap > 0 {
		r.MaxToolIterations = cap
	}
	if s.applyAgentLimits {
		if s.cfg.Agent.MaxRetries > 0 {
			r.MaxRetries = s.cfg.Agent.MaxRetries
		}
		if s.cfg.Agent.MaxTurnContextTokens > 0 {
			r.MaxTurnContextTokens = s.cfg.Agent.MaxTurnContextTokens
		}
		if s.cfg.Agent.MaxToolResultChars > 0 {
			r.MaxToolResultChars = s.cfg.Agent.MaxToolResultChars
		}
		r.PlanFirst = s.cfg.Agent.PlanFirst
	}
	r.Pricing = pricing.Lookup(route.Preset) // closes role-runner pricing gap
	repoInstructions, _ := loadRepoInstructions(s.state.WorkingDir)
	var customAddendum string
	if route.CustomAgent != nil {
		ca := route.CustomAgent
		customAddendum = ca.SystemPrompt
		if len(ca.ToolDenylist) > 0 {
			r.Registry = agent.DenylistView(r.Registry, ca.ToolDenylist)
		}
		if ca.ApprovalMode != "" {
			r.SetApprovalMode(parseApprovalMode(ca.ApprovalMode))
		}
		if ca.MaxIterations > 0 {
			r.MaxToolIterations = ca.MaxIterations
		}
	}
	r.SystemPromptAddendum = composeAddendum(repoInstructions, customAddendum)
	return r, nil
}

// buildSwarmRunner wires the Milestone O swarm: every role runner shares
// the session state, policy engine, and one WriteLock; read-only roles get
// the filtered registry view; each role's provider/model comes from the
// routing profile via ResolveRole (falling back to the implementer preset
// for unconfigured roles).
func buildSwarmRunner(cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index) *swarm.Orchestrator {
	spec := roleRunnerSpec{
		cfg:         cfg,
		state:       state,
		pol:         pol,
		resolver:    resolver,
		reg:         reg,
		readOnlyReg: registry.ReadOnlyView(reg),
		testerReg:   registry.TesterView(reg),
		skillIndex:  skillIndex,
		memory:      &dbMemoryProvider{db: database},
		projectID:   projectID,

		writeGate:        &swarm.WriteLock{},
		metricsObserver:  metricsRecorder(database, projectID, state.SessionID(), state.Logger()),
		applyAgentLimits: true,
	}
	o := swarm.New(state, spec.newRunner)
	o.MaxFixRounds = cfg.Swarm.Budget.MaxFixRounds
	o.MaxTotalTokens = cfg.Swarm.Budget.MaxTotalTokens
	return o
}

// buildPipelineController wires a plan-execution controller for one plan.
// It is built per run, not at startup: the controller is bound to a single
// plan file.
func buildPipelineController(cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index, commandRunner native.CommandRunner, planPath string) *pipeline.ControllerAdapter {
	spec := roleRunnerSpec{
		cfg:         cfg,
		state:       state,
		pol:         pol,
		resolver:    resolver,
		reg:         reg,
		readOnlyReg: registry.ReadOnlyView(reg),
		skillIndex:  skillIndex,
		memory:      &dbMemoryProvider{db: database},
		projectID:   projectID,

		childSession: true,
	}
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		return spec.newRunner(role, scope)
	}

	var adapter *pipeline.ControllerAdapter
	c, err := pipeline.NewController(pipeline.ControllerOpts{
		PlanPath: planPath,
		RepoRoot: state.WorkingDir,
		Git:      worktree.CLIGitOps{},
		Dispatch: pipeline.Dispatcher{
			Factory: factory,
			State:   state,
			OnTokens: func(n int) {
				if adapter == nil {
					return
				}
				ctl := adapter.Controller()
				ctl.UsageTokens += n
				state.UpdateSDDTokens(ctl.UsageTokens, cfg.SDD.MaxTotalTokens)
			},
			RegistryFactory: makePipelineRegistryFactory(cfg, state, commandRunner, resolver, database, projectID, skillIndex, reg),
		},
		Verifier: pipeline.DefaultVerifier(
			state.WorkingDir,
			cfg.SDD.Verify.Build,
			cfg.SDD.Verify.Test,
			time.Duration(cfg.SDD.VerifyTimeoutMS)*time.Millisecond,
		),
		MaxFixRounds:       cfg.SDD.MaxFixRounds,
		MaxDispatchRetries: cfg.SDD.DispatchRetries,
		AutoEscalate:       parseApprovalMode(cfg.Agent.ApprovalMode) == policy.ModeAuto,
		TargetBranch:       "main",
		MaxTokensCfg:       cfg.SDD.MaxTotalTokens,
	})
	if err != nil {
		state.Logger().Warn("pipeline: controller construction failed", "error", err)
		state.AddMessage(session.RoleSystem, fmt.Sprintf("Cannot start plan run: %v", err), session.ContentTypePlain)
		return nil
	}
	c.RunStore = pipeline.NewRunStore(c.Paths)
	adapter = pipeline.NewControllerAdapter(c, state)
	return adapter
}

// resolveAuthorPlansDir returns the canonical absolute path of the SDD
// plans directory under the repository working dir, walking up to the
// nearest existing ancestor so non-existent plans directories
// canonicalize into the same namespace as the repository root. A
// symlinked plans_dir that points outside the repository resolves into a
// different directory and is rejected by sddplans.DraftPath at authoring
// time.
func resolveAuthorPlansDir(workingDir, plansDirRel string) string {
	return repo.Canonical(filepath.Join(workingDir, plansDirRel))
}

// buildPlanAuthorFactory returns a factory that builds a scoped SDD
// plan-authoring runner for one request. The child may inspect the
// repository and write exactly one plan artifact; it cannot modify source
// files, run commands, spawn agents, or ask the user.
func buildPlanAuthorFactory(cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index, commandRunner native.CommandRunner) sddauthor.Factory {
	return func(req sddauthor.Request) (*sddauthor.Runner, error) {
		route, p, err := resolver.ResolveRole(routing.RoleSDDPlanAuthor)
		if err != nil {
			return nil, err
		}
		// A fresh child session keeps the authoring turn's tool noise out of
		// the parent transcript. Depth is parent+1 so any nested subagent
		// attempt is rejected by the child's own depth guard.
		childState := session.New(cfg, state.WorkingDir, time.Now(), session.Persistence{}, session.WithDepth(state.SubagentDepth()+1))
		childPol := pol.Clone()
		childPol.SetApprovalMode(policy.ModeAuto)

		// A fresh native registry rooted at the working directory, with @plan
		// aliasing the resolved plans directory so the child can write the
		// plan artifact without exposing absolute paths and so a symlinked
		// plans_dir cannot trick the child into writing outside the repo.
		childReg := registry.New()
		plansDir := resolveAuthorPlansDir(state.WorkingDir, cfg.SDD.PlansDir)
		nativeOpts := native.Options{
			WorkspaceRoot:  state.WorkingDir,
			CommandRunner:  commandRunner,
			MaxOutputBytes: cfg.Tools.Shell.MaxOutputBytes,
			SessionState:   childState,
			Config:         cfg,
			NamedRoots:     map[string]string{"@plan": plansDir},
		}
		if err := native.RegisterAll(childReg, nativeOpts); err != nil {
			return nil, fmt.Errorf("plan author registry: register: %w", err)
		}
		// Restrict the child to read-only tools plus the exact candidate plan
		// artifact. No skills.register, question, agent.run, or command tools.
		allowed := []string{"@plan/" + filepath.Base(req.PlanPath)}
		childReg = registry.PlanWriterView(childReg, "@plan", allowed)

		// Load the built-in authoring skill quietly into the child context.
		if err := skills.LoadSkillIntoSessionQuiet(skillIndex, childState, "marshal-sdd-plan-authoring"); err != nil {
			return nil, fmt.Errorf("plan author skill: %w", err)
		}

		childRunner := agent.NewRunner(p, childReg, childPol, childState, route.Preset.Model)
		childRunner.Role = agent.RoleSDDPlanAuthor
		childRunner.SkillIndex = skillIndex
		childRunner.MemoryProvider = &dbMemoryProvider{db: database}
		childRunner.ProjectID = projectID
		childRunner.ApprovalTimeout = agentApprovalTimeout
		childRunner.SetForceClass("question")
		decoding := resolveActionDecoding(route.Preset.ToolCalling, p.Capabilities(context.Background()))
		childRunner.NativeTools = decoding.Native
		childRunner.ResponseFormat = decoding.ResponseFormat
		if cap := roleToolIterations(cfg, agent.RoleSDDPlanAuthor); cap > 0 {
			childRunner.MaxToolIterations = cap
		}
		childRunner.Pricing = pricing.Lookup(route.Preset)
		return sddauthor.NewRunner(childRunner), nil
	}
}

// makePipelineRegistryFactory returns a RegistryFactory that builds a fresh
// tool registry bound to a per-dispatch execution context. Each dispatch
// creates its own child session and native toolset so handlers close over
// the correct worktree root and artifact aliases rather than the parent
// session's project root. The returned registry is then filtered by scope.
//
// ScopeFallback narrows file.write_patch to the controller-supplied
// allowlist (set on Dispatcher.FallbackAllowedFiles immediately before
// each fallback dispatch) so the marshal.agent fallback cannot modify
// parts of the worktree outside its declared scope.
func makePipelineRegistryFactory(cfg config.Config, state *session.State, commandRunner native.CommandRunner, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index, parentReg *registry.Registry) pipeline.RegistryFactory {
	return func(ctx pipeline.ExecutionContext, scope pipeline.RegistryScope) (*registry.Registry, error) {
		childState := session.New(cfg, ctx.WorkspaceRoot, time.Now(), session.Persistence{}, session.WithDepth(state.SubagentDepth()+1))
		childReg := registry.New()
		nativeOpts := native.Options{
			WorkspaceRoot:  ctx.WorkspaceRoot,
			CommandRunner:  commandRunner,
			MaxOutputBytes: cfg.Tools.Shell.MaxOutputBytes,
			SessionState:   childState,
			Config:         cfg,
			NamedRoots:     ctx.NamedRoots(),
		}
		if err := native.RegisterAll(childReg, nativeOpts); err != nil {
			return nil, fmt.Errorf("pipeline registry factory: register: %w", err)
		}
		skills.RegisterTool(childReg, skillIndex, childState)
		switch scope {
		case pipeline.ScopeReadOnly:
			return registry.ReadOnlyView(childReg), nil
		case pipeline.ScopeArtifactWriter:
			return registry.ArtifactWriterView(childReg, ctx.ArtifactAlias), nil
		case pipeline.ScopeFallback:
			allowed := currentFallbackAllowedFiles(state)
			if len(allowed) == 0 {
				return nil, fmt.Errorf("pipeline registry factory: fallback scope requires a non-empty allowlist")
			}
			return registry.FallbackWriterView(childReg, allowed), nil
		default:
			return childReg, nil
		}
	}
}

// currentFallbackAllowedFiles returns the controller's pending fallback
// allowlist, if any. The controller stashes it on the session via
// session.SetSDDFallbackAllowedFiles immediately before each fallback
// dispatch and clears it after; the pipeline registry factory reads it
// back here so FallbackWriterView can narrow file.write_patch to the
// declared paths.
func currentFallbackAllowedFiles(state *session.State) []string {
	if state == nil {
		return nil
	}
	return state.SDDFallbackAllowedFiles()
}

// roleToolIterations returns the per-role tool-iteration cap, falling back
// to the agent-wide cap when no role-specific value is configured.
func roleToolIterations(cfg config.Config, role agent.AgentRole) int {
	if n, ok := cfg.Swarm.Budget.ToolIters[string(role)]; ok && n > 0 {
		return n
	}
	return cfg.Agent.MaxToolIterations
}

// parseApprovalMode converts a string to a policy.ApprovalMode.
// Unknown or empty values default to ModeDefault. Delegates to the policy
// package so the TUI seeds its mode label from exactly the same mapping.
func parseApprovalMode(s string) policy.ApprovalMode {
	return policy.ParseApprovalMode(s)
}

// defaultSubtaskIterations is the tool-iteration cap for an ad-hoc agent.run
// child when the user has not set [agent] subtask_iterations in config.
// Kept lower than DefaultMaxToolIterations so a misbehaving child does not
// burn tokens on an out-of-scope subtask while still leaving headroom for
// real research work.
const defaultSubtaskIterations = 12

// buildSubagentFactory returns a closure that constructs a fresh child
// Runner for an agent.run invocation. The closure captures the parent's
// provider, policy engine, and base registry; per call it spins up a new
// session.State (so the child's transcript does not pollute the parent's
// message log), a filtered registry view (read-only + network, no nested
// agent.run), and binds RoleSubtask so the system prompt enforces the
// appropriate scope. The child session's depth is parent+1 so its own
// depth guard rejects any attempt to spawn nested subagents.
//
// When the request names a custom agent, the factory resolves it via the
// router and applies its overrides (system prompt, tool denylist, max
// iterations). An explicit model in the request replaces only the resolved
// preset's provider/model; all other overrides are retained. Both named-agent
// and ad-hoc paths wire Pricing, UsageObserver, and MetricsObserver so
// subagent token usage and cost are visible to the parent session.
func buildSubagentFactory(cfg config.Config, parentState *session.State, parentProvider provider.Provider, parentReg *registry.Registry, pol *policy.PolicyEngine, defaultModel string, router *routing.StaticRouter, database *db.DB, projectID int64) agent.SubagentRunnerFactory {
	subtaskIters := cfg.Agent.SubtaskIterations
	if subtaskIters <= 0 {
		subtaskIters = defaultSubtaskIterations
	}
	metricsObserver := metricsRecorder(database, projectID, parentState.SessionID(), parentState.Logger())
	repoInstructionsForSubagent, _ := loadRepoInstructions(parentState.WorkingDir)
	return func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		childState := session.New(parentState.Config, parentState.WorkingDir, time.Now(), session.Persistence{}, session.WithDepth(parentState.SubagentDepth()+1))
		roReg := agent.SubtaskScopeView(parentReg)
		role := agent.RoleSubtask
		model := defaultModel
		var addendum string
		var pricingRates pricing.ModelPricing
		iters := subtaskIters
		// An explicit provider/model pair resolves first so invalid pairs
		// fail clearly before any execution, and so a named agent can replace
		// only its preset provider/model while keeping its other overrides.
		var explicitPreset *routing.ModelPreset
		if req.Model != "" {
			if router == nil {
				return nil, nil, fmt.Errorf("agent.run: explicit model %q requested but no router configured", req.Model)
			}
			eroute, err := router.ResolveExplicitModel(req.Model, agent.RoleSubtask)
			if err != nil {
				return nil, nil, fmt.Errorf("agent.run: %w", err)
			}
			model = eroute.Preset.Model
			pricingRates = pricing.Lookup(eroute.Preset)
			explicitPreset = &eroute.Preset
		}
		if req.Agent != "" && router != nil {
			route, err := router.ResolveCustomAgent(req.Agent, agent.RoleSubtask)
			if err != nil {
				return nil, nil, fmt.Errorf("agent.run: %w", err)
			}
			if explicitPreset == nil {
				model = route.Preset.Model
				pricingRates = pricing.Lookup(route.Preset)
			}
			if route.CustomAgent != nil {
				ca := route.CustomAgent
				addendum = ca.SystemPrompt
				if len(ca.ToolDenylist) > 0 {
					roReg = agent.DenylistView(roReg, ca.ToolDenylist)
				}
				if ca.MaxIterations > 0 {
					iters = ca.MaxIterations
				}
			}
		}
		child := agent.NewRunner(parentProvider, roReg, pol, childState, model)
		child.Role = role
		child.MaxToolIterations = iters
		child.NativeTools = true
		child.SystemPromptAddendum = composeAddendum(repoInstructionsForSubagent, addendum)
		child.Pricing = pricingRates
		child.MetricsObserver = metricsObserver
		// Fold the child's token usage into the parent session's running
		// total so /context and the session usage view include subagent
		// work. The child session is separate; this is additive to the
		// parent's own turns, not a double-count.
		child.UsageObserver = func(usage schema.TokenUsage) {
			used, _ := parentState.TurnUsage()
			parentState.SetTurnUsage(used + usage.PromptTokens + usage.CompletionTokens)
		}
		return child, childState, nil
	}
}

type actionDecodingConfig struct {
	Native         bool
	ResponseFormat *schema.ResponseFormat
}

// resolveActionDecoding builds the opt-in decoding mode for a model preset.
// It never fails construction: unsupported preferences degrade to the next
// provider-supported mode.
//
// An unset preference ("") falls back to whatever the provider advertises:
// when caps.ToolCalling is true the runner uses native tool-calling, otherwise
// it degrades to the JSON-envelope path (json_schema → json_object → nil),
// matching the "native" preference's fallback. "none" explicitly opts out of
// tool-calling entirely (text-only, no envelope).
func resolveActionDecoding(toolCalling string, caps schema.ProviderCapabilities) actionDecodingConfig {
	switch toolCalling {
	case "native", "":
		if caps.ToolCalling {
			return actionDecodingConfig{Native: true}
		}
		return actionDecodingConfig{ResponseFormat: fallbackResponseFormat(caps)}
	case "json_schema":
		return actionDecodingConfig{ResponseFormat: fallbackResponseFormat(caps)}
	case "none":
		return actionDecodingConfig{}
	}
	return actionDecodingConfig{}
}

func fallbackResponseFormat(caps schema.ProviderCapabilities) *schema.ResponseFormat {
	if caps.StructuredOutput {
		return agent.ActionEnvelopeResponseFormat()
	}
	if caps.JSONMode {
		return &schema.ResponseFormat{Type: "json_object"}
	}
	return nil
}

func Run(ctx context.Context, stdout io.Writer, opts ...Option) error {
	if ctx.Err() != nil {
		return nil
	}

	runOpts := options{
		now:           time.Now,
		configLoader:  config.Load,
		programRunner: runProgram,
	}
	for _, opt := range opts {
		opt(&runOpts)
	}
	runOpts.deferTrustPrompt = true

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	workingDir, err := resolveWorkingDir(runOpts.workingDir)
	if err != nil {
		return err
	}

	// Define configReloader before startRuntime so it can be wired into
	// native.Options. The closure captures rt by reference; rt is set
	// immediately after startRuntime returns.
	var rt *Runtime
	configReloader := func(newCfg config.Config) error {
		return reloadAgentRuntime(ctx, newCfg, rt)
	}
	runOpts.configReloader = configReloader

	var reloadForTrust bool
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// First-run detection: when no config exists yet, open the connect
	// panel in the TUI instead of running a separate onboarding wizard.
	firstRun := !config.HasConfig(config.LoadOptions{HomeDir: homeDir, WorkingDir: workingDir})
	trustStoreDir := filepath.Join(homeDir, ".local", "share", "marshal")
	trustDecide := func(d trust.Decision) {
		switch d {
		case trust.DecisionTrustPermanent:
			abs, _ := filepath.Abs(workingDir)
			hash, _ := trust.ConfigHashFor(workingDir)
			_ = trust.NewStore(trustStoreDir).SetTrust(abs, true, hash)
			reloadForTrust = true
		case trust.DecisionTrustSession:
			runOpts.sessionTrusted = true
			reloadForTrust = true
		}
	}
	// Interactive project-config saves (/settings, /set, /agents) made from
	// a trusted session advance the permanent-trust config hash so the next
	// launch doesn't re-prompt. RefreshConfigHash is a no-op when the project
	// has no permanent-trust record, so external or agent-made config edits
	// (which never hit this path) still force re-trust.
	trustRefresh := func(dir string) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return
		}
		hash, err := trust.ConfigHashFor(dir)
		if err != nil {
			return
		}
		_ = trust.NewStore(trustStoreDir).RefreshConfigHash(abs, hash)
	}

	defer func() {
		if rt != nil {
			_ = rt.Close(context.Background())
		}
	}()

	for {
		rt, err = startRuntime(ctx, runOpts)
		if err != nil {
			return err
		}

		cfg := rt.Config
		workingDir = rt.WorkingDir
		database := must[*db.DB](rt.DB)
		projectID := rt.ProjectID
		sessionID := rt.SessionID
		runner := rt.Runner
		swarmRunner := rt.SwarmRunner
		toolReg := rt.ToolRegistry
		jobBroker := must[*pubsub.Broker[native.JobEvent]](rt.JobBroker)
		steeringBroker := must[*pubsub.Broker[session.SteeringEvent]](rt.SteeringBroker)
		workspaceBroker := must[*pubsub.Broker[session.WorkspaceEvent]](rt.WorkspaceBroker)
		state := rt.State
		logger := rt.Logger

		cmdReg := rt.CommandRegistry

		var tuiOpts []tui.Option
		tuiOpts = append(tuiOpts, tui.WithMemoryStore(database, projectID))
		tuiOpts = append(tuiOpts, tui.WithCommandRegistry(cmdReg))
		configLayers := &rt.Layers
		tuiOpts = append(tuiOpts, tui.WithConfigLayers(&configLayers))
		tuiOpts = append(tuiOpts, tui.WithLayerReloader(func() (config.Layers, bool) {
			layers, err := config.LoadLayers(config.LoadOptions{
				WorkingDir: workingDir,
			})
			if err != nil {
				return config.Layers{}, false
			}
			return layers, true
		}))
		// F18: eager-seed the @file completion popup with the repo file
		// index. Failures (no DB, empty index) are non-fatal — the TUI
		// falls back to a lazy load on the first @-keystroke.
		if filePaths, ferr := loadFileIndexPaths(database, projectID); ferr == nil && len(filePaths) > 0 {
			tuiOpts = append(tuiOpts, tui.WithFileIndex(filePaths))
		}
		if firstRun {
			tuiOpts = append(tuiOpts, tui.WithOpenConnectOnStart())
		}
		// Config reloading stays wired even when the initial provider build
		// failed: /settings, /set and /connect are the recovery path. A
		// successful reload rebuilds the runtime in place, and the runner
		// source lets the TUI adopt the rebuilt runner (see tui.adoptRunner).
		tuiOpts = append(tuiOpts, tui.WithConfigReloader(configReloader))
		tuiOpts = append(tuiOpts, tui.WithHomeDir(homeDir))
		tuiOpts = append(tuiOpts, tui.WithWorkingDir(workingDir))
		tuiOpts = append(tuiOpts, tui.WithRunnerSource(func() (context.Context, tui.AgentRunner) {
			rt.mu.Lock()
			defer rt.mu.Unlock()
			if rt.Runner == nil {
				return nil, nil
			}
			return ctx, rt.Runner
		}))
		if state.ProviderError() == nil {
			tuiOpts = append(tuiOpts, tui.WithRunner(ctx, runner))
			tuiOpts = append(tuiOpts, tui.WithSwarmRunner(ctx, swarmRunner))
			tuiOpts = append(tuiOpts, tui.WithPipelineFactory(ctx, rt.PipelineFactory))
			tuiOpts = append(tuiOpts, tui.WithPlanAuthorFactory(ctx, rt.PlanAuthorFactory))
			tuiOpts = append(tuiOpts, tui.WithJobBroker(ctx, jobBroker))
			tuiOpts = append(tuiOpts, tui.WithSteeringBroker(ctx, steeringBroker))
			tuiOpts = append(tuiOpts, tui.WithWorkspaceBroker(ctx, workspaceBroker))
			tuiOpts = append(tuiOpts, tui.WithToolRegistry(toolReg))
			tuiOpts = append(tuiOpts, tui.WithCustomAgentRunnerFactory(
				func(agentName string) (tui.AgentRunner, error) {
					runner, _, err := rt.CustomAgentFactory(agent.SubagentRequest{Agent: agentName})
					return runner, err
				},
			))
			tuiOpts = append(tuiOpts, tui.WithSubagentFactory(
				func(agentName string) (tui.AgentRunner, error) {
					runner, _, err := rt.CustomAgentFactory(agent.SubagentRequest{Agent: agentName})
					return runner, err
				},
			))
		}
		if rt.TrustPromptPending {
			tuiOpts = append(tuiOpts, tui.WithTrustPrompt(workingDir, trustDecide))
		}
		tuiOpts = append(tuiOpts, tui.WithTrustRefresh(trustRefresh))
		if rt.DataDir != "" {
			tuiOpts = append(tuiOpts, tui.WithModelCache(rt.DataDir))
		}

		logger.Info("marshal started", "project", cfg.Project.Name, "working_dir", workingDir)

		// ── Worker lifecycle ──────────────────────────────────────────────
		// Start injected workers (test seam) or construct the index watcher
		// when embeddings are configured and the watcher is enabled.
		workers := runOpts.workers
		if len(workers) == 0 {
			embeddingConfigured := false
			embedRouter := routing.NewStaticRouter(cfg.RoutingConfig())
			if _, err := embedRouter.ResolveEmbedding(); err == nil {
				embeddingConfigured = true
			}

			// Surface a hint when embedding indexing is enabled but no
			// embedding role is bound in the active profile — semantic search
			// will report "unavailable" until the role is set.
			if cfg.Indexing.UseEmbeddings && !embeddingConfigured {
				msg := "Semantic search is enabled (indexing.use_embeddings) but no embedding role is configured in the active agent profile. " +
					"Bind one with: [agent_profiles.<profile>.roles] embedding = '<preset>' — or via /settings → Profiles."
				logger.Warn("embedding role not configured while use_embeddings is enabled")
				state.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
			}

			// Build the LSP symbol adapter for the index pass.
			var lspAdapter index.LSPSymbols
			if rt.LSPManager != nil {
				lspAdapter = lsp.NewSymbolAdapter(rt.LSPManager)
			}

			if config.WatchEnabled(cfg.Indexing.Watch, embeddingConfigured) {
				debounce := time.Duration(cfg.Indexing.WatchDebounceMs) * time.Millisecond
				runPass := func(c context.Context) error {
					embedder := resolveEmbedderFromConfig(cfg)
					_, err := index.Run(c, index.Deps{
						DB: database, Root: workingDir, Ignore: cfg.Indexing.Ignore,
						MaxBytes: cfg.Indexing.MaxIndexableFileBytes, Embedder: embedder,
						LSP: lspAdapter,
					}, projectID)
					return err
				}
				workers = append(workers, index.NewWatcher(workingDir, debounce, runPass, logger))
			}
		}
		// LSPManager is started inside startRuntime (shared by Run and
		// StartRuntime) — see runtime.go. Do not start it again here.
		var workerWG sync.WaitGroup
		for _, w := range workers {
			startWorker(rt.workCtx, &workerWG, w, logger)
		}
		// ── End worker lifecycle ──────────────────────────────────────────

		select {
		case <-ctx.Done():
			return nil
		default:
		}

		progResult := runOpts.programRunner(ctx, tui.New(state, tuiOpts...), stdout)

		if progResult.ResumeSession != "" && progResult.Err == nil {
			// User chose to resume a different session. Tear down the
			// current runtime without finalising knowledge (the current
			// transcript is already persisted; knowledge can run when that
			// session is eventually exited) and restart startRuntime in
			// existing-session mode.
			_ = rt.Close(context.Background())
			runOpts.sessionID = ""
			runOpts.existingSessionID = progResult.ResumeSession
			continue
		}

		progErr := progResult.Err
		if !reloadForTrust {
			// Phase 1: quiesce — cancel and join active work/jobs without
			// closing persistence so knowledge finalization can use the DB.
			quiesceCtx, cancelQuiesce := context.WithTimeout(context.Background(), jobShutdownTimeout)
			quiesceErr := rt.Quiesce(quiesceCtx)
			cancelQuiesce()

			// Wait for workers to finish (bounded).
			workerDone := make(chan struct{})
			go func() {
				workerWG.Wait()
				close(workerDone)
			}()
			select {
			case <-workerDone:
			case <-time.After(5 * time.Second):
				logger.Warn("worker shutdown timed out")
			}

			// Phase 2: knowledge — finalize the session while DB and logger
			// are still open.
			knowledgeCtx, cancelKnowledge := context.WithTimeout(context.Background(), shutdownKnowledgeTimeout)
			knowledge.EndSession(knowledgeCtx, knowledge.EndSessionInput{
				DB:            database,
				ProjectID:     projectID,
				SessionID:     sessionID,
				State:         state,
				RouteResolver: newRoutedProviderResolver(state.Config, rt.DataDir),
				WorkingDir:    workingDir,
				Now:           runOpts.now,
				Logger:        logger,
			})
			cancelKnowledge()

			if runOpts.knowledgeHook != nil {
				runOpts.knowledgeHook(knowledgeCtx, state, database)
			}

			// Phase 3: close — tear down MCP, brokers, snapshots, DB, logger.
			closeErr := rt.Close(context.Background())
			return errors.Join(progErr, quiesceErr, closeErr)
		}

		// Trust granted inline: tear down quietly and reinitialise via the
		// same startRuntime path as startup — now with the project config in
		// force. No Quiesce/knowledge: no agent work ever ran.
		reloadForTrust = false
		// Use Background: the user may have cancelled ctx during the trust
		// prompt; the deferred Close covers the final rt, this covers reload
		// iterations.
		_ = rt.Close(context.Background())
	}
}

func reloadAgentRuntime(ctx context.Context, cfg config.Config, rt *Runtime) error {
	db := must[*db.DB](rt.DB)
	jb := must[*pubsub.Broker[native.JobEvent]](rt.JobBroker)
	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, newDesktopCloser, newSubagentFactory, newLSPHandle, newPipelineFactory, newPlanAuthorFactory, err := buildAgentRunner(rt.workCtx, cfg, rt.State, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb, rt.ConfigReloader, rt.HomeDir)
	if err != nil {
		slog.Default().Warn("reload: dry-run build failed; keeping previous config",
			"err", err)
		rt.State.AddMessage(session.RoleSystem,
			"Config reload failed; keeping previous settings.",
			session.ContentTypePlain)
		return err
	}

	// Config validated — swap atomically with the runtime.
	rt.State.Config = cfg

	// Capture old values for cleanup under the pointer mutex.
	rt.mu.Lock()
	oldMCP := rt.MCPManager
	oldJobMgr := rt.JobManager
	oldDesktopCloser := rt.DesktopCloser
	oldLSP := rt.LSPManager

	// Copy in-place fields. When the startup provider build failed there is
	// no live runner to mutate — adopt the rebuilt one wholesale (mirroring
	// the startRuntime hook wiring) so the session can recover via /connect
	// or /settings without a restart.
	if rt.Runner == nil {
		if rt.State.Trusted() && len(cfg.Hooks.Entries) > 0 {
			newRunner.HookRunner = hooks.NewRunnerFromConfig(cfg.Hooks)
		}
		rt.Runner = newRunner
	} else {
		rt.Runner.CopyFrom(newRunner)
	}
	if rt.SwarmRunner == nil {
		rt.SwarmRunner = newSwarmRunner
	} else if newSwarmRunner != nil {
		*rt.SwarmRunner = *newSwarmRunner
	}
	if newPipelineFactory != nil {
		rt.PipelineFactory = newPipelineFactory
	}
	if newPlanAuthorFactory != nil {
		rt.PlanAuthorFactory = newPlanAuthorFactory
	}

	// Swap reload-owned pointers.
	rt.ToolRegistry = newReg
	if newMCP != nil {
		rt.MCPManager = newMCP
	} else {
		rt.MCPManager = nil
	}
	if newSnap != nil {
		rt.Snapshot = newSnap
	} else {
		rt.Snapshot = nil
	}
	if newLSPHandle != nil {
		rt.LSPManager = newLSPHandle
	} else {
		rt.LSPManager = nil
	}
	rt.JobManager = newJobMgr
	rt.DesktopCloser = newDesktopCloser
	rt.CustomAgentFactory = newSubagentFactory
	rt.mu.Unlock()

	// Cleanup old resources outside the lock.
	var cleanupErr error
	if oldMCP != nil {
		// Check that the old interface holds a non-nil concrete pointer.
		if err := oldMCP.Close(); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if oldJobMgr != nil {
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := oldJobMgr.Shutdown(sc); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if oldDesktopCloser != nil {
		oldDesktopCloser()
	}
	if oldLSP != nil && oldLSP != newLSPHandle {
		// Stop the old LSP manager generation. Start a new one if the reload
		// produced a handle, otherwise leave the slot empty.
		rt.lspCancel()
		if newLSPHandle != nil {
			rt.lspCancel = rt.runLSPManager(rt.LSPManager.Get())
		}
	}
	return cleanupErr
}

func runProgram(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
	program := tea.NewProgram(model,
		tea.WithOutput(output),
		tea.WithContext(ctx),
	)
	finalModel, err := program.Run()
	res := ProgramResult{Err: err}
	if signaller, ok := finalModel.(interface{ ResumeSession() string }); ok {
		res.ResumeSession = signaller.ResumeSession()
	}
	return res
}

// loadFileIndexPaths fetches the repo's known file paths for the
// completion popup. Returns nil on any error (no DB, no rows, query
// failure) so callers can treat absence as "skip the eager seed".
// resolveEmbedderFromConfig constructs an embedding.Embedder from the
// routing config, or returns nil when no embedding role is configured.
// This mirrors the native tool set's resolveEmbedder closure.
func resolveEmbedderFromConfig(cfg config.Config) embedding.Embedder {
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	route, err := router.ResolveEmbedding()
	if err != nil {
		return nil
	}
	pc, ok := cfg.Providers[route.Preset.Provider]
	if !ok {
		return nil
	}
	e, err := embedding.NewFromConfig(route.Preset.Provider, pc, route.Preset.Model)
	if err != nil {
		return nil
	}
	return e
}

func loadFileIndexPaths(database *db.DB, projectID int64) ([]string, error) {
	if database == nil || projectID == 0 {
		return nil, fmt.Errorf("no database or project id")
	}
	index, err := database.GetFileIndex(projectID, 0)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(index))
	for _, f := range index {
		paths = append(paths, f.Path)
	}
	return paths, nil
}

func newDesktopBackend(cfg config.DesktopConfig) (browser.BrowserBackend, error) {
	timeout := cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	switch cfg.Mode {
	case "attach":
		return browser.NewAttachBackend(cfg.CDPURL, timeout)
	case "standalone", "":
		return browser.NewStandaloneBackend(cfg.Headless, timeout)
	default:
		return nil, fmt.Errorf("unknown desktop mode %q", cfg.Mode)
	}
}

// ── LSP helpers ────────────────────────────────────────────────────────

// lspEnabled returns true when the LSP config is not explicitly disabled.
func lspEnabled(cfg config.LSPConfig) bool {
	return cfg.Enabled == nil || *cfg.Enabled
}

// toServerSpecs converts the config's LSPServerConfig map to the lsp.ServerSpec
// map used by DetectServers.
func toServerSpecs(cfgServers map[string]config.LSPServerConfig) map[string]lsp.ServerSpec {
	out := make(map[string]lsp.ServerSpec, len(cfgServers))
	for lang, s := range cfgServers {
		out[lang] = lsp.ServerSpec{Command: s.Command, Args: s.Args}
	}
	return out
}

// disabledLangs returns the set of languages that are explicitly disabled in
// the LSP server config.
func disabledLangs(cfgServers map[string]config.LSPServerConfig) map[string]bool {
	out := make(map[string]bool, len(cfgServers))
	for lang, s := range cfgServers {
		if s.Disabled {
			out[lang] = true
		}
	}
	return out
}
