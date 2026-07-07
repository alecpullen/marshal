package app

import (
	"context"
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

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/commands"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/knowledge"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
	"marshal/internal/tools/mcp"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

type ProgramRunner func(ctx context.Context, model tea.Model, output io.Writer) error
type configLoader func(config.LoadOptions) (config.Config, error)

type options struct {
	now            func() time.Time
	configLoader   configLoader
	programRunner  ProgramRunner
	skipOnboarding bool
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

func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, error) {
	resolver := newRoutedProviderResolver(cfg)
	route, resolvedProvider, err := resolver.Resolve(routing.TaskProfile{Class: "edit"})
	if err != nil {
		return nil, nil, nil, nil, err
	}

	reg := registry.New()
	if err := native.RegisterAll(reg, native.Options{
		WorkspaceRoot: state.WorkingDir,
		TestCommand:   cfg.Commands.Test,
		SessionState:  state,
		DB:            database,
		ProjectID:     projectID,
	}); err != nil {
		return nil, nil, nil, nil, err
	}

	skills.RegisterTool(reg, skillIndex, state)

	var mcpMgr *mcp.Manager
	if len(cfg.MCP.Servers) > 0 {
		mcpMgr = mcp.NewManager(&cfg)
		if err := mcpMgr.Start(ctx); err != nil {
			return nil, nil, nil, nil, err
		}
		if err := mcpMgr.RegisterTools(reg); err != nil {
			mcpMgr.Close()
			return nil, nil, nil, nil, err
		}
	}

	pol := policy.NewEngine(&cfg, state.SessionRules())
	runner := agent.NewRunner(resolvedProvider, reg, pol, state, route.Preset.Model)
	runner.SkillIndex = skillIndex
	runner.RouteResolver = resolver
	runner.MemoryProvider = &dbMemoryProvider{db: database}
	runner.ProjectID = projectID
	runner.MetricsObserver = metricsRecorder(database, projectID, state.SessionID(), state.Logger())
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
	if runner.RequestTimeout == 0 {
		runner.RequestTimeout = 60 * time.Second
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
	swarmRunner := buildSwarmRunner(ctx, cfg, state, reg, pol, resolver, database, projectID, skillIndex)
	return runner, reg, swarmRunner, mcpMgr, nil
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
		return r, nil
	}
	o := swarm.New(state, factory)
	o.MaxFixRounds = cfg.Swarm.Budget.MaxFixRounds
	o.MaxTotalTokens = cfg.Swarm.Budget.MaxTotalTokens
	o.NewMeter = func() swarm.TokenMeter { return swarm.NewEstimateMeter() }
	return o
}

// roleToolIterations returns the per-role tool-iteration cap, falling back
// to the agent-wide cap when no role-specific value is configured.
func roleToolIterations(cfg config.Config, role agent.AgentRole) int {
	if n, ok := cfg.Swarm.Budget.ToolIters[string(role)]; ok && n > 0 {
		return n
	}
	return cfg.Agent.MaxToolIterations
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
		if caps.JSONMode {
			return actionDecodingConfig{ResponseFormat: &schema.ResponseFormat{Type: "json_object"}}
		}
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

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find working directory: %w", err)
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

	cfg, err := runOpts.configLoader(config.LoadOptions{WorkingDir: workingDir})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(workingDir, ".marshal"), 0755); err != nil {
		return fmt.Errorf("create .marshal directory: %w", err)
	}

	database, err := db.Open(dbPath(workingDir))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	projectID, err := database.GetOrCreateProject(workingDir, cfg.Project.Name)
	if err != nil {
		return fmt.Errorf("get or create project: %w", err)
	}

	sessionID := fmt.Sprintf("sess_%d", runOpts.now().UnixNano())
	if err := database.CreateSession(sessionID, projectID, "", runOpts.now()); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	// The TUI owns the terminal via Bubble Tea's alt-screen (both stdout and
	// stderr point at the same TTY), so any log line written there corrupts the
	// rendered frame. Send logs to a file instead; fall back to discarding them
	// rather than ever writing to the live screen.
	logWriter := io.Discard
	if logFile, err := os.OpenFile(logPath(workingDir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		defer logFile.Close()
		logWriter = logFile
	}
	logger := logging.New(logWriter, slog.LevelInfo)
	state := session.New(cfg, workingDir, runOpts.now(), session.Persistence{DB: database, SessionID: sessionID, Logger: logger})

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = ""
	}
	globalSkillsDir := filepath.Join(homeDir, ".config", "marshal", "skills")
	projectSkillsDir := filepath.Join(workingDir, ".marshal", "skills")
	skillIndex, err := skills.LoadSkills(globalSkillsDir, projectSkillsDir)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}

	var runner *agent.Runner
	var toolReg *registry.Registry
	var swarmRunner *swarm.Orchestrator
	var mcpMgr *mcp.Manager
	runner, toolReg, swarmRunner, mcpMgr, err = buildAgentRunner(ctx, cfg, state, database, projectID, skillIndex)
	if err == nil {
		defer func() {
			if mcpMgr != nil {
				_ = mcpMgr.Close()
			}
		}()
	}

	cmdReg := commands.New()
	if err == nil {
		if err := commands.RegisterAll(cmdReg, toolReg); err != nil {
			return fmt.Errorf("register commands: %w", err)
		}
	}

	var tuiOpts []tui.Option
	tuiOpts = append(tuiOpts, tui.WithMemoryStore(database, projectID))
	tuiOpts = append(tuiOpts, tui.WithCommandRegistry(cmdReg))
	if err == nil {
		tuiOpts = append(tuiOpts, tui.WithRunner(ctx, runner))
		tuiOpts = append(tuiOpts, tui.WithSwarmRunner(ctx, swarmRunner))
		configReloader := func(newCfg config.Config) error {
			state.Config = newCfg
			return reloadAgentRuntime(ctx, newCfg, state, database, projectID, skillIndex, runner, swarmRunner, &mcpMgr)
		}
		tuiOpts = append(tuiOpts, tui.WithConfigReloader(configReloader))
	} else {
		state.SetProviderError(err)
	}
	done := make(chan struct{})
	defer close(done)
	defer state.Shutdown()
	go func() {
		select {
		case <-ctx.Done():
			state.Shutdown()
		case <-done:
		}
	}()

	logger.Info("marshal started", "project", cfg.Project.Name, "working_dir", workingDir)

	select {
	case <-ctx.Done():
		return nil
	default:
	}

	progErr := runOpts.programRunner(ctx, tui.New(state, tuiOpts...), stdout)
	knowledgeCtx, cancelKnowledge := context.WithTimeout(context.Background(), shutdownKnowledgeTimeout)
	defer cancelKnowledge()
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
	return progErr
}

func reloadAgentRuntime(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, runner *agent.Runner, swarmRunner *swarm.Orchestrator, activeMCP **mcp.Manager) error {
	if runner == nil {
		return nil
	}
	newRunner, _, newSwarmRunner, newMCP, err := buildAgentRunner(ctx, cfg, state, database, projectID, skillIndex)
	if err != nil {
		return err
	}
	if activeMCP != nil && *activeMCP != nil {
		_ = (*activeMCP).Close()
	}
	*runner = *newRunner
	if swarmRunner != nil && newSwarmRunner != nil {
		*swarmRunner = *newSwarmRunner
	}
	if activeMCP != nil {
		*activeMCP = newMCP
	} else if newMCP != nil {
		_ = newMCP.Close()
	}
	return nil
}

func runProgram(ctx context.Context, model tea.Model, output io.Writer) error {
	program := tea.NewProgram(model,
		tea.WithOutput(output),
		tea.WithContext(ctx),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := program.Run()
	return err
}
