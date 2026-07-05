package app

import (
	"context"
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
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

type ProgramRunner func(ctx context.Context, model tea.Model, output io.Writer) error
type configLoader func(config.LoadOptions) (config.Config, error)

type options struct {
	now           func() time.Time
	configLoader  configLoader
	programRunner ProgramRunner
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

func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, error) {
	resolver := newRoutedProviderResolver(cfg)
	route, resolvedProvider, err := resolver.Resolve(routing.TaskProfile{Class: "edit"})
	if err != nil {
		return nil, nil, nil, err
	}

	reg := registry.New()
	if err := native.RegisterAll(reg, native.Options{
		WorkspaceRoot: state.WorkingDir,
		TestCommand:   cfg.Commands.Test,
		SessionState:  state,
		DB:            database,
		ProjectID:     projectID,
	}); err != nil {
		return nil, nil, nil, err
	}

	skills.RegisterTool(reg, skillIndex, state)

	pol := policy.NewEngine(&cfg, state.SessionRules())
	runner := agent.NewRunner(resolvedProvider, reg, pol, state, route.Preset.Model)
	runner.SkillIndex = skillIndex
	runner.RouteResolver = resolver
	runner.MemoryProvider = &dbMemoryProvider{db: database}
	runner.ProjectID = projectID
	if route.Preset.ToolCalling == "json" && resolvedProvider.Capabilities(ctx).JSONMode {
		runner.ResponseFormat = &schema.ResponseFormat{Type: "json_object"}
	}
	if cfg.Agent.MaxToolIterations > 0 {
		runner.MaxToolIterations = cfg.Agent.MaxToolIterations
	}
	if cfg.Agent.MaxRetries > 0 {
		runner.MaxRetries = cfg.Agent.MaxRetries
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
	return runner, reg, swarmRunner, nil
}

// buildSwarmRunner wires the Milestone O swarm: every role runner shares
// the session state, policy engine, and one WriteLock; read-only roles get
// the filtered registry view; each role's provider/model comes from the
// routing profile via ResolveRole (falling back to the implementer preset
// for unconfigured roles).
func buildSwarmRunner(ctx context.Context, cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index) *swarm.Orchestrator {
	readOnlyReg := registry.ReadOnlyView(reg)
	gate := &swarm.WriteLock{}
	memory := &dbMemoryProvider{db: database}

	factory := func(role agent.AgentRole, readOnly bool) (*agent.Runner, error) {
		// agent.AgentRole and routing.AgentRole share string values
		// ("planner", "repo_scout", "implementer", "reviewer").
		route, p, err := resolver.ResolveRole(routing.AgentRole(role))
		if err != nil {
			return nil, err
		}
		toolReg := reg
		if readOnly {
			toolReg = readOnlyReg
		}
		r := agent.NewRunner(p, toolReg, pol, state, route.Preset.Model)
		r.Role = role
		r.WriteGate = gate
		r.SkillIndex = skillIndex
		r.MemoryProvider = memory
		r.ProjectID = projectID
		r.RequestTimeout = 60 * time.Second
		// Swarm role prompts embed the shared plan, so skip the per-turn
		// classify/plan pass (class "question" bypasses planning).
		r.SetForceClass("question")
		if route.Preset.ToolCalling == "json" && p.Capabilities(ctx).JSONMode {
			r.ResponseFormat = &schema.ResponseFormat{Type: "json_object"}
		}
		if cfg.Agent.MaxToolIterations > 0 {
			r.MaxToolIterations = cfg.Agent.MaxToolIterations
		}
		if cfg.Agent.MaxRetries > 0 {
			r.MaxRetries = cfg.Agent.MaxRetries
		}
		return r, nil
	}
	return swarm.New(state, factory)
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

	logger := logging.New(stderr, slog.LevelInfo)
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
	runner, toolReg, swarmRunner, err = buildAgentRunner(ctx, cfg, state, database, projectID, skillIndex)

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
			if runner == nil {
				return nil
			}
			resolver := newRoutedProviderResolver(newCfg)
			route, p, err := resolver.Resolve(routing.TaskProfile{Class: "edit"})
			if err != nil {
				return err
			}
			runner.Provider = p
			runner.Model = route.Preset.Model
			runner.RouteResolver = resolver
			if route.Preset.ToolCalling == "json" && p.Capabilities(ctx).JSONMode {
				runner.ResponseFormat = &schema.ResponseFormat{Type: "json_object"}
			} else {
				runner.ResponseFormat = nil
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
			return nil
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

func runProgram(ctx context.Context, model tea.Model, output io.Writer) error {
	program := tea.NewProgram(model, tea.WithOutput(output), tea.WithContext(ctx), tea.WithAltScreen())
	_, err := program.Run()
	return err
}
