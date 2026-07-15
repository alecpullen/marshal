package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/agent"
	"marshal/internal/agent/sdd"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/commands"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/knowledge"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/pubsub"
	"marshal/internal/sandbox"
	"marshal/internal/skills"
	"marshal/internal/snapshot"
	"marshal/internal/tools/desktop"
	"marshal/internal/tools/desktop/browser"
	"marshal/internal/tools/mcp"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/trust"
)

type ProgramRunner func(ctx context.Context, model tea.Model, output io.Writer) error
type configLoader func(config.LoadOptions) (config.Config, error)

// mustDB panics with a clear message if raw is a non-nil value that is not
// *db.DB. Nil is accepted (returned as nil) since the original type assertion
// produced nil for a nil interface value.
func mustDB(raw DBCloser) *db.DB {
	if raw == nil {
		return nil
	}
	d, ok := raw.(*db.DB)
	if !ok {
		panic(fmt.Sprintf("runtime: DBCloser is %T, want *db.DB", raw))
	}
	return d
}

type options struct {
	now               func() time.Time
	configLoader      configLoader
	programRunner     ProgramRunner
	skipOnboarding    bool
	trustResolver     trust.Resolver
	workingDir        string
	sessionID         string
	existingSessionID string
	additionalDirs    []string
	knowledgeHook     func(ctx context.Context, state *session.State, database *db.DB)
}

type Option func(*options)

var shutdownKnowledgeTimeout = 5 * time.Second

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

func WithSkipOnboarding(skip bool) Option {
	return func(opts *options) {
		opts.skipOnboarding = skip
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

func dbPath(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "marshal.db")
}

func logPath(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "marshal.log")
}

func routingConfigFromAppConfig(cfg config.Config) routing.Config {
	contextBudgets := make(map[routing.AgentRole]routing.ContextBudget, len(cfg.Agents))
	for role, agentCfg := range cfg.Agents {
		contextBudgets[role] = agentCfg.Context
	}
	return routing.Config{
		DefaultProfile: cfg.Profile.Default,
		RemoteAllowed:  cfg.Privacy.RemoteProvidersAllowed,
		Presets:        cfg.Models.Presets,
		Profiles:       cfg.AgentProfiles,
		ContextBudgets: contextBudgets,
		LegacyProvider: cfg.Agent.Provider,
		LegacyModel:    cfg.Agent.Model,
	}
}

type routedProviderResolver struct {
	router    *routing.StaticRouter
	cfg       config.Config
	mu        sync.Mutex // guards providers; swarm may resolve roles from concurrent paths
	providers map[string]provider.Provider
}

// dbMemoryProvider adapts stored project memories for context-pack
// injection, excluding memories that have been marked stale.
type dbMemoryProvider struct {
	db *db.DB
}

func newRoutedProviderResolver(cfg config.Config) *routedProviderResolver {
	return &routedProviderResolver{
		router:    routing.NewStaticRouter(routingConfigFromAppConfig(cfg)),
		cfg:       cfg,
		providers: make(map[string]provider.Provider),
	}
}

func (r *routedProviderResolver) Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error) {
	route, err := r.router.Resolve(task)
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
	p, err := provider.NewFromConfig(route.Preset.Provider, providerConfig)
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
			SoftStalls:       m.SoftStalls,
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

func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, dataDir string, additionalDirs []string, jobBroker *pubsub.Broker[native.JobEvent]) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *sdd.Orchestrator, *mcp.Manager, *snapshot.Service, *native.JobManager, func(), error) {
	resolver := newRoutedProviderResolver(cfg)
	route, resolvedProvider, err := resolver.Resolve(routing.TaskProfile{Class: "edit"})
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, nil, err
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
		return nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("build sandbox: %w", sbErr)
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
	var jmErr error
	defer func() {
		if jmErr != nil {
			sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			jobManager.Shutdown(sc)
		}
	}()

	var fileTracker native.FileTracker
	if database != nil {
		fileTracker = filetrack.New(database.SQLDB(), state.SessionID())
	}

	pol := policy.NewEngine(&cfg, state.SessionRules())
	if state.Logger() != nil {
		pol.SetLogger(state.Logger())
	}

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
	}
	if len(additionalDirs) > 0 {
		nativeOpts.AdditionalRoots = additionalDirs
	}
	if err := native.RegisterAll(reg, nativeOpts); err != nil {
		jmErr = err
		return nil, nil, nil, nil, nil, nil, nil, nil, err
	}

	skills.RegisterTool(reg, skillIndex, state)

	var mcpMgr *mcp.Manager
	if len(cfg.MCP.Servers) > 0 {
		mcpMgr = mcp.NewManager(&cfg, mcp.WithManagerLogger(state.Logger()))
		if err := mcpMgr.Start(ctx); err != nil {
			jmErr = err
			return nil, nil, nil, nil, nil, nil, nil, nil, err
		}
		if err := mcpMgr.RegisterTools(reg); err != nil {
			mcpMgr.Close()
			jmErr = err
			return nil, nil, nil, nil, nil, nil, nil, nil, err
		}
	}
	if err := reg.Register(agent.NewSubagentTool(
		buildSubagentFactory(cfg, state, resolvedProvider, reg, pol, route.Preset.Model),
		reg,
		state,
		2,
	)); err != nil {
		jmErr = err
		return nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.run: %w", err)
	}
	runner := agent.NewRunner(resolvedProvider, reg, pol, state, route.Preset.Model)
	runner.SkillIndex = skillIndex
	runner.RouteResolver = resolver
	runner.MemoryProvider = &dbMemoryProvider{db: database}
	runner.ProjectID = projectID
	runner.MetricsObserver = metricsRecorder(database, projectID, state.SessionID(), state.Logger())
	runner.UsageObserver = func(promptTokens, completionTokens int) {
		state.SetTurnUsage(promptTokens + completionTokens)
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
	runner.PlanFirst = cfg.Agent.PlanFirst
	if runner.RequestTimeout == 0 {
		runner.RequestTimeout = 60 * time.Second
	}

	var snapSvc *snapshot.Service
	if dataDir != "" && cfg.Snapshots.Enabled {
		snapSvc = snapshot.New(dataDir, state.WorkingDir, int64(cfg.Snapshots.MaxFileBytes), cfg.Indexing.Ignore, state.Logger())
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
		Legacy:    route.Legacy,
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
	swarmRunner := buildSwarmRunner(ctx, cfg, state, reg, pol, resolver, database, projectID, skillIndex)

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
			jmErr = err
			return nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register desktop tools: %w", err)
		}
		desktopCloser = closer
	}

	sddRunner := buildSDDRunner(ctx, cfg, state, reg, pol, resolver, database, projectID, skillIndex)
	return runner, reg, swarmRunner, sddRunner, mcpMgr, snapSvc, jobManager, desktopCloser, nil
}

// buildSwarmRunner wires the Milestone O swarm: every role runner shares
// the session state, policy engine, and one WriteLock; read-only roles get
// the filtered registry view; each role's provider/model comes from the
// routing profile via ResolveRole (falling back to the implementer preset
// for unconfigured roles).
func buildSwarmRunner(ctx context.Context, cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index) *swarm.Orchestrator {
	readOnlyReg := registry.ReadOnlyView(reg)
	testerReg := registry.TesterView(reg)
	gate := &swarm.WriteLock{}
	memory := &dbMemoryProvider{db: database}

	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		// agent.AgentRole and routing.AgentRole share string values
		// ("planner", "repo_scout", "implementer", "reviewer").
		route, p, err := resolver.ResolveRole(routing.AgentRole(role))
		if err != nil {
			return nil, err
		}
		toolReg := reg
		switch scope {
		case swarm.ScopeReadOnly:
			toolReg = readOnlyReg
		case swarm.ScopeTester:
			toolReg = testerReg
		}
		r := agent.NewRunner(p, toolReg, pol, state, route.Preset.Model)
		r.Role = role
		r.WriteGate = gate
		r.SkillIndex = skillIndex
		r.MemoryProvider = memory
		r.ProjectID = projectID
		r.MetricsObserver = metricsRecorder(database, projectID, state.SessionID(), state.Logger())
		r.RequestTimeout = 60 * time.Second
		// Swarm role prompts embed the shared plan, so skip the per-turn
		// classify/plan pass (class "question" bypasses planning).
		r.SetForceClass("question")
		decoding := resolveActionDecoding(route.Preset.ToolCalling, p.Capabilities(ctx))
		r.NativeTools = decoding.Native
		r.ResponseFormat = decoding.ResponseFormat
		if cap := roleToolIterations(cfg, role); cap > 0 {
			r.MaxToolIterations = cap
		}
		if cfg.Agent.MaxRetries > 0 {
			r.MaxRetries = cfg.Agent.MaxRetries
		}
		if cfg.Agent.MaxTurnContextTokens > 0 {
			r.MaxTurnContextTokens = cfg.Agent.MaxTurnContextTokens
		}
		r.PlanFirst = cfg.Agent.PlanFirst
		return r, nil
	}
	o := swarm.New(state, factory)
	o.MaxFixRounds = cfg.Swarm.Budget.MaxFixRounds
	o.MaxTotalTokens = cfg.Swarm.Budget.MaxTotalTokens
	o.NewMeter = func() swarm.TokenMeter { return swarm.NewEstimateMeter() }
	return o
}

// buildSDDRunner wires the SDD orchestrator: same factory pattern as the
// swarm, resolving each SDD role's route via the routing profile.
func buildSDDRunner(ctx context.Context, cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index) *sdd.Orchestrator {
	readOnlyReg := registry.ReadOnlyView(reg)
	factory := func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		route, p, err := resolver.ResolveRole(routing.AgentRole(role))
		if err != nil {
			return nil, err
		}
		toolReg := reg
		switch scope {
		case swarm.ScopeReadOnly:
			toolReg = readOnlyReg
		}
		r := agent.NewRunner(p, toolReg, pol, state, route.Preset.Model)
		r.Role = role
		r.SkillIndex = skillIndex
		r.MemoryProvider = &dbMemoryProvider{db: database}
		r.ProjectID = projectID
		r.RequestTimeout = 60 * time.Second
		r.SetForceClass("question")
		decoding := resolveActionDecoding(route.Preset.ToolCalling, p.Capabilities(ctx))
		r.NativeTools = decoding.Native
		r.ResponseFormat = decoding.ResponseFormat
		if cfg.Agent.MaxToolIterations > 0 {
			r.MaxToolIterations = cfg.Agent.MaxToolIterations
		}
		return r, nil
	}
	return sdd.New(state, factory, cfg.SDD)
}

// roleToolIterations returns the per-role tool-iteration cap, falling back
// to the agent-wide cap when no role-specific value is configured.
func roleToolIterations(cfg config.Config, role agent.AgentRole) int {
	if n, ok := cfg.Swarm.Budget.ToolIters[string(role)]; ok && n > 0 {
		return n
	}
	return cfg.Agent.MaxToolIterations
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
func buildSubagentFactory(cfg config.Config, parentState *session.State, parentProvider provider.Provider, parentReg *registry.Registry, pol *policy.PolicyEngine, defaultModel string) agent.SubagentRunnerFactory {
	subtaskIters := cfg.Agent.SubtaskIterations
	if subtaskIters <= 0 {
		subtaskIters = defaultSubtaskIterations
	}
	return func() (*agent.Runner, error) {
		childState := session.New(parentState.Config, parentState.WorkingDir, time.Now(), session.Persistence{}, session.WithDepth(parentState.SubagentDepth()+1))
		roReg := agent.SubtaskScopeView(parentReg)
		child := agent.NewRunner(parentProvider, roReg, pol, childState, defaultModel)
		child.Role = agent.RoleSubtask
		child.MaxToolIterations = subtaskIters
		child.NativeTools = true
		return child, nil
	}
}

type actionDecodingConfig struct {
	Native         bool
	ResponseFormat *schema.ResponseFormat
}

// resolveActionDecoding builds the opt-in decoding mode for a model preset.
// It never fails construction: unsupported preferences degrade to the next
// provider-supported mode.
func resolveActionDecoding(toolCalling string, caps schema.ProviderCapabilities) actionDecodingConfig {
	switch toolCalling {
	case "native":
		if caps.ToolCalling {
			return actionDecodingConfig{Native: true}
		}
		return actionDecodingConfig{ResponseFormat: fallbackResponseFormat(caps)}
	case "json_schema":
		return actionDecodingConfig{ResponseFormat: fallbackResponseFormat(caps)}
	case "json":
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

func Run(ctx context.Context, stdout io.Writer, stderr io.Writer, opts ...Option) error {
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

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	workingDir, err := resolveWorkingDir(runOpts.workingDir)
	if err != nil {
		return err
	}

	if !runOpts.skipOnboarding && flag.Lookup("test.v") == nil && !config.HasConfig(config.LoadOptions{WorkingDir: workingDir}) {
		onboarding := NewOnboardingModel(workingDir)
		p := tea.NewProgram(onboarding, tea.WithOutput(stdout), tea.WithContext(ctx))
		if _, err := p.Run(); err != nil {
			return fmt.Errorf("onboarding failed: %w", err)
		}
		if onboarding.state != stateDone {
			return nil
		}
	}

	rt, err := StartRuntime(ctx, opts...)
	if err != nil {
		return err
	}
	defer func() {
		_ = rt.Close(context.Background())
	}()

	cfg := rt.Config
	workingDir = rt.WorkingDir
	database := mustDB(rt.DB)
	projectID := rt.ProjectID
	sessionID := rt.SessionID
	runner := rt.Runner
	swarmRunner := rt.SwarmRunner
	sddRunner := rt.SDDRunner
	toolReg := rt.ToolRegistry
	jobBroker, ok := rt.JobBroker.(*pubsub.Broker[native.JobEvent])
	if !ok && rt.JobBroker != nil {
		panic(fmt.Sprintf("runtime: JobBroker is %T, want *pubsub.Broker[native.JobEvent]", rt.JobBroker))
	}
	steeringBroker, ok := rt.SteeringBroker.(*pubsub.Broker[session.SteeringEvent])
	if !ok && rt.SteeringBroker != nil {
		panic(fmt.Sprintf("runtime: SteeringBroker is %T, want *pubsub.Broker[session.SteeringEvent]", rt.SteeringBroker))
	}
	state := rt.State
	logger := rt.Logger

	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, toolReg); err != nil {
		return fmt.Errorf("register commands: %w", err)
	}

	jobBrokerCtx := ctx
	var tuiOpts []tui.Option
	tuiOpts = append(tuiOpts, tui.WithMemoryStore(database, projectID))
	tuiOpts = append(tuiOpts, tui.WithCommandRegistry(cmdReg))
	// F18: eager-seed the @file completion popup with the repo file
	// index. Failures (no DB, empty index) are non-fatal — the TUI
	// falls back to a lazy load on the first @-keystroke.
	if filePaths, ferr := loadFileIndexPaths(database, projectID); ferr == nil && len(filePaths) > 0 {
		tuiOpts = append(tuiOpts, tui.WithFileIndex(filePaths))
	}
	if state.ProviderError() == nil {
		tuiOpts = append(tuiOpts, tui.WithRunner(ctx, runner))
		tuiOpts = append(tuiOpts, tui.WithSwarmRunner(ctx, swarmRunner))
		tuiOpts = append(tuiOpts, tui.WithSDDRunner(ctx, sddRunner))
		tuiOpts = append(tuiOpts, tui.WithJobBroker(jobBrokerCtx, jobBroker))
		tuiOpts = append(tuiOpts, tui.WithSteeringBroker(jobBrokerCtx, steeringBroker))
		configReloader := func(newCfg config.Config) error {
			return reloadAgentRuntime(ctx, newCfg, rt)
		}
		tuiOpts = append(tuiOpts, tui.WithConfigReloader(configReloader))
	}

	logger.Info("marshal started", "project", cfg.Project.Name, "working_dir", workingDir)

	select {
	case <-ctx.Done():
		return nil
	default:
	}

	progErr := runOpts.programRunner(ctx, tui.New(state, tuiOpts...), stdout)

	// Phase 1: quiesce — cancel and join active work/jobs without
	// closing persistence so knowledge finalization can use the DB.
	quiesceCtx, cancelQuiesce := context.WithTimeout(context.Background(), jobShutdownTimeout)
	quiesceErr := rt.Quiesce(quiesceCtx)
	cancelQuiesce()

	// Phase 2: knowledge — finalize the session while DB and logger
	// are still open.
	knowledgeCtx, cancelKnowledge := context.WithTimeout(context.Background(), shutdownKnowledgeTimeout)
	knowledge.EndSession(knowledgeCtx, knowledge.EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: newRoutedProviderResolver(state.Config),
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

func reloadAgentRuntime(ctx context.Context, cfg config.Config, rt *Runtime) error {
	if rt.Runner == nil {
		return nil
	}
	db := mustDB(rt.DB)
	jb, ok := rt.JobBroker.(*pubsub.Broker[native.JobEvent])
	if !ok && rt.JobBroker != nil {
		panic(fmt.Sprintf("runtime: JobBroker is %T, want *pubsub.Broker[native.JobEvent]", rt.JobBroker))
	}
	newRunner, newReg, newSwarmRunner, newSDDRunner, newMCP, newSnap, newJobMgr, newDesktopCloser, err := buildAgentRunner(rt.workCtx, cfg, rt.State, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb)
	if err != nil {
		return err
	}

	// Config validated — swap atomically with the runtime.
	rt.State.Config = cfg

	// Capture old values for cleanup under the pointer mutex.
	rt.mu.Lock()
	oldMCP := rt.MCPManager
	oldJobMgr := rt.JobManager
	oldSnap := rt.Snapshot
	oldDesktopCloser := rt.DesktopCloser

	// Copy in-place fields.
	rt.Runner.CopyFrom(newRunner)
	if rt.SwarmRunner != nil && newSwarmRunner != nil {
		*rt.SwarmRunner = *newSwarmRunner
	}
	if rt.SDDRunner != nil && newSDDRunner != nil {
		*rt.SDDRunner = *newSDDRunner
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
	rt.JobManager = newJobMgr
	rt.DesktopCloser = newDesktopCloser
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
	_ = oldSnap
	if oldDesktopCloser != nil {
		oldDesktopCloser()
	}
	return cleanupErr
}

func runProgram(ctx context.Context, model tea.Model, output io.Writer) error {
	program := tea.NewProgram(model,
		tea.WithOutput(output),
		tea.WithContext(ctx),
	)
	_, err := program.Run()
	return err
}

// loadFileIndexPaths fetches the repo's known file paths for the
// completion popup. Returns nil on any error (no DB, no rows, query
// failure) so callers can treat absence as "skip the eager seed".
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
