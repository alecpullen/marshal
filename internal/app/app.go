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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/clipboard"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/contextpack"
	"marshal/internal/credentials"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/hooks"
	"marshal/internal/index"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/provider/oauthworker"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/lsp"
	"marshal/internal/oauth"
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
	"marshal/internal/watch"
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
	now                    func() time.Time
	configWithLayersLoader func(config.LoadOptions) (config.Config, config.Layers, error)
	programRunner          ProgramRunner
	trustResolver          trust.Resolver
	// deferTrustPrompt moves the folder-trust question into the TUI (the
	// interactive path). sessionTrusted carries an inline session-trust
	// answer across the reload loop.
	deferTrustPrompt  bool
	sessionTrusted    bool
	workingDir        string
	homeDir           string
	sessionID         string
	existingSessionID string
	additionalDirs    []string
	shutdownHook      func(ctx context.Context, state *session.State, database *db.DB)
	workers           []worker.Worker
	configReloader    func(config.Config) error
	sessionRouting    config.SessionRouting
	// hosts is the process's project-host registry (WithHosts). Nil gives the
	// runtime a private one.
	hosts *Hosts
	// authResolver is the credential/OAuth dependency the runtime hands to
	// every credential-backed consumer it builds: provider construction, the
	// OAuth refresh workers and the MCP managers. authResolverSet records an
	// explicit injection (including an explicitly nil one) so an omitted
	// option can select the production default without masking a caller's
	// nil — which is rejected rather than silently swapping in the real
	// OS keychain.
	authResolver    provider.AuthResolver
	authResolverSet bool
}

type Option func(*options)

// ErrInvalidSessionRouting is returned (wrapped) by StartRuntime when the
// routing given to WithSessionRouting names an unknown profile, role or
// preset.
var ErrInvalidSessionRouting = errors.New("invalid session routing")

// ErrAuthResolverRequired is returned by StartRuntime when a caller injects
// an explicitly nil provider.AuthResolver. An omitted option means "use the
// production default"; an explicit nil is a bug in the caller, and treating
// it as "default" would silently point an isolated runtime at the real OS
// keychain.
var ErrAuthResolverRequired = errors.New("app: WithAuthResolver requires a non-nil auth resolver")

// WithAuthResolver pins the credential/OAuth dependency the runtime uses for
// every credential-backed construction: provider building (including Codex
// OAuth engines), the per-provider token-refresh workers, and the MCP
// managers' OAuth token stores. Production omits it, which selects
// provider.DefaultAuthResolver(); tests inject an isolated resolver so no
// part of startup or rebuild can reach the developer's real keychain.
//
// Passing an explicitly nil resolver is rejected at StartRuntime rather than
// quietly falling back to the production default.
func WithAuthResolver(auth provider.AuthResolver) Option {
	return func(opts *options) {
		opts.authResolver = auth
		opts.authResolverSet = true
	}
}

// resolveAuthResolver picks the effective resolver for one runtime. An
// omitted option selects the production default; an explicitly injected
// (therefore nil) resolver is an error.
func resolveAuthResolver(opts options) (provider.AuthResolver, error) {
	if !opts.authResolverSet {
		return provider.DefaultAuthResolver(), nil
	}
	if opts.authResolver == nil {
		return nil, ErrAuthResolverRequired
	}
	return opts.authResolver, nil
}

// WithSessionRouting gives the runtime's session its own routing: profile
// (when non-empty) replaces the configured default profile, and overrides
// rebind roles to presets. Both apply for the life of the session, including
// across config reloads. The TUI path never sets it.
func WithSessionRouting(profile string, overrides map[routing.AgentRole]string) Option {
	return func(opts *options) {
		opts.sessionRouting = config.SessionRouting{Profile: profile, Overrides: overrides}
	}
}

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
		opts.configWithLayersLoader = func(lo config.LoadOptions) (config.Config, config.Layers, error) {
			cfg, err := loader(lo)
			if err != nil {
				return config.Config{}, config.Layers{}, err
			}
			layers := config.Layers{Default: cfg, User: cfg, Merged: cfg}
			return cfg, layers, nil
		}
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

// WithShutdownHook registers a callback invoked during TUI exit after the
// runtime has quiesced and the session is marked ended, but before the
// runtime closes. Tests use it to observe the shutdown window.
func WithShutdownHook(hook func(ctx context.Context, state *session.State, database *db.DB)) Option {
	return func(opts *options) {
		opts.shutdownHook = hook
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

// indexEventCompleted is published on Runtime.IndexBroker after every
// successful index pass (startup scan or watcher rerun).
const indexEventCompleted = "index.completed"

// namedWorker adapts a function to the worker.Worker contract for production
// background workers. (app_test.go defines its own workerFunc for the test
// seam; the names must not collide.)
type namedWorker struct {
	name string
	run  func(ctx context.Context) error
}

func (w namedWorker) Name() string                  { return w.name }
func (w namedWorker) Run(ctx context.Context) error { return w.run(ctx) }

// WithHomeDir overrides the home directory used to resolve the user config
// and data directories. When empty, the process's real home directory is
// used. Intended for tests so config loads (and the project→user hoist) do
// not touch the developer's real user config.
func WithHomeDir(dir string) Option {
	return func(opts *options) {
		opts.homeDir = dir
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

func logPath(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "marshal.log")
}

type routedProviderResolver struct {
	router    *routing.StaticRouter
	cfg       config.Config
	dataDir   string
	mu        *sync.Mutex // guards providers; swarm may resolve roles from concurrent paths
	providers map[string]provider.Provider
	sessionID string
	// auth is the credential/OAuth dependency every provider this resolver
	// builds must use. It is never nil: the runtime resolves the default
	// once at its construction boundary (see resolveAuthResolver) and every
	// copy below carries the same value forward, so no rebuild can fall back
	// to the process-wide default after an isolated startup.
	auth provider.AuthResolver
}

// dbMemoryProvider adapts stored project memories for context-pack
// injection, excluding memories that have been marked stale.
type dbMemoryProvider struct {
	db *db.DB
	// workspace is $MARSHAL_WORKSPACE, read once at construction, so
	// workspace-scoped memories reach agents the bridge spawned into one.
	workspace string
}

func newDBMemoryProvider(database *db.DB) *dbMemoryProvider {
	return &dbMemoryProvider{db: database, workspace: os.Getenv("MARSHAL_WORKSPACE")}
}

// newRoutedProviderResolver builds the resolver for one runtime generation.
//
// auth is the runtime's selected credential/OAuth dependency. The runtime
// paths always pass one explicitly (see resolveAuthResolver). The parameter is
// variadic only so call sites outside this change — test fixtures that predate
// injection — keep compiling: an omitted resolver selects
// unmappedAuthResolver, which fails every credential entry point rather than
// falling back to the production OS keychain.
func newRoutedProviderResolver(cfg config.Config, dataDir string, auth ...provider.AuthResolver) *routedProviderResolver {
	return &routedProviderResolver{
		router:    routing.NewStaticRouter(cfg.RoutingConfig()),
		cfg:       cfg,
		dataDir:   dataDir,
		mu:        &sync.Mutex{},
		providers: make(map[string]provider.Provider),
		sessionID: "",
		auth:      effectiveAuthResolver(firstAuthResolver(auth)),
	}
}

// firstAuthResolver returns the explicitly supplied resolver, or nil when the
// caller omitted one.
func firstAuthResolver(auth []provider.AuthResolver) provider.AuthResolver {
	if len(auth) == 0 {
		return nil
	}
	return auth[0]
}

// effectiveAuthResolver returns auth when one was selected, and a
// fail-closed resolver otherwise. It never substitutes the production
// default: a code path that forgot to thread the resolver must fail loudly
// before it can open the developer's real credential store.
func effectiveAuthResolver(auth provider.AuthResolver) provider.AuthResolver {
	if auth != nil {
		return auth
	}
	return unmappedAuthResolver{}
}

// unmappedAuthResolver is the credential dependency used when no resolver was
// selected. Every entry point refuses, naming what it refused to do, so a
// missed injection surfaces as a provider/MCP failure in the test that
// provoked it instead of reaching the real OS keychain.
type unmappedAuthResolver struct{}

func (unmappedAuthResolver) OpenStore(service string) (credentials.Store, error) {
	return nil, fmt.Errorf("app: no auth resolver was selected; refusing to open the OS credential store for %q", service)
}

func (unmappedAuthResolver) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	return nil, fmt.Errorf("app: no auth resolver was selected; refusing to build the production OAuth engine for %q", name)
}

// withSessionID returns a copy of the resolver that stamps the given stable
// conversation ID onto every OpenAI-compatible provider it builds, as
// x-opencode-session (required by OpenCode Zen for routing/caching). The
// provider cache is shared with the original resolver so the copy can serve
// the same conversation.
func (r *routedProviderResolver) withSessionID(sessionID string) *routedProviderResolver {
	if r.sessionID == sessionID {
		return r
	}
	return &routedProviderResolver{
		router:    routing.NewStaticRouter(r.cfg.RoutingConfig()),
		cfg:       r.cfg,
		dataDir:   r.dataDir,
		mu:        r.mu,
		providers: r.providers,
		sessionID: sessionID,
		auth:      r.auth,
	}
}

// withRoleOverrides returns a new resolver that resolves roles through a
// routing config with the given per-run overrides applied. The provider
// cache is shared with the original resolver so providers are not rebuilt
// per run. The original resolver is not mutated.
func (r *routedProviderResolver) withRoleOverrides(overrides map[routing.AgentRole]string) *routedProviderResolver {
	if len(overrides) == 0 {
		return r
	}
	rc := r.cfg.RoutingConfig()
	for role, preset := range overrides {
		rc = rc.WithRoleOverride(role, preset)
	}
	return &routedProviderResolver{
		router:    routing.NewStaticRouter(rc),
		cfg:       r.cfg,
		dataDir:   r.dataDir,
		providers: r.providers, // shared cache
		mu:        r.mu,        // shared mutex (pointer)
		sessionID: r.sessionID,
		auth:      r.auth,
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
	// The runtime's selected resolver, never the process-wide default: the
	// Codex OAuth engine a provider holds must come from the same resolver the
	// refresh worker and the MCP managers use, so a test runtime cannot reach
	// the developer's keychain and two runtimes cannot share tokens.
	p, err := provider.NewFromConfigWithAuth(route.Preset.Provider, providerConfig, r.dataDir, r.cfg.Privacy.RemoteLimitDiscovery, r.cfg.Agent.ThinkingBudgetMargin, r.sessionID, effectiveAuthResolver(r.auth))
	if err != nil {
		return nil, err
	}
	r.providers[route.Preset.Provider] = p
	return p, nil
}

// Memories returns the curated notes visible to this project: confirmed
// project memories, then workspace and global memories (promoted by a
// person, see db.GetScopedMemories). Tentative project memories — what an
// agent learned on its own — and stale memories are never injected.
func (p *dbMemoryProvider) Memories(projectID int64) ([]contextpack.MemoryNote, error) {
	memories, err := p.db.GetScopedMemories(projectID, p.workspace)
	if err != nil {
		return nil, err
	}
	notes := make([]contextpack.MemoryNote, 0, len(memories))
	for _, m := range memories {
		if m.Confidence == db.MemoryConfidenceStale {
			continue
		}
		curated := m.Confidence == db.MemoryConfidenceConfirmed ||
			m.Scope == db.MemoryScopeWorkspace || m.Scope == db.MemoryScopeGlobal
		if !curated {
			continue
		}
		notes = append(notes, contextpack.MemoryNote{Kind: m.Kind, Content: m.Content, Confidence: m.Confidence, UpdatedAt: m.UpdatedAt})
	}
	return notes, nil
}

// metricsRecorder returns a MetricsObserver that persists each turn's
// metrics. Failures are logged and swallowed: telemetry must never break a
// turn.
func metricsRecorder(database *db.DB, projectID int64, sessionID string, logger *slog.Logger) func(agent.TurnMetrics) {
	return func(m agent.TurnMetrics) {
		_, err := database.InsertTurnMetrics(db.TurnMetricsRow{
			ProjectID:       projectID,
			SessionID:       sessionID,
			StartedAt:       m.StartedAt,
			DurationMs:      m.DurationMs,
			Class:           m.Class,
			Role:            m.Role,
			Provider:        m.Provider,
			Model:           m.Model,
			Goal:            m.Goal,
			Iterations:      m.Iterations,
			ToolCalls:       m.ToolCalls,
			ToolErrors:      m.ToolErrors,
			CacheHits:       m.CacheHits,
			ParseFailures:   m.ParseFailures,
			ParseFailKind:   m.ParseFailKind,
			ParseFailSample: m.ParseFailSample,
			ParseRepairs:    m.ParseRepairs,
			HardStalls:      m.HardStalls,
			// Failure-ladder telemetry: how far the identical-failed-call
			// escalation got, and the longest failed streak behind it.
			FailedRepeatStreak:      m.FailedRepeatStreak,
			HighestFailedRepeatTier: m.HighestFailedRepeatTier,
			IntentNudges:            m.IntentNudges,
			ReasoningReplayedChars:  m.ReasoningReplayedChars,
			ReasoningTrimmed:        m.ReasoningTrimmed,
			Outcome:                 m.Outcome,
			SalvageReason:           m.SalvageReason,
			PromptTokens:            m.PromptTokens,
			CompletionTokens:        m.CompletionTokens,
			ReasoningTokens:         m.ReasoningTokens,
			CacheReadTokens:         m.CacheReadTokens,
			CacheWriteTokens:        m.CacheWriteTokens,
			EstimatedCostCents:      m.EstimatedCostCents,
			EstimatedCostMicroUSD:   m.EstimatedCostMicroUSD,
			QuotaUsedPercent:        quotaUsedPercent(m.Quota),
			QuotaResetAfterSecs:     quotaResetAfterSecs(m.Quota),
			QuotaPlanType:           quotaPlanType(m.Quota),
		})
		if err != nil && logger != nil {
			logger.Warn("failed to persist turn metrics", "error", err)
		}
	}
}

// quotaUsedPercent, quotaResetAfterSecs, and quotaPlanType flatten a quota
// reading for persistence. A nil reading (the common case — most providers
// report no quota) yields zero values.
func quotaUsedPercent(q *schema.QuotaInfo) int {
	if q == nil {
		return 0
	}
	return q.PrimaryUsedPercent
}

func quotaResetAfterSecs(q *schema.QuotaInfo) int {
	if q == nil {
		return 0
	}
	return q.PrimaryResetAfterSecs
}

func quotaPlanType(q *schema.QuotaInfo) string {
	if q == nil {
		return ""
	}
	return q.PlanType
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
// rollover is not effectively enabled for the model's window (see
// RolloverConfig.EffectiveEnabled), it returns nil, nil (no generation rows
// are created). A nil database also yields nil, nil: the controller
// archives generations to the DB and cannot function without one.
// modelContextWindow is the model's full context window in tokens; when >0,
// the controller uses it for context_percent calculations instead of the
// per-turn compaction budget.
// digestProvider is the DigestProvider to use; when nil, minimalDigestProvider
// is used as a safe fallback. usageCounter, when non-nil, is passed to
// ResolveCounter so the "usage" counter can observe provider-reported tokens.
func NewRolloverController(sessionID string, cfg config.RolloverConfig, database *db.DB, localOnly bool, modelContextWindow int, digestProvider rollover.DigestProvider, usageCounter *rollover.UsageCounter) (*rollover.Controller, error) {
	if !cfg.EffectiveEnabled(localOnly, modelContextWindow) || database == nil {
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

// conversationCommandService is the command supervisor ONE conversation owns,
// from admission through settlement, plus the two things a rebuild may update
// on it: the committed launch generation and the live LSP rescan seam.
//
// Ownership is the point. The service is created once per conversation (see
// newConversationCommandService) and every later build — a config reload, a
// child registry, a pipeline dispatch — borrows it. That is what keeps handles,
// ids, the count callback, the job broker and the exit sink stable across a
// reload: a rebuild changes FUTURE launches (its launch defaults) and never
// repoints or closes the supervisor itself.
type conversationCommandService struct {
	manager *native.JobManager
	// generation is the launch generation currently committed to the service.
	// It is bumped only by a successful commit, so a failed candidate build
	// leaves the previous generation in force.
	generation int

	mu sync.Mutex
	// lsp is the live language-server rescan seam. A completion resolves it at
	// settlement time (not at installation), so a job that finishes after a
	// reload re-syncs through the generation that is live now rather than the
	// one that admitted it.
	lsp native.LSPSync
}

// nextGeneration is the generation a build should commit. It is read from the
// SERVICE, not from a caller's counter: a service that was adopted from an
// already-live manager (a Runtime assembled before the service existed, a
// fixture that owns a manager directly) reports the generation it actually has,
// so a rebuild never re-commits a generation number an already-admitted
// execution is using.
func (s *conversationCommandService) nextGeneration() int {
	if s == nil {
		return 1
	}
	if s.manager != nil {
		if gen := s.manager.Generation(); gen >= 0 {
			return gen + 1
		}
	}
	return s.generation + 1
}

// setLiveLSP records the rescan seam of the generation that just became live.
func (s *conversationCommandService) setLiveLSP(sync native.LSPSync) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lsp = sync
	s.mu.Unlock()
}

// rescanLSP announces files a settled command changed on disk. A nil seam (no
// language server configured, or none live yet) is a no-op.
func (s *conversationCommandService) rescanLSP() {
	if s == nil {
		return
	}
	s.mu.Lock()
	sync := s.lsp
	s.mu.Unlock()
	if sync != nil {
		sync.Rescan()
	}
}

// newConversationCommandService creates the conversation's command service and
// installs everything that belongs to its owner: the parent's running-count
// callback, the F19 job broker and the exit sink that makes a detached
// completion durable.
//
// These are installed HERE and nowhere else. A rebuild must not re-install them
// — a child registry or a reload that repointed them would leave the parent's
// evidence and its stop target aimed at a toolset that is being replaced.
func newConversationCommandService(ctx context.Context, cfg config.Config, state *session.State, jobBroker *pubsub.Broker[native.JobEvent]) *conversationCommandService {
	svc := &conversationCommandService{generation: 1}
	svc.manager = native.NewCommandService(ctx, commandLaunchPolicy(cfg, state, 1, nil, nil))
	if state != nil {
		svc.manager.SetOnChange(state.SetRunningJobsCount)
		// The conversation's service is the target an explicit turn stop
		// reaches: the session owns it, so Ctrl+C (or /stop, or a cancelled
		// review/pipeline/SDD turn) can mark that turn's attached commands
		// stopping BEFORE the agent's own waits are cancelled. Installing it
		// once, at owner construction, is what keeps a child registry or a
		// reload from silently replacing the stop target.
		state.SetTurnStopService(svc.manager)
		// The conversation end gate's admission seam. Every command this service
		// admits registers with the session's work membership atomically with its
		// admission decision, so an accepted end can see it (and refuse a launch
		// after the freeze) at the one place every ordinary command passes
		// through. Installed here, at owner construction, and deliberately not
		// re-installed by a rebuild.
		svc.manager.SetWorkAdmission(state)
	}
	if jobBroker != nil {
		svc.manager.SetBroker(jobBroker)
	}
	svc.manager.SetOnExit(func(info native.JobInfo, ran time.Duration, output string) error {
		exit := session.JobExitFromExecution(info.Execution(), info.Command, output, ran, info.ExitCode)
		err := state.AddJobExit(exit)
		// The rescan follows the files the command changed on disk, which
		// happened whether or not the outcome was stored, so it is not gated on
		// the durable write. It resolves the LIVE handle, so a command admitted
		// before a reload still re-syncs through the generation running now.
		svc.rescanLSP()
		if err != nil {
			// Surface the failure and leave the record pending: it is already in
			// the transcript in memory, and the manager retries the durable write.
			state.Logger().Error("job exit was not persisted", "error", err, "job", info.ID, "execution", info.ExecutionID)
			return err
		}
		return nil
	})
	return svc
}

// commandLaunchPolicy renders one generation's launch defaults from config.
// Only the defaults belong here: an execution admitted under the previous
// generation keeps the runner, cwd, output policy and wait it captured, so
// committing this policy re-times and re-limits nothing that is already running.
func commandLaunchPolicy(cfg config.Config, state *session.State, generation int, runner native.CommandRunner, resources []io.Closer) native.LaunchPolicy {
	policy := native.LaunchPolicy{
		Generation:     generation,
		Runner:         runner,
		MaxJobs:        cfg.Tools.Shell.MaxBackgroundJobs,
		Retention:      cfg.Tools.Shell.BackgroundRetention,
		MaxOutputBytes: cfg.Tools.Shell.MaxOutputBytes,
		Resources:      resources,
	}
	if state != nil {
		policy.DirFunc = func() string { return state.Workspace().ActiveRoot }
	}
	return policy
}

// commitLaunchPolicy atomically installs policy as the service's defaults for
// future launches. It is called only after a candidate generation has been
// fully built and validated, so a failed candidate never mutates the service.
func (s *conversationCommandService) commitLaunchPolicy(policy native.LaunchPolicy, liveLSP native.LSPSync) {
	if s == nil || s.manager == nil {
		return
	}
	s.manager.CommitLaunchPolicy(policy)
	s.generation = policy.Generation
	s.setLiveLSP(liveLSP)
}

// buildAgentRunner is the test-facing form: children serialize on a private
// lock. Production uses buildAgentRunnerWithLock so the parent runner and
// every agent.run child share the runtime's stable WriteLock across config
// reloads. (Keeping this wrapper avoids churning the ~25 test call sites.)
//
// An auth fixture passes its resolver as the trailing argument; every other
// fixture omits it and therefore fails closed (see newRoutedProviderResolver)
// instead of reaching the real OS keychain.
func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, dataDir string, additionalDirs []string, jobBroker *pubsub.Broker[native.JobEvent], configReloader func(config.Config) error, homeDir string, auth ...provider.AuthResolver) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Rooted, *native.JobManager, func(), agent.SubagentRunnerFactory, *lsp.Handle, func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner, sddauthor.Factory, tui.SwarmOverrideFactory, error) {
	// The watch manager is owned by the runtime (via buildAgentRunnerWithLock);
	// this wrapper discards it so the ~25 test call sites that use
	// buildAgentRunner directly do not need to change. The LSP handle is the
	// runtime's too: the wrapper wires the tools to an empty one and returns
	// it.
	lspHandle := lsp.NewHandle(nil, nil, state.Logger())
	runner, reg, swarmRunner, mcpMgr, snapSvc, commandService, _, desktopCloser, subagentFactory, pipelineFactory, planAuthorFactory, swarmOverrideFactory, err := buildAgentRunnerWithLock(ctx, cfg, state, database, projectID, skillIndex, dataDir, additionalDirs, jobBroker, nil, nil, configReloader, homeDir, &swarm.WriteLock{}, nil, lspHandle, auth...)
	var jobMgr *native.JobManager
	if commandService != nil {
		jobMgr = commandService.manager
	}
	return runner, reg, swarmRunner, mcpMgr, snapSvc, jobMgr, desktopCloser, subagentFactory, lspHandle, pipelineFactory, planAuthorFactory, swarmOverrideFactory, err
}

// buildAgentRunnerWithLock is the production form of buildAgentRunner. It
// takes the runtime's stable WriteLock so the parent runner and every
// agent.run child serialize writes on the same gate, and so a config reload
// (which rebuilds the runner and the subagent factory) reuses the same lock
// that in-flight background children from the pre-reload generation already
// hold. It also takes the conversation's command service and the runtime's
// selected auth resolver: the service so a rebuild borrows the one supervisor
// the conversation already owns, and the auth resolver as the LAST parameter
// (variadic so pre-existing non-auth call sites keep compiling and fail closed
// instead of reaching the real keychain).
//
// service is the conversation's command service. A nil service means this call
// is the OWNER construction: it creates the service and installs the parent's
// count callback, job broker and exit sink on it. A non-nil service means this
// call is a REBUILD (a config reload, a replacement session) and only borrows
// it: nothing about the service's ownership, identity or callbacks is touched,
// so an explicit turn stop, a background card and an already-admitted command
// all keep pointing at the one supervisor the rebuild found.
//
// Either way, a successful build commits a new launch GENERATION as the
// service's defaults for future launches. That commit is the last thing the
// function does, so a candidate that fails validation leaves the previous
// generation — and the service — exactly as they were.
//
// lspHandle is the runtime's LSP handle, which the tools, the symbol query
// and the diagnostics checker are wired to. The build never creates or
// starts a language server: the runtime points the handle at the project
// host's pooled servers (Runtime.bindLSP). A nil handle wires no LSP.
func buildAgentRunnerWithLock(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index, dataDir string, additionalDirs []string, jobBroker *pubsub.Broker[native.JobEvent], watchBroker *pubsub.Broker[watch.Event], watchResumeCell *WatchResumeHook, configReloader func(config.Config) error, homeDir string, writeLock *swarm.WriteLock, service *conversationCommandService, lspHandle *lsp.Handle, auth ...provider.AuthResolver) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Rooted, *conversationCommandService, *watch.Manager, func(), agent.SubagentRunnerFactory, func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner, sddauthor.Factory, tui.SwarmOverrideFactory, error) {
	authResolver := effectiveAuthResolver(firstAuthResolver(auth))
	resolver := newRoutedProviderResolver(cfg, dataDir, authResolver).withSessionID(state.SessionID())
	route, resolvedProvider, err := resolver.Resolve("edit")
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
	}

	// Resolve rollover enablement against the route once, up front: when
	// session.rollover.enabled is unset, local routes with small windows
	// default on and everything else stays off (see
	// RolloverConfig.EffectiveEnabled). Normalizing here lets every
	// downstream consumer — the usage counter, the controller — read the
	// plain flag. cfg is a value copy, so this does
	// not leak into the stored config or the /settings view. Skipped without
	// a database: the controller archives generations to the DB, so rollover
	// cannot function (and must not start) without one.
	if !cfg.Session.Rollover.EnabledSet && database != nil {
		window, _ := agent.ResolveModelLimits(route.Preset, nil, state.Logger())
		cfg.Session.Rollover.Enabled = cfg.Session.Rollover.EffectiveEnabled(route.Preset.LocalOnly, window)
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
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("build sandbox: %w", sbErr)
	}
	caps := commandRunner.Capabilities()
	state.SetSandboxInfo(session.SandboxInfo{
		Backend:          caps.Backend,
		NetworkIsolation: caps.NetworkIsolation,
	})

	// Record the model discovery cache dir on the session state so the
	// agent roster can enrich itself with fresh probe results (see
	// agent.RenderAgentRosterWithDiscovered). The TUI writes this same
	// cache via /models probing.
	if dataDir != "" {
		state.SetModelCacheDir(dataDir)
	}
	// Record the home directory so in-session layer reloads
	// (config.LoadSessionLayers) can locate the user config layer.
	state.SetHomeDir(homeDir)

	// Milestone Q: the conversation's command service supervises every
	// ordinary command — foreground and background — from admission through
	// settlement. It honours the configured sandbox backend, output limit and
	// concurrency limits, and it is created ONCE per conversation.
	//
	// A rebuild (a config reload, a replacement session, a child registry)
	// borrows the service it is given. It must not create a second one: two
	// supervisors would each hold their own capacity, ids, count callback and
	// settlement map, and an explicit turn stop would reach only the one the
	// registry happened to capture.
	owned := service == nil
	commandService := service
	if commandService == nil {
		commandService = newConversationCommandService(ctx, cfg, state, jobBroker)
	}
	jobManager := commandService.manager
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
	if owned {
		cleanup = append(cleanup, func() {
			sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			jobManager.Shutdown(sc)
		})
	}

	// Milestone W: construct the watch manager from the sandboxed command
	// runner and the job broker. Command samples run through the same
	// sandboxed runner as background jobs; job watches subscribe to the job
	// event stream; fired reports are pushed to the session's watch-report
	// queue (task 5 refines the formatting). OnEvent publishes watch events
	// to the TUI lane broker.
	watchManager := watch.NewManager(ctx, watch.Deps{
		RunSample: func(c context.Context, command, dir string) (string, int, error) {
			res, err := commandRunner.Run(c, native.CommandRequest{
				Command:        command,
				Dir:            dir,
				MaxOutputBytes: cfg.Tools.Shell.MaxOutputBytes,
			})
			return res.Stdout, res.ExitCode, err
		},
		SubscribeJobs: func(c context.Context) (<-chan pubsub.Event[native.JobEvent], func()) {
			if jobBroker == nil {
				return nil, nil
			}
			// The broker auto-unsubscribes on ctx cancellation; the returned
			// func is a no-op that relies on the watch's context to clean up.
			sub := jobBroker.Subscribe(c, pubsub.WithTerminal[native.JobEvent]())
			return sub, func() {}
		},
		JobLookup: func(id string) (native.JobInfo, bool) {
			info, _, err := jobManager.Output(id, 0)
			return info, err == nil
		},
		DirFn: func() string { return state.Workspace().ActiveRoot },
		OnFire: func(r watch.Report) {
			// Format the report and push it to the in-memory watch-report
			// queue, coalesced by watch ID (a watch that fires repeatedly
			// while idle folds into one pending entry carrying the cumulative
			// FiredCount). The runner persists each drained report as a
			// RoleUser message at the next loop-top — the durable copy that
			// buildHistoryMessages replays across restart. Persisting at drain
			// time (rather than at push) is what bounds the persisted
			// transcript the same way the queue is bounded.
			state.PushWatchReport(r.WatchID, watch.Format(r), r.Name, r.Mode == watch.ModeRepeat, r.Resume)
			// Auto-resume (spec §5): the TUI never binds WatchResume, so this
			// is a no-op there; the ACP host binds it per session in its
			// Lookup closure and starts a server-initiated turn when the
			// session is idle. r.Resume gates: non-resume fires invoke
			// nothing.
			if r.Resume {
				watchResumeCell.Invoke(r)
			}
		},
		OnEvent: func(ev watch.Event) {
			if watchBroker != nil {
				watchBroker.Publish("watch", ev)
			}
		},
		// The durable-result sink: a terminal watch fact is stored in the owning
		// conversation's work view. The session identity is captured here, from
		// the State this build was handed, so the sink never dereferences a later
		// runtime State (a reload, a replacement, or a closed conversation).
		ConversationID: state.SessionID(),
		OnTerminal: func(info watch.Info) error {
			return state.SaveWatchWorkResult(watchWorkResult(state.SessionID(), info))
		},
	})
	// Shut the watch manager down BEFORE the job manager (watches may depend
	// on jobs). The cleanup slice runs in reverse order, so appending the
	// watch cleanup after the job cleanup achieves this.
	cleanup = append(cleanup, func() {
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		watchManager.Close(sc)
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
		// The root runtime is the one transport-attached registration: the TUI
		// and ACP both supply an access resolver, so mode.request may publish a
		// capability request here. Child registrations below deliberately leave
		// this false — a subagent must ask its parent, and the handler and the
		// session both reject depth > 0 as well.
		AccessRequests: true,
		DB:             database,
		ProjectID:      projectID,
		FileTracker:    fileTracker,
		Config:         cfg,
		JobManager:     jobManager,
		JobBroker:      jobBroker,
		Guardrail:      func(cmd string, system bool) error { return pol.GuardrailCheck(cmd, system) },
		SudoPolicy:     pol.CheckPrivileged,
	}
	if len(additionalDirs) > 0 {
		nativeOpts.AdditionalRoots = additionalDirs
	}
	// Wire the LSP adapters BEFORE RegisterAll so the toolSet and
	// diagnostics checker receive non-nil LSP fields.
	// symbols answers through the language servers and falls back to the
	// symbol index, so it is wired unconditionally; lspHandle may be nil.
	nativeOpts.Symbols = lsp.NewSymbolQuery(lspHandle, database, projectID)
	var lspSync native.LSPSync
	if lspHandle != nil {
		lspSync = lsp.NewSync(lspHandle)
		nativeOpts.LSPSync = lspSync
		nativeOpts.LSPIndex = lsp.NewSymbolAdapter(lspHandle)
	}
	if err := native.RegisterAll(reg, nativeOpts); err != nil {
		buildErr = err
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
	}
	skills.RegisterTool(reg, skillIndex, state, skills.SkillsToolOptions{
		HomeDir:    homeDir,
		WorkingDir: state.WorkingDir,
	})

	var mcpMgr *mcp.Manager
	if len(cfg.MCP.Servers) > 0 {
		// The manager's OAuth token store comes from the runtime's selected
		// resolver, so a remote MCP server's tokens land where the rest of
		// this runtime reads them — and a test runtime never reaches the
		// developer's keychain. authResolver.OpenStore never falls back to
		// credentials.New, so an injected resolver's error stays local and a
		// manager built without one refuses rather than swapping stores.
		mcpMgr = mcp.NewManager(&cfg, mcp.WithManagerLogger(state.Logger()), mcp.WithCredentialFactory(authResolver.OpenStore))
		// A server that will not start is a per-server failure, not a fatal
		// one: the agent must still run, with the healthy servers' tools and
		// a notice naming what was skipped. Aborting here used to leave the
		// runtime with a nil runner, so one bad MCP entry cost every turn.
		if failures := mcpMgr.Start(ctx); len(failures) > 0 {
			state.SetNotice(session.Notice{
				Category: session.NoticeConfig,
				Severity: session.SeverityWarn,
				Message:  mcpFailureMessage(failures),
				Hint:     "Fix or remove the failing server in /settings → MCP, then apply any settings change to retry.",
				Source:   "mcp",
			})
		}
		cleanup = append(cleanup, func() { _ = mcpMgr.Close() })
		if err := mcpMgr.RegisterTools(reg); err != nil {
			buildErr = err
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
		}
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentPricing := pricing.Lookup(route.Preset, state.Logger())
	// One write lock shared by the parent runner and every agent.run child:
	// background children outlive the spawning turn, so parent and child
	// writes must serialize on the same gate. The lock is the caller's
	// (buildAgentRunnerWithLock passes the runtime's stable lock so a config
	// reload reuses the same gate in-flight children already hold).
	subagentFactory, subagentResolver := buildSubagentFactoryWithLock(cfg, state, resolvedProvider, reg, pol, route.Preset.Model, router, resolver, database, projectID, parentPricing, writeLock, nativeOpts, watchManager, commandService)
	if err := reg.Register(agent.NewSubagentTool(
		subagentFactory,
		subagentResolver,
		reg,
		state,
		agent.WithSubagentParentModel(route.Preset.Model, parentPricing),
		agent.WithSubagentParentProvider(resolvedProvider.Name()),
	)); err != nil {
		buildErr = err
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.run: %w", err)
	}
	// agent.await and agent.kill both cover the same two classes of
	// background work: subagents and shell jobs. Both take the job adapter so
	// await can wait on (or peek at) a job and kill can terminate one.
	if err := reg.Register(agent.NewSubagentAwaitTool(state,
		agent.WithAwaitJobs(jobAwaitAdapter{m: jobManager}),
	)); err != nil {
		buildErr = err
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.await: %w", err)
	}
	if err := reg.Register(agent.NewSubagentKillTool(state,
		agent.WithAwaitJobs(jobAwaitAdapter{m: jobManager}),
	)); err != nil {
		buildErr = err
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.kill: %w", err)
	}
	// agent.steer sends an instruction into a running subagent. Main agent
	// only: SubtaskScopeView excludes it, so a child cannot steer a sibling.
	if err := reg.Register(agent.NewSubagentSteerTool(state)); err != nil {
		buildErr = err
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register agent.steer: %w", err)
	}
	runner := agent.NewRunner(resolvedProvider, reg, pol, state, route.Preset.Model)
	runner.WriteGate = writeLock
	// Send now: this is the conversation's ROOT/general runner, the one turn
	// every operation beneath it belongs to, so it is the only runner built
	// here that opts in. The depth/role predicate inside RunTask stays
	// authoritative — a child, SDD or swarm runner never enables the flag, so
	// enabling it on the root cannot make nested work steerable.
	runner.EnableSteering = true
	// The interruption handler is the conversation-owned manager captured BY
	// VALUE here. An OWNER build (a fresh conversation: the startup build, a
	// replacement session) installs it, so the State's handler always names the
	// manager that was built for the same conversation. A rebuild borrows the
	// existing service instead, so it leaves the binding exactly as it was and
	// a reload can never repoint a live turn's handler. Capturing the pointer
	// rather than a closure over runtime.State is what guarantees an old bound
	// handler can never address a replacement conversation's work.
	if owned && jobManager != nil {
		state.SetOperationInterrupt(jobManager.InterruptOperation)
	}
	// The execution-control seam is what gives an explicit turn stop a precise
	// target: the session owns the conversation's command service and the stop
	// registration, so the parent runner and every synthetic worker share the
	// one target a user's stop names.
	runner.ExecutionControl = agent.NewSessionExecutionControl(state)
	// Wire the watch manager as the runner's WatchTransferrer so a
	// subagent's once watches re-parent to the parent and its repeat
	// watches stop when the subagent finishes or is killed.
	runner.WatchTransferrer = watchManager
	repoInstructions, _ := loadRepoInstructions(state.WorkingDir)
	runner.SystemPromptAddendum = composeAddendum(repoInstructions, "")
	runner.SkillIndex = skillIndex
	runner.RouteResolver = resolver
	runner.MemoryProvider = newDBMemoryProvider(database)
	runner.ProjectID = projectID
	recorder := metricsRecorder(database, projectID, state.SessionID(), state.Logger())
	runner.MetricsObserver = recorder
	runner.Pricing = parentPricing

	limitsTable := loadLimitsTable(ctx, cfg, dataDir)
	runner.LimitsTable = limitsTable

	// Defensive: align runner mode with policy engine in case the runner
	// ever copies the engine instead of sharing it.
	if runner.Policy != nil && runner.Policy.ApprovalMode() != parseApprovalMode(cfg.Agent.ApprovalMode) {
		runner.SetApprovalMode(parseApprovalMode(cfg.Agent.ApprovalMode))
	}

	// T17: wire UsageCounter so the rollover controller can use
	// provider-reported prompt_tokens as the numerator for context_percent
	// (branch review finding #2). The counter itself is allocated just
	// before the controller below, once the model window is known — the
	// small-window default makes enablement window-dependent. The observer
	// reads the variable at call time, long after allocation.
	var usageCounter *rollover.UsageCounter
	runner.UsageObserver = func(usage schema.TokenUsage) {
		state.SetTurnUsage(usage.PromptTokens + usage.CompletionTokens)
		if usageCounter != nil {
			usageCounter.Observe(usage.PromptTokens)
		}
	}

	// Subscription quota from OAuth-backed providers. Recorded on the
	// session so the footer and /agents can render it; providers that
	// report no quota never call this, so the value stays nil for them.
	runner.QuotaObserver = func(q schema.QuotaInfo) {
		state.SetTurnQuota(&q)
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
	// 0 means unlimited (the default); budget.go honors base <= 0 as no
	// ceiling. Pass it through unconditionally so the config can also
	// re-disable a cap after one was set.
	runner.MaxToolIterations = cfg.Agent.MaxToolIterations
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
	runner.NarrationPrompt = cfg.Agent.NarrationPrompt
	runner.IntentNudge = cfg.Agent.IntentNudge
	runner.SuppressParseRepairFeedback = !cfg.Agent.ParseRepairFeedbackEnabled()
	runner.VerificationGate = cfg.Agent.VerificationGateEnabled()
	if cfg.Agent.ReconnectMaxWaitSeconds > 0 {
		runner.ReconnectMaxWait = time.Duration(cfg.Agent.ReconnectMaxWaitSeconds) * time.Second
	}

	// T17: wire rollover controller into the runner when enabled.
	// Pass the model's full context window so context_percent fires against
	// the correct denominator (branch review finding #7).
	// Use LLMSummaryProvider as the primary digest provider, falling back to
	// minimalDigestProvider only when the runner's provider is not available
	// (branch review finding #1).
	modelCtxWindow, _ := agent.ResolveModelLimits(route.Preset, runner.LimitsTable, state.Logger())
	// Allocate the usage counter (declared above) once enablement can be
	// resolved against the model window.
	if cfg.Session.Rollover.EffectiveEnabled(route.Preset.LocalOnly, modelCtxWindow) && cfg.Session.Rollover.TokenCounter == "usage" {
		usageCounter = rollover.NewUsageCounter()
	}
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
	if rolloverCtrl, rerr := NewRolloverController(state.SessionID(), cfg.Session.Rollover, database, route.Preset.LocalOnly, modelCtxWindow, digestProvider, usageCounter); rerr != nil {
		buildErr = rerr
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("new rollover controller: %w", rerr)
	} else if rolloverCtrl != nil {
		runner.Rollover = &agent.Rollover{
			Controller: rolloverCtrl,
			State:      state,
		}
		// Start generation 0.
		if err := rolloverCtrl.Start(ctx); err != nil {
			buildErr = err
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("rollover start: %w", err)
		}
		// Record generation 0 in session state.
		genID, genSeq, genSeed := rolloverCtrl.Current()
		state.BeginGeneration(genID, genSeq, genSeed)
	}

	var snapSvc *snapshot.Rooted
	if dataDir != "" && cfg.Snapshots.Enabled {
		snapSvc = snapshot.NewRooted(dataDir, state.WorkingDir,
			func() string { return state.Workspace().ActiveRoot },
			int64(cfg.Snapshots.MaxFileBytes), cfg.Indexing.Ignore, state.Logger(),
			// The ceilings and the user-global reader are installed here so a
			// sweep enforces exactly what a capture admits against. The reader
			// is consulted under the store lock on every decision, so a limit
			// the user lowers takes effect without restarting this process.
			snapshot.WithRootedLimits(snapshotLimits(cfg)),
			snapshot.WithRootedGlobalLimits(snapshotGlobalLimitsReader(homeDir, cfg)))
		state.SetSnapshotter(snapSvc)
		runner.Snapshotter = snapSvc
		// The recorder is wired here so the pre-write hook can record a
		// PUBLISHED capture. It is only ever invoked with a real hash, so a
		// store that declines or skips a capture can never leave a row
		// implying the snapshot exists.
		runner.SnapshotRecorder = database

		// Startup maintenance is NOT run here: this builder also runs for every
		// new/resumed session and config reload, and a sweep blocked each of
		// them. startRuntime runs it once per runtime, in the background (see
		// Runtime.startSnapshotMaintenance).
	}

	state.SetActiveRoute(session.RouteInfo{
		Role:             route.Role,
		Profile:          route.Profile,
		Preset:           route.Preset.Name,
		Provider:         route.Preset.Provider,
		Model:            route.Preset.Model,
		LocalOnly:        route.Preset.LocalOnly,
		Thinking:         route.Preset.Thinking,
		ReasoningCapable: resolvedProvider.Capabilities(ctx).Reasoning,
		Active:           true,
	})

	// F13 + re-titling: wire the turn-start title manager. The call is
	// sequential (runs before the main request), so it is safe even when the
	// title role resolves to the same provider+model as the active turn
	// route. With no resolvable title route the manager stays nil: sessions
	// remain untitled and /rename still works.
	if cfg.Titling.Enabled {
		if titleRoute, titleProvider, titleErr := resolver.ResolveRole(routing.RoleTitle); titleErr == nil && titleRoute.Preset.Model != "" && titleProvider != nil {
			runner.TitleManager = agent.NewTitleManager(titleProvider, titleRoute.Preset.Model, state, time.Duration(cfg.Titling.TimeoutMs)*time.Millisecond)
		}
	}

	// AI-07: wire the router-role classifier for keyword-miss goals. Unlike
	// the title manager above, this call runs inside the turn, so it must be
	// skipped when the router role resolves to the same provider+model as the
	// active turn route — a single-model local backend cannot interleave two
	// calls. The title manager needs no such skip: its call is sequential and
	// completes before the turn's first request.
	if routerRoute, routerProvider, routerErr := resolver.ResolveRole(routing.RoleRouter); routerErr == nil && routerRoute.Preset.Model != "" {
		if routerRoute.Preset.Provider == route.Preset.Provider && routerRoute.Preset.Model == route.Preset.Model {
			runner.Classifier = nil
		} else if routerProvider != nil {
			runner.Classifier = agent.NewModelClassifier(routerProvider, routerRoute.Preset.Model)
		}
	}
	swarmRunner := buildSwarmRunner(cfg, state, reg, pol, resolver, database, projectID, skillIndex, limitsTable)
	// swarmOverrideFactory builds a per-run RunnerFactory with role→preset
	// overrides applied to a copy of the routing config. The provider cache
	// is shared with the startup resolver so providers are not rebuilt per
	// run. The TUI calls this when the castlist confirms with overrides,
	// and RestoreRunner undoes the wrap when the run ends.
	swarmOverrideFactory := func(overrides map[routing.AgentRole]string) swarm.RunnerFactory {
		perRunResolver := resolver.withRoleOverrides(overrides)
		spec := roleRunnerSpec{
			cfg:              cfg,
			state:            state,
			pol:              pol,
			resolver:         perRunResolver,
			reg:              reg,
			readOnlyReg:      registry.ReadOnlyView(reg),
			testerReg:        registry.TesterView(reg),
			skillIndex:       skillIndex,
			memory:           newDBMemoryProvider(database),
			projectID:        projectID,
			limitsTable:      limitsTable,
			database:         database,
			writeGate:        &swarm.WriteLock{},
			metricsObserver:  metricsRecorder(database, projectID, state.SessionID(), state.Logger()),
			applyAgentLimits: true,
		}
		return spec.newRunner
	}

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
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("register desktop tools: %w", err)
		}
		desktopCloser = closer
	}

	subagentFactory, _ = buildSubagentFactoryWithLock(cfg, state, resolvedProvider, reg, pol, route.Preset.Model, router, resolver, database, projectID, pricing.Lookup(route.Preset, state.Logger()), writeLock, nativeOpts, watchManager, commandService)
	pipelineFactory := func(planPath string, overrides map[routing.AgentRole]string) tui.AgentRunner {
		return buildPipelineController(cfg, state, reg, pol, resolver, database, projectID, skillIndex, commandRunner, planPath, limitsTable, overrides, commandService)
	}
	planAuthorFactory := buildPlanAuthorFactory(cfg, state, reg, pol, resolver, database, projectID, skillIndex, commandRunner, commandService)

	// The generation is committed last, and only on the success path: every
	// candidate above has been built and validated, so this is the point at
	// which the new defaults become the service's — atomically, for future
	// launches only. A failure returned earlier leaves the service, its
	// callbacks and the previous generation exactly as they were.
	commandService.commitLaunchPolicy(commandLaunchPolicy(cfg, state, commandService.nextGeneration(), commandRunner, native.ResourcesOf(commandRunner)), lspSync)
	return runner, reg, swarmRunner, mcpMgr, snapSvc, commandService, watchManager, desktopCloser, subagentFactory, pipelineFactory, planAuthorFactory, swarmOverrideFactory, nil
}

// beginSessionStop registers the conversation-level stop group for one parent
// operation — a TUI turn, a /review dispatch, an SDD run, a pipeline dispatch,
// an ACP prompt — BEFORE that operation is dispatched, and returns the teardown
// that clears only that registration (never a newer operation's).
//
// Registering first is the point: a stop that lands while the operation is still
// starting must have a target, and the group identity is what every command the
// operation launches records as its turn.
func beginSessionStop(state *session.State, actor string) (stop func()) {
	if state == nil {
		return func() {}
	}
	token, _ := state.BeginTurnStop(actor)
	return func() { state.EndTurnStop(token) }
}

// mcpFailureMessage renders per-server MCP start failures as one notice
// line. Names and reasons only — never header values.
//
// A server that failed because it needs OAuth gets an actionable sentence
// naming the command that fixes it; every other failure keeps its raw
// reason and the generic "unavailable" wording.
func mcpFailureMessage(failures []mcp.ServerFailure) string {
	parts := make([]string, 0, len(failures))
	allAuth := len(failures) > 0
	for _, f := range failures {
		if errors.Is(f.Err, oauth.ErrAuthSentinel) {
			parts = append(parts, fmt.Sprintf("MCP server '%s' needs OAuth — run /mcp auth %s", f.Name, f.Name))
			continue
		}
		allAuth = false
		parts = append(parts, f.Error())
	}
	// When every failure is an authentication gap, the "unavailable"
	// framing is misleading: the server is configured correctly and only
	// needs the user to authorize it.
	if allAuth {
		return strings.Join(parts, "; ")
	}
	return fmt.Sprintf("%d MCP server(s) unavailable: %s", len(failures), strings.Join(parts, "; "))
}

// loadLimitsTable loads the merged model-limits table so presets without an
// explicit context_window still resolve a real window instead of falling
// straight through to the local catalog. Cache-only unless remote limit
// discovery is enabled; nil when dataDir is empty or no table is available —
// resolution degrades gracefully (agent.ResolveModelLimits falls back to the
// catalog).
func loadLimitsTable(ctx context.Context, cfg config.Config, dataDir string) *limits.Table {
	if dataDir == "" {
		return nil
	}
	if cfg.Privacy.RemoteLimitDiscovery {
		if t, err := limits.LoadTable(ctx, dataDir, limits.DefaultTTL); err == nil {
			return &t
		}
		return nil
	}
	if c, err := limits.Load(dataDir); err == nil && len(c.Table) > 0 {
		t := limits.NewTable(c.Table)
		return &t
	}
	return nil
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
	limitsTable *limits.Table
	database    *db.DB // nil-safe: only used for pipeline child rollover (Task 7)

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
	// Unattended pipeline children evaluate under an auto-approving policy
	// clone, so a network tool's Confirm would be silently allowed with no
	// user watching. The pipeline registry factory strips them too and
	// replaces this registry on the production path; this covers the legacy
	// dispatch that has no RegistryFactory wired.
	if s.childSession {
		toolReg = registry.WithoutNetwork(toolReg)
	}
	// Pipeline role runners stream into a fresh child session so their turn
	// and tool noise stays out of the orchestrator transcript; the parent
	// shows a drillable summary card instead. Depth is parent+1 so any
	// nested subagent attempt is rejected by the child's own depth guard.
	runnerState := s.state
	pol := s.pol
	if s.childSession {
		runnerState = session.New(s.state.Config, s.state.WorkingDir, time.Now(), session.Persistence{}, session.WithDepth(s.state.SubagentDepth()+1), session.WithSubagentMaxConcurrency(s.state.Config.Agent.MaxConcurrentSubagents))
		// No UI watches a child session's pending approvals, so an
		// unattended runner's approval could sit unanswered indefinitely.
		// Unattended runners therefore evaluate under an auto-approving
		// clone of the shared engine (guardrails and the git-push floor
		// still apply), leaving the parent mode intact.
		pol = s.pol.Clone()
		pol.SetApprovalMode(policy.ModeAuto)
	}
	r := agent.NewRunner(p, toolReg, pol, runnerState, route.Preset.Model)
	r.Role = role
	// Synchronous role workers (pipeline implementer/reviewer, swarm roles)
	// inherit the parent operation's stop group: a user stopping the running
	// turn must reach the commands this worker launched, and those commands
	// belong to the same conversation operation. NewSyntheticExecutionControl
	// falls back to a self-allocated group when no parent registration exists.
	r.ExecutionControl = agent.NewSyntheticExecutionControl(s.state)
	// AI-03: without a RouteResolver, resolveRoute returns a zero route and
	// role runners compact against the 60000-token fallback regardless of
	// their model's real window, ignore per-role context budgets, and get no
	// semantic retrieval. The static resolver replays the already-resolved
	// role route — the same pattern subagent children use (app.go:1287).
	r.RouteResolver = &staticRouteResolver{route: route, provider: p}
	r.LimitsTable = s.limitsTable
	r.WriteGate = s.writeGate
	r.SkillIndex = s.skillIndex
	r.MemoryProvider = s.memory
	r.ProjectID = s.projectID
	r.MetricsObserver = s.metricsObserver
	if s.cfg.Agent.ReconnectMaxWaitSeconds > 0 {
		r.ReconnectMaxWait = time.Duration(s.cfg.Agent.ReconnectMaxWaitSeconds) * time.Second
	}
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
	// Global verification-gate fallback: pipeline/swarm role runners get the
	// same [agent] verification_gate default as the parent runner; a preset
	// override still wins per-route (Runner.verificationGateOn).
	r.VerificationGate = s.cfg.Agent.VerificationGateEnabled()
	r.NarrationPrompt = s.cfg.Agent.NarrationPrompt
	r.IntentNudge = s.cfg.Agent.IntentNudge
	r.Pricing = pricing.Lookup(route.Preset, s.state.Logger()) // closes role-runner pricing gap
	// AI-03: per-runner rollover for pipeline (child-session) runners only.
	// Each child gets a minted session ID with its own agent_sessions row
	// (session_generations has a real FK — migrations.go:179-188), so the
	// controller is the sole writer for its ID. Swarm runners share the
	// parent session state and run concurrently; per-runner controllers
	// there would interleave generation rows and race archive cursors, so
	// they keep the summarize-and-continue fallback (now correctly sized
	// via the RouteResolver above). Digests use minimalDigestProvider:
	// ephemeral subagent generations are not worth an LLM call.
	if s.childSession && s.database != nil {
		window, _ := agent.ResolveModelLimits(route.Preset, s.limitsTable, s.state.Logger())
		if s.cfg.Session.Rollover.EffectiveEnabled(route.Preset.LocalOnly, window) {
			childSessionID := "sess_sub_" + uuid.New().String()
			if err := s.database.CreateSession(childSessionID, s.projectID, "sdd:"+string(role), time.Now()); err != nil {
				if l := s.state.Logger(); l != nil {
					l.Warn("pipeline child session insert failed; rollover disabled", "role", role, "error", err)
				}
			} else {
				ctrl, cerr := NewRolloverController(childSessionID, s.cfg.Session.Rollover, s.database, route.Preset.LocalOnly, window, minimalDigestProvider{}, nil)
				switch {
				case cerr != nil:
					if l := s.state.Logger(); l != nil {
						l.Warn("pipeline rollover controller failed; continuing without rollover", "role", role, "error", cerr)
					}
				case ctrl == nil:
					// Rollover disabled between the check above and construction.
				default:
					if serr := ctrl.Start(context.Background()); serr != nil {
						if l := s.state.Logger(); l != nil {
							l.Warn("pipeline rollover start failed; continuing without rollover", "role", role, "error", serr)
						}
					} else {
						r.Rollover = &agent.Rollover{Controller: ctrl, State: runnerState}
						r.CloseRolloverOnDone = true
					}
				}
			}
		}
	}
	repoInstructions, _ := loadRepoInstructions(s.state.WorkingDir)
	var customAddendum string
	if route.CustomAgent != nil {
		ca := route.CustomAgent
		r.ActorLabel = ca.Name
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
func buildSwarmRunner(cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index, lt *limits.Table) *swarm.Orchestrator {
	spec := roleRunnerSpec{
		cfg:         cfg,
		state:       state,
		pol:         pol,
		resolver:    resolver,
		reg:         reg,
		readOnlyReg: registry.ReadOnlyView(reg),
		testerReg:   registry.TesterView(reg),
		skillIndex:  skillIndex,
		memory:      newDBMemoryProvider(database),
		projectID:   projectID,
		limitsTable: lt,
		database:    database,

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
func buildPipelineController(cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index, commandRunner native.CommandRunner, planPath string, lt *limits.Table, overrides map[routing.AgentRole]string, commandService *conversationCommandService) *pipeline.ControllerAdapter {
	// Apply per-run role overrides to a copy of the routing config. The
	// provider cache is shared with the startup resolver so providers are
	// not rebuilt per run. Overrides are never written to disk.
	perRunResolver := resolver.withRoleOverrides(overrides)
	spec := roleRunnerSpec{
		cfg:         cfg,
		state:       state,
		pol:         pol,
		resolver:    perRunResolver,
		reg:         reg,
		readOnlyReg: registry.ReadOnlyView(reg),
		skillIndex:  skillIndex,
		memory:      newDBMemoryProvider(database),
		projectID:   projectID,
		limitsTable: lt,
		database:    database,

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
				// UsageTokens is owned and incremented by the controller's
				// chained OnTokens closure; read the updated value here.
				state.UpdateSDDTokens(ctl.UsageTokens, cfg.SDD.MaxTotalTokens)
			},
			RegistryFactory: makePipelineRegistryFactory(cfg, state, commandRunner, resolver, database, projectID, skillIndex, reg, commandService),
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
		MaxTokensCfg:       cfg.SDD.MaxTotalTokens,
		Setup:              cfg.Worktree,
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

// resolveAuthorPlansDir joins the working directory with the plans
// directory relative path and canonicalizes the result via repo.Canonical.
// If the plans directory does not yet exist, repo.Canonical walks up to
// the nearest existing ancestor, so non-existent plans directories
// canonicalize into the same namespace as the repository root.
//
// Security note: a symlinked plans_dir that points outside the repository
// resolves into a different directory and is rejected by sddplans.DraftPath
// at authoring time.
func resolveAuthorPlansDir(workingDir, plansDirRel string) string {
	return repo.Canonical(filepath.Join(workingDir, plansDirRel))
}

// buildPlanAuthorFactory returns a factory that builds a scoped SDD
// plan-authoring runner for one request. The child may inspect the
// repository and write exactly one plan artifact; it cannot modify source
// files, run commands, spawn agents, or ask the user.
func buildPlanAuthorFactory(cfg config.Config, state *session.State, reg *registry.Registry, pol *policy.PolicyEngine, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index, commandRunner native.CommandRunner, commandService *conversationCommandService) sddauthor.Factory {
	repoInstructionsForPlanAuthor, _ := loadRepoInstructions(state.WorkingDir)
	return func(req sddauthor.Request) (*sddauthor.Runner, error) {
		route, p, err := resolver.ResolveRole(routing.RoleSDDPlanAuthor)
		if err != nil {
			return nil, err
		}
		// A fresh child session keeps the authoring turn's tool noise out of
		// the parent transcript. Depth is parent+1 so any nested subagent
		// attempt is rejected by the child's own depth guard.
		childState := session.New(cfg, state.WorkingDir, time.Now(), session.Persistence{}, session.WithDepth(state.SubagentDepth()+1), session.WithSubagentMaxConcurrency(cfg.Agent.MaxConcurrentSubagents))
		childPol := pol.Clone()
		childPol.SetApprovalMode(policy.ModeAuto)

		// A fresh native registry rooted at the working directory, with @plan
		// aliasing the resolved plans directory so the child can write the
		// plan artifact without exposing absolute paths and so a symlinked
		// plans_dir cannot trick the child into writing outside the repo.
		childReg := registry.New()
		plansDir := resolveAuthorPlansDir(state.WorkingDir, cfg.SDD.PlansDir)
		// No-command path: the authoring child never runs an ordinary command
		// — PlanWriterView strips the command tools below — so it is given no
		// command service at all. A dispatch that somehow reached shell.run
		// therefore fails with a clear owner error instead of supervising a
		// command into a registry the conversation does not own.
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
		childRunner.ActorLabel = "plan author"
		// The plan author runs synchronously inside the parent's SDD operation,
		// so it inherits that operation's stop group: a user stopping the turn
		// reaches the author's commands too.
		childRunner.ExecutionControl = agent.NewSyntheticExecutionControl(state)
		childRunner.NarrationPrompt = cfg.Agent.NarrationPrompt
		childRunner.IntentNudge = cfg.Agent.IntentNudge
		childRunner.SkillIndex = skillIndex
		childRunner.MemoryProvider = newDBMemoryProvider(database)
		childRunner.ProjectID = projectID
		if cfg.Agent.ReconnectMaxWaitSeconds > 0 {
			childRunner.ReconnectMaxWait = time.Duration(cfg.Agent.ReconnectMaxWaitSeconds) * time.Second
		}
		childRunner.VerificationGate = cfg.Agent.VerificationGateEnabled()
		childRunner.SetForceClass("question")
		decoding := resolveActionDecoding(route.Preset.ToolCalling, p.Capabilities(context.Background()))
		childRunner.NativeTools = decoding.Native
		childRunner.ResponseFormat = decoding.ResponseFormat
		if cap := roleToolIterations(cfg, agent.RoleSDDPlanAuthor); cap > 0 {
			childRunner.MaxToolIterations = cap
		}
		childRunner.Pricing = pricing.Lookup(route.Preset, state.Logger())
		childRunner.SystemPromptAddendum = repoInstructionsForPlanAuthor
		return sddauthor.NewRunner(childRunner), nil
	}
}

// makePipelineRegistryFactory returns a RegistryFactory that builds a fresh
// tool registry bound to a per-dispatch execution context. The dispatcher
// passes the runner's own child session so stateful tools (todo.write)
// land where the drill-down card renders; the native toolset still closes
// over the per-dispatch worktree root and artifact aliases.
// The returned registry is then filtered by scope.
//
// ScopeFallback narrows file.write_patch to the controller-supplied
// allowlist (set on Dispatcher.FallbackAllowedFiles immediately before
// each fallback dispatch) so the marshal.agent fallback cannot modify
// parts of the worktree outside its declared scope.
//
// commandService is the conversation's command service, borrowed — never
// created here. A pipeline worker runs inside the conversation that dispatched
// it, so its commands belong to the same supervisor: one capacity, one count
// callback, one exit sink and one stop target. Its output stays child-local
// because SessionState is the runner's own child state.
func makePipelineRegistryFactory(cfg config.Config, state *session.State, commandRunner native.CommandRunner, resolver *routedProviderResolver, database *db.DB, projectID int64, skillIndex *skills.Index, parentReg *registry.Registry, commandService *conversationCommandService) pipeline.RegistryFactory {
	return func(ctx pipeline.ExecutionContext, scope pipeline.RegistryScope, childState *session.State) (*registry.Registry, error) {
		childReg := registry.New()
		var jobManager *native.JobManager
		if commandService != nil {
			jobManager = commandService.manager
		}
		nativeOpts := native.Options{
			WorkspaceRoot:  ctx.WorkspaceRoot,
			CommandRunner:  commandRunner,
			MaxOutputBytes: cfg.Tools.Shell.MaxOutputBytes,
			SessionState:   childState,
			Config:         cfg,
			JobManager:     jobManager,
			// A dispatch's commands belong to THIS dispatch's worktree, not to
			// whatever the session's active root happens to be later. Capturing
			// it here — per registry, read at admission — is what stops a
			// sibling dispatch or a workspace change from redirecting a command
			// that already started.
			CommandDir: ctx.WorkspaceRoot,
			NamedRoots: ctx.NamedRoots(),
		}
		// When the pipeline runs in a worktree (WorkspaceRoot != RepoRoot),
		// expose the project root as an additional root so workers can read
		// files outside the worktree (e.g. ../docs/architecture.md) without
		// the path escaping every allowed root.
		if ctx.WorkspaceRoot != ctx.RepoRoot {
			nativeOpts.AdditionalRoots = []string{ctx.RepoRoot}
		}
		if err := native.RegisterAll(childReg, nativeOpts); err != nil {
			return nil, fmt.Errorf("pipeline registry factory: register: %w", err)
		}
		skills.RegisterTool(childReg, skillIndex, childState, skills.SkillsToolOptions{
			HomeDir: state.HomeDir(),
			// Project scope must resolve to the real repository, not the
			// per-dispatch worktree: the worktree is gitignored and removed
			// by workspace.finish, so a skill installed there would vanish.
			WorkingDir: ctx.RepoRoot,
		})
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
			// ScopeFull is the implementer/fixer: the only pipeline scope
			// that keeps write and shell tools. It must still lose network
			// tools — pipeline role runners are built with childSession:
			// true, so their policy clone auto-approves a web Confirm with
			// no user watching, and web.fetch bypasses the shell sandbox's
			// network isolation.
			return registry.WithoutNetwork(childReg), nil
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

// accessTUIOptions wires the TUI's access seam to this runtime. The bound
// methods read the runtime's current State and Runner under the runtime gate,
// so a successful NewSession does not leave the TUI holding a closure over the
// session it replaced: the same options keep working against the new one.
func accessTUIOptions(rt *Runtime) []tui.Option {
	return []tui.Option{
		tui.WithAccessRequests(rt.AccessRequest, rt.ResolveAccess),
		tui.WithSessionModeSetter(rt.SetSessionMode),
		tui.WithSessionEffortSetter(rt.SetSessionEffort),
	}
}

// buildSubagentFactory is the test-facing form: children serialize on a
// private lock. Production uses buildSubagentFactoryWithLock so the parent
// runner and every agent.run child share one gate. (Keeping this wrapper
// avoids churning the ~25 test call sites.)
//
// It passes no command service: the wrapper's empty base options already mean
// children fall back to a filtered view of the parent registry (no native
// command tools of their own), and those tests never execute a tool.
func buildSubagentFactory(cfg config.Config, parentState *session.State, parentProvider provider.Provider, parentReg *registry.Registry, pol *policy.PolicyEngine, defaultModel string, router *routing.StaticRouter, resolver *routedProviderResolver, database *db.DB, projectID int64, parentPricing pricing.ModelPricing) (agent.SubagentRunnerFactory, agent.SubagentModelResolver) {
	return buildSubagentFactoryWithLock(cfg, parentState, parentProvider, parentReg, pol, defaultModel, router, resolver, database, projectID, parentPricing, &swarm.WriteLock{}, native.Options{}, nil, nil)
}

// buildSubagentFactoryWithLock returns a closure that constructs a fresh
// child Runner for an agent.run invocation. The closure captures the
// parent's provider, policy engine, and base registry; per call it spins up
// a new session.State (so the child's transcript does not pollute the
// parent's message log), a filtered registry view (read-only + network, no
// nested agent.run), and binds RoleSubtask so the system prompt enforces
// the appropriate scope. The child session's depth is parent+1 so its own
// depth guard rejects any attempt to spawn nested subagents.
//
// When the request names a custom agent, the factory resolves it via the
// router and applies its overrides (system prompt, tool denylist, max
// iterations). An explicit model in the request replaces only the resolved
// preset's provider/model; all other overrides are retained. A role in the
// request resolves through the active profile's explicit binding for that
// role, falling back to the default model when the role is unbound. Both
// named-agent and ad-hoc paths wire Pricing, UsageObserver, and MetricsObserver
// so subagent token usage and cost are visible to the parent session.
//
// writeLock is the shared swarm.WriteLock every child runner serializes its
// writes on. Production passes the parent runner's own gate so parent and
// background children (which outlive the spawning turn) serialize together.
//
// commandService is the conversation's command service, borrowed — never
// created here. A child's shell commands belong to the conversation that
// spawned it, so they are counted, settled and stopped through the same
// supervisor as the parent's; its output stays child-local because the child
// registry is bound to the child's own SessionState. A nil service (the test
// wrapper, or a scope with no command tools) simply leaves the child with no
// command owner, and a dispatch through one fails clearly instead of
// supervising commands into a registry nothing owns.
func buildSubagentFactoryWithLock(cfg config.Config, parentState *session.State, parentProvider provider.Provider, parentReg *registry.Registry, pol *policy.PolicyEngine, defaultModel string, router *routing.StaticRouter, resolver *routedProviderResolver, database *db.DB, projectID int64, parentPricing pricing.ModelPricing, writeLock *swarm.WriteLock, baseNativeOpts native.Options, watchTransferrer agent.WatchTransferrer, commandService *conversationCommandService) (agent.SubagentRunnerFactory, agent.SubagentModelResolver) {
	// A child tool ceiling is opt-in. An unset value, like an explicit zero,
	// leaves the child unlimited. Negative values are treated as unset.
	subtaskIters := cfg.Agent.SubtaskIterations
	if !parentState.Layers().SubtaskIterationsSet || subtaskIters < 0 {
		subtaskIters = 0
	}
	metricsObserver := metricsRecorder(database, projectID, parentState.SessionID(), parentState.Logger())
	repoInstructionsForSubagent, _ := loadRepoInstructions(parentState.WorkingDir)

	// Model resolver for the consent gate — mirrors the factory's model
	// resolution without building a runner. Used by agent.run's handler to
	// preview the child's model so it can ask the user for consent when the
	// child's model has different cost implications than the parent's.
	modelResolver := agent.SubagentModelResolver(func(req agent.SubagentRequest) (agent.SubagentModelPreview, error) {
		model := defaultModel
		providerName := ""
		if parentProvider != nil {
			providerName = parentProvider.Name()
		}
		if req.Model != "" {
			if router == nil {
				return agent.SubagentModelPreview{}, fmt.Errorf("explicit model %q requested but no router configured", req.Model)
			}
			eroute, err := router.ResolveExplicitModel(req.Model, agent.RoleSubtask)
			if err != nil {
				return agent.SubagentModelPreview{}, fmt.Errorf("agent.run: %w", err)
			}
			return agent.SubagentModelPreview{
				Model:    eroute.Preset.Model,
				Provider: eroute.Preset.Provider,
				Pricing:  pricing.Lookup(eroute.Preset, parentState.Logger()),
			}, nil
		}
		if req.Role != "" && req.Agent == "" && router != nil {
			if route, ok := router.ResolveRoleIfBound(routing.AgentRole(req.Role)); ok {
				return agent.SubagentModelPreview{
					Model:    route.Preset.Model,
					Provider: route.Preset.Provider,
					Pricing:  pricing.Lookup(route.Preset, parentState.Logger()),
				}, nil
			}
		}
		if req.Agent != "" && router != nil {
			route, err := router.ResolveCustomAgent(req.Agent, agent.RoleSubtask)
			if err != nil {
				return agent.SubagentModelPreview{}, fmt.Errorf("agent.run: %w", err)
			}
			if req.Model == "" {
				return agent.SubagentModelPreview{
					Model:    route.Preset.Model,
					Provider: route.Preset.Provider,
					Pricing:  pricing.Lookup(route.Preset, parentState.Logger()),
				}, nil
			}
		}
		return agent.SubagentModelPreview{Model: model, Provider: providerName, Pricing: parentPricing}, nil
	})

	return func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		childState := session.New(parentState.Config, parentState.WorkingDir, time.Now(), session.Persistence{}, session.WithDepth(parentState.SubagentDepth()+1), session.WithSubagentMaxConcurrency(parentState.Config.Agent.MaxConcurrentSubagents))
		// Seed the child with the parent's session context. Layer-aware
		// project saves (config.SaveProjectConfig) compare against
		// state.Layers(): a child without the snapshot silently regresses to
		// write-everything, and config writes from the TUI reach children
		// through the shared ConfigReloader — so a subagent could bake
		// user-global values into the committable .marshal/config.toml.
		childState.SetLayers(parentState.Layers())
		childState.SetHomeDir(parentState.HomeDir())
		childState.SetTrusted(parentState.Trusted())
		// System access is inherited only when the parent has it AND the
		// request explicitly asked for it. The AND lives here, at
		// construction, so a child can never widen its own scope even if a
		// future caller bypasses the handler's guard. Set outside the
		// native-toolset branch below so the flag holds on every factory
		// path (including the test wrapper with no WorkspaceRoot).
		childState.SetSystemAccess(req.System && parentState.SystemAccess())
		// The child needs its own native toolset, not a filtered view of the
		// parent's. SubtaskScopeView re-registers the parent's existing Tool
		// values, whose handlers close over the parent's toolSet — so a
		// filtered view still writes into the parent's session state. That is
		// how a child's shell output ended up streaming into the parent's
		// in-flight agent.await row.
		//
		// The test wrapper passes an empty baseNativeOpts (no WorkspaceRoot),
		// so we fall back to the old SubtaskScopeView view there — those tests
		// only inspect model/pricing/iterations and never execute a tool.
		var roReg *registry.Registry
		if baseNativeOpts.WorkspaceRoot != "" {
			childOpts := baseNativeOpts
			childOpts.SessionState = childState    // per-agent: active call, output, approvals
			childOpts.WorkspaceState = parentState // per-workspace: backups, active root
			// A child's commands belong to the conversation that spawned it,
			// so the child borrows the conversation's command service rather
			// than owning one. Its output stays child-local (SessionState is
			// the child's), while the running count and the durable exit sink
			// stay parent-owned (both were installed at owner construction).
			// A nil service — the test wrapper, a fixture without an owner —
			// leaves the child with no command owner, and a shell dispatch
			// through it fails clearly instead of supervising commands into a
			// registry nothing owns.
			childOpts.JobManager = commandServiceManager(commandService)
			// A subagent must not be able to run privileged commands: its
			// prompt would have no transport to render it, so the call would
			// hang until turn cancellation. Today that is enforced only by
			// the child's SecretPromptCapable defaulting to false — a fragile
			// gate, since any future change that propagates the flag to child
			// states would silently hand children a working shell.sudo. Clear
			// the policy seam here so the documented gate is the real one.
			childOpts.SudoPolicy = nil
			childReg := registry.New()
			if err := native.RegisterAll(childReg, childOpts); err != nil {
				return nil, nil, fmt.Errorf("agent.run: child registry: %w", err)
			}
			// Copy non-native tools (MCP, skills, desktop) from the parent's
			// scoped registry so the child still sees them. The authoritative
			// exclusion list lives on SubtaskScopeView's doc comment
			// (internal/agent/subagent.go) — don't duplicate it here.
			for _, tool := range agent.SubtaskScopeView(parentReg).List() {
				if _, exists := childReg.Lookup(tool.Name); !exists {
					_ = childReg.Register(tool)
				}
			}
			roReg = childReg
		} else {
			roReg = agent.SubtaskScopeView(parentReg)
		}
		role := agent.RoleSubtask
		model := defaultModel
		childProvider := parentProvider
		var addendum string
		var pricingRates pricing.ModelPricing
		iters := subtaskIters
		// Ad-hoc sampling overrides from the agent.run call win over the
		// named agent's own config (explicit override > preset > role default).
		var tempOverride *float64
		var thinkOverride string
		if req.Temperature != nil {
			tempOverride = req.Temperature
		}
		if req.Thinking != "" {
			thinkOverride = req.Thinking
		}
		// An explicit provider/model pair resolves first so invalid pairs
		// fail clearly before any execution, and so a named agent can replace
		// only its preset provider/model while keeping its other overrides.
		var targetRoute routing.Route
		if req.Model != "" {
			if router == nil {
				return nil, nil, fmt.Errorf("agent.run: explicit model %q requested but no router configured", req.Model)
			}
			eroute, err := router.ResolveExplicitModel(req.Model, agent.RoleSubtask)
			if err != nil {
				return nil, nil, fmt.Errorf("agent.run: %w", err)
			}
			model = eroute.Preset.Model
			pricingRates = pricing.Lookup(eroute.Preset, parentState.Logger())
			targetRoute = eroute
		}
		// Role pinning: applies only when no explicit model/agent was requested,
		// and only when the role is explicitly bound in the profile. Unbound or
		// inherited bindings keep the default model (today's behavior).
		if req.Role != "" && req.Model == "" && req.Agent == "" && router != nil {
			if route, ok := router.ResolveRoleIfBound(routing.AgentRole(req.Role)); ok {
				model = route.Preset.Model
				pricingRates = pricing.Lookup(route.Preset, parentState.Logger())
				targetRoute = route
			}
		}
		if req.Agent != "" && router != nil {
			route, err := router.ResolveCustomAgent(req.Agent, agent.RoleSubtask)
			if err != nil {
				return nil, nil, fmt.Errorf("agent.run: %w", err)
			}
			if req.Model == "" {
				model = route.Preset.Model
				pricingRates = pricing.Lookup(route.Preset, parentState.Logger())
				targetRoute = route
			}
			if route.CustomAgent != nil {
				ca := route.CustomAgent
				addendum = ca.SystemPrompt
				if len(ca.ToolDenylist) > 0 {
					roReg = agent.DenylistView(roReg, ca.ToolDenylist)
				}
				if ca.MaxIterationsUnlimited {
					iters = 0 // unlimited, matching DefaultMaxToolIterations semantics
				} else if ca.MaxIterations > 0 {
					iters = ca.MaxIterations
				}
				if tempOverride == nil {
					tempOverride = ca.Temperature
				}
				if thinkOverride == "" {
					thinkOverride = ca.Thinking
				}
			}
		}
		if targetRoute.Preset.Provider != "" && (parentProvider == nil || targetRoute.Preset.Provider != parentProvider.Name()) {
			// In tests resolver may be nil; keep the parent provider so
			// unit tests that only inspect model/pricing still work.
			if resolver != nil {
				p, err := resolver.providerFor(targetRoute)
				if err != nil {
					return nil, nil, fmt.Errorf("agent.run: model %q: %w", model, err)
				}
				childProvider = p
			}
		}
		// Resolve action decoding from the child's route preset and the
		// child provider's capabilities — matching roleRunnerSpec.newRunner.
		// Hardcoding NativeTools=true sent tool definitions to providers
		// that ignored them, leaving the model unable to call tools.
		toolCalling := "native"
		if targetRoute.Preset.ToolCalling != "" {
			toolCalling = targetRoute.Preset.ToolCalling
		}
		// A nil provider (unit tests that only inspect model/pricing) has no
		// capabilities to probe; default to native tool-calling, the old
		// behavior, so those tests keep working.
		var caps schema.ProviderCapabilities
		if childProvider != nil {
			caps = childProvider.Capabilities(context.Background())
		}
		decoding := resolveActionDecoding(toolCalling, caps)
		// Rebind todo.write to the child's own session: a subagent's task
		// list must not overwrite the parent's visible list.
		if err := roReg.Replace(native.TodoWriteTool(childState)); err != nil {
			parentState.Logger().Warn("subagent: todo rebind failed; child shares parent todos", "error", err)
		}
		child := agent.NewRunner(childProvider, roReg, pol, childState, model)
		child.Role = role
		child.NarrationPrompt = cfg.Agent.NarrationPrompt
		child.IntentNudge = cfg.Agent.IntentNudge
		if req.Agent != "" {
			child.ActorLabel = req.Agent
		}
		child.MaxToolIterations = iters
		child.TemperatureOverride = tempOverride
		child.ThinkingOverride = thinkOverride
		// Global verification-gate fallback, matching the parent runner's
		// wiring; a preset override still wins per-route via the child's
		// RouteResolver (Runner.verificationGateOn).
		child.VerificationGate = cfg.Agent.VerificationGateEnabled()
		child.NativeTools = decoding.Native
		child.ResponseFormat = decoding.ResponseFormat
		// Set a route resolver so the child's RunTask can derive the
		// correct context window and max output from the resolved route.
		// Without this, resolveRoute returns an empty Route with
		// Window=0, causing premature context truncation.
		if targetRoute.Preset.Name != "" {
			// Explicit model or named agent: use a static resolver that
			// always returns the already-resolved target route.
			child.RouteResolver = &staticRouteResolver{route: targetRoute, provider: childProvider}
		} else if resolver != nil {
			// Default model: reuse the parent's resolver so the child
			// gets the same context window as the parent.
			child.RouteResolver = resolver
		}
		child.SystemPromptAddendum = composeAddendum(repoInstructionsForSubagent, addendum)
		child.Pricing = pricingRates
		child.MetricsObserver = metricsObserver
		child.WriteGate = writeLock
		// Wire the watch manager onto the child runner so its completion
		// goroutine can re-parent the child's once watches and stop its
		// repeat watches when the subagent ends.
		child.WatchTransferrer = watchTransferrer
		// A synthetic child (agent.run) owns its own stop group, but it shares
		// the parent's session and command service, so its commands still
		// arbitrate at one boundary. newSubagentFactory's caller replaces this
		// with the child's dedicated group once the view id exists.
		child.ExecutionControl = agent.NewSyntheticExecutionControl(parentState)
		// C-1: wire the snapshot service onto the child runner so its
		// pre-write snapshots are recorded against the child's own session
		// ID. Without this, a background child can clobber files with no
		// rollback coverage — the parent's dispatch-time snapshot records
		// changedFilesForTool("agent.run", ...) which is always empty.
		if snap := parentState.Snapshotter(); snap != nil {
			child.Snapshotter = snap
			child.SnapshotRecorder = database
		}
		// A background child's writes must drop the parent's cached reads;
		// the child's own ClearToolCache only covers its own session.
		child.CacheInvalidator = parentState.ClearToolCache
		// Record child usage on the child's own session state (visible in
		// the drilled-in view). It must NOT fold into the parent's
		// turn-usage counter: the status bar shows that counter as live
		// parent-turn context use, and folding made it inflate past the
		// window and snap back. Session-level rollups come from
		// turn_metrics, which MetricsObserver already feeds.
		child.UsageObserver = func(usage schema.TokenUsage) {
			childState.SetTurnUsage(usage.PromptTokens + usage.CompletionTokens)
		}
		return child, childState, nil
	}, modelResolver
}

// staticRouteResolver implements agent.RouteResolver, always returning the
// same pre-resolved route. Used by subagent children that were dispatched
// with an explicit model or named agent so their RunTask derives the
// correct context window without re-resolving through the profile.
type staticRouteResolver struct {
	route    routing.Route
	provider provider.Provider
}

func (s *staticRouteResolver) Resolve(string) (routing.Route, provider.Provider, error) {
	return s.route, s.provider, nil
}

// tuiEndController is the runtime adapter behind tui.EndController. It converts
// the runtime's own shapes into the TUI's neutral mirrors and delegates every
// decision back to the runtime, so the model never sees a session, a policy
// engine or a process.
type tuiEndController struct {
	rt *Runtime
}

// ActiveWork reports the runtime's current conversation work.
func (c *tuiEndController) ActiveWork() tui.EndWorkSnapshot {
	return tuiEndWorkSnapshot(c.rt.ActiveWork())
}

// AcceptEnd submits one end acceptance and reports it in the TUI's shape.
func (c *tuiEndController) AcceptEnd(req tui.EndAcceptRequest) (tui.EndDecision, error) {
	decision, err := c.rt.AcceptEnd(EndAcceptRequest{
		Intent:           EndIntent(req.Intent),
		DisplayedIDs:     req.DisplayedIDs,
		RequireDisplayed: req.RequireDisplayed,
	})
	if err != nil {
		return tui.EndDecision{}, err
	}
	return tui.EndDecision{
		Accepted: decision.Accepted,
		Expanded: decision.Expanded,
		Work:     tuiEndWorkSnapshot(decision.Work),
	}, nil
}

// EndAccepted reports whether an end has been accepted.
func (c *tuiEndController) EndAccepted() bool { return c.rt.EndAccepted() }

// WaitForEnd stops and joins the accepted end's work. It accepts first and is
// safe to retry: a failed join reports the outstanding work and never latches
// success.
func (c *tuiEndController) WaitForEnd(ctx context.Context) error { return c.rt.WaitForEnd(ctx) }

// RunExitAnalysis runs the scoped exit-analysis pass during the postmortem
// phase. The runtime owns the pass's elevation and joins its epoch before
// returning.
func (c *tuiEndController) RunExitAnalysis(ctx context.Context, goal string) error {
	return c.rt.RunExitAnalysis(ctx, goal)
}

// tuiEndWorkSnapshot converts a runtime work snapshot to the TUI's neutral
// mirror.
func tuiEndWorkSnapshot(work ActiveWork) tui.EndWorkSnapshot {
	out := tui.EndWorkSnapshot{Phase: work.Phase, Targets: work.Targets}
	for _, item := range work.Items {
		out.Items = append(out.Items, tui.EndWorkItem{
			ID:         item.ID,
			Kind:       item.Kind,
			Label:      item.Label,
			Group:      item.Group,
			Parent:     item.Parent,
			SubagentID: item.SubagentID,
		})
	}
	return out
}

// runWithExitAnalysisElevation runs fn with the temporary elevation the exit
// postmortem's agent pass needs, and restores the previous values whatever the
// outcome.
//
// The pass writes the report at an absolute path outside the workspace, so it
// needs system access and an editing approval mode/force class; without them
// plan/default mode would deny the write and bounce a mode.request at someone
// who is trying to exit. The grant is SCOPED to this call: it is undone here
// (including on failure), so a refused or failed pass can never leave an
// ordinary prompt elevated.
func runWithExitAnalysisElevation(rt *Runtime, fn func() error) error {
	if rt == nil {
		return fmt.Errorf("exit analysis: no runtime")
	}
	priorSystem := false
	if rt.State != nil {
		priorSystem = rt.State.SystemAccess()
		rt.State.SetSystemAccess(true)
	}
	// Save and restore the runner's own posture. The approval mode lives on the
	// policy engine; the TUI mirrors it into state.Config.Agent.ApprovalMode, so
	// the prior mode is read from the engine when one exists and from the config
	// otherwise. The force class has no accessor (the field is mutex-guarded for
	// writers only), so it is restored from the same source the TUI derives it
	// from — the configured mode.
	priorMode := policy.ParseApprovalMode(rt.Config.Agent.ApprovalMode)
	runner := rt.Runner
	if runner != nil {
		if runner.Policy != nil {
			priorMode = runner.Policy.ApprovalMode()
		}
		runner.SetApprovalMode(policy.ModeEdit)
		runner.SetForceClass(string(agent.ClassEdit))
	}
	defer func() {
		if runner != nil {
			runner.SetApprovalMode(priorMode)
			runner.SetForceClass(classForApprovalMode(priorMode))
		}
		if rt.State != nil {
			rt.State.SetSystemAccess(priorSystem)
		}
	}()
	return fn()
}

// classForApprovalMode maps an approval mode to the task class the TUI forces
// for it: plan and default ask before acting, every other mode edits.
func classForApprovalMode(mode policy.ApprovalMode) string {
	if mode == policy.ModePlan || mode == policy.ModeDefault {
		return string(agent.ClassQuestion)
	}
	return string(agent.ClassEdit)
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

// buildIndexWorkers assembles the indexing background workers: the
// fsnotify watcher (only when config.WatchEnabled allows) and the one-shot
// startup scan (AI-02 — always; hashes+symbols even without embeddings so
// repo.map/symbols work on a fresh project). Every successful pass
// publishes indexEventCompleted on indexBroker, which triggers the
// context-pack re-seed in startRuntime.
func buildIndexWorkers(cfg config.Config, state *session.State, database *db.DB, projectID int64, workingDir string, lspAdapter index.LSPSymbols, indexBroker *pubsub.Broker[index.Report], logger *slog.Logger) []worker.Worker {
	runPass := func(c context.Context) error {
		cfg := state.Config

		embedder := resolveEmbedderFromConfig(cfg)
		if embedder != nil {
			probeCtx, cancel := context.WithTimeout(c, 30*time.Second)
			defer cancel()
			if _, err := embedding.Probe(probeCtx, embedder); err != nil {
				return fmt.Errorf("embedding probe failed: %w", err)
			}
		}

		passCtx, cancel := context.WithTimeout(c, 10*time.Minute)
		defer cancel()
		rep, err := index.Run(passCtx, index.Deps{
			DB:       database,
			Root:     workingDir,
			Ignore:   cfg.Indexing.Ignore,
			MaxBytes: cfg.Indexing.MaxIndexableFileBytes,
			Embedder: embedder,
			LSP:      lspAdapter,
			OnProgress: func(msg string) {
				state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: msg})
			},
		}, projectID)
		if err == nil {
			indexBroker.Publish(indexEventCompleted, rep)
		}
		return err
	}

	var workers []worker.Worker
	embeddingConfigured := false
	embedRouter := routing.NewStaticRouter(cfg.RoutingConfig())
	if _, err := embedRouter.ResolveEmbedding(); err == nil {
		embeddingConfigured = true
	}
	if config.WatchEnabled(cfg.Indexing.Watch, embeddingConfigured) {
		debounce := time.Duration(cfg.Indexing.WatchDebounceMs) * time.Millisecond
		workers = append(workers, index.NewWatcher(workingDir, debounce, runPass, logger))
	}
	workers = append(workers, namedWorker{name: "startup-index-scan", run: runPass})
	return workers
}

// buildOAuthWorkers returns one token-refresh worker per provider entry that
// authenticates with OAuth. Both the worker and the provider backend get their
// engine from the same auth resolver, so a proactive refresh and an inline
// chat-time refresh serialize on the same mutex rather than racing: the
// resolver's per-name engine cache is what guarantees the two hold the exact
// same *oauth.Engine (see provider.AuthResolver).
//
// A provider whose engine cannot be built (no OS keychain, or a resolver that
// refuses) is skipped with a warning: the provider itself will report the same
// problem when used, and a missing keychain must not prevent the rest of the
// app from starting.
func buildOAuthWorkers(cfg config.Config, logger *slog.Logger, auth ...provider.AuthResolver) []worker.Worker {
	authResolver := effectiveAuthResolver(firstAuthResolver(auth))
	var out []worker.Worker
	for name, pc := range cfg.Providers {
		if pc.Auth != "oauth" || pc.Type != "openai_codex" {
			continue
		}
		engine, err := authResolver.CodexOAuthEngine(name)
		if err != nil {
			logger.Warn("oauth refresh worker not started", "provider", name, "err", err)
			continue
		}
		out = append(out, &oauthworker.Worker{
			Engine:   engine,
			Provider: name,
			Logger:   logger,
		})
	}
	return out
}

func Run(ctx context.Context, stdout io.Writer, opts ...Option) error {
	if ctx.Err() != nil {
		return nil
	}

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
		programRunner: runProgram,
	}
	for _, opt := range opts {
		opt(&runOpts)
	}
	runOpts.deferTrustPrompt = true

	// Resolve the credential/OAuth dependency ONCE, at the outer construction
	// boundary: an explicitly nil injection is rejected here, and an omitted
	// one becomes the production default stored back on the options. Every
	// startRuntime iteration in the resume/restart loop below therefore hands
	// the very same object to provider construction, the refresh workers and
	// the MCP managers.
	authResolver, err := resolveAuthResolver(runOpts)
	if err != nil {
		return err
	}
	runOpts.authResolver = authResolver
	runOpts.authResolverSet = true

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
	homeDir := runOpts.homeDir
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return err
		}
	}

	// First-run detection: when no config exists yet, open the connect
	// panel in the TUI instead of running a separate onboarding wizard.
	firstRun := !config.HasConfig(config.LoadOptions{HomeDir: homeDir, WorkingDir: workingDir})
	trustStoreDir := config.DataDir(homeDir)
	trustDecide := func(d trust.Decision) {
		switch d {
		case trust.DecisionTrustPermanent:
			abs := trust.Canonicalize(workingDir)
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
		abs := trust.Canonicalize(dir)
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
		watchBroker := must[*pubsub.Broker[watch.Event]](rt.WatchBroker)
		steeringBroker := must[*pubsub.Broker[session.SteeringEvent]](rt.SteeringBroker)
		workspaceBroker := must[*pubsub.Broker[session.WorkspaceEvent]](rt.WorkspaceBroker)
		subagentBroker := must[*pubsub.Broker[session.SubagentEvent]](rt.SubagentBroker)
		state := rt.State
		logger := rt.Logger

		cmdReg := rt.CommandRegistry

		var tuiOpts []tui.Option
		tuiOpts = append(tuiOpts, tui.WithMemoryStore(database, projectID))
		tuiOpts = append(tuiOpts, tui.WithCommandRegistry(cmdReg))
		configLayers := &rt.Layers
		tuiOpts = append(tuiOpts, tui.WithConfigLayers(&configLayers))
		tuiOpts = append(tuiOpts, tui.WithLayerReloader(layerReloaderFor(homeDir, workingDir, state.Trusted)))
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
		// The access seam is wired alongside the reloader: both are runtime
		// operations the TUI invokes, and both must keep working after a
		// session swap.
		tuiOpts = append(tuiOpts, accessTUIOptions(rt)...)
		// Send now: the same rule as the access seam. The bound runtime methods
		// read the conversation's current State under the pointer mutex, so a
		// successful NewSession keeps the facade working against the replacement
		// while an explicit SessionID/TurnID on the request stops a stale one
		// from targeting it.
		tuiOpts = append(tuiOpts, tui.WithSendNow(rt.SteeringSnapshot, rt.SubmitSendNow))
		tuiOpts = append(tuiOpts, tui.WithHomeDir(homeDir))
		tuiOpts = append(tuiOpts, tui.WithDataDir(config.DataDir(homeDir)))
		// The TUI's auth-backed surfaces (provider probes, the connect
		// wizard's sign-in engine and token store, the settings browser,
		// /mcp auth) must use the dependency this runtime selected. Without
		// this the TUI would fall back to the production adapter and, under
		// an injected resolver, reach the real OS keychain.
		tuiOpts = append(tuiOpts, tui.WithAuthResolver(rt.AuthResolver))
		tuiOpts = append(tuiOpts, tui.WithWorkingDir(workingDir))
		tuiOpts = append(tuiOpts, tui.WithSkillIndex(rt.SkillIndex))
		// Copy prefers a local clipboard helper and falls back to OSC 52 over
		// SSH or when no helper resolves.
		tuiOpts = append(tuiOpts, tui.WithClipboard(
			&clipboard.LocalWriter{},
			func() bool { return clipboard.Remote(os.Getenv) },
		))
		if rt.LSPManager != nil {
			tuiOpts = append(tuiOpts, tui.WithReferenceFinder(lsp.NewSymbolQuery(rt.LSPManager, nil, 0)))
		}
		tuiOpts = append(tuiOpts, tui.WithRunnerSource(func() (context.Context, tui.AgentRunner) {
			rt.mu.Lock()
			defer rt.mu.Unlock()
			if rt.Runner == nil {
				return nil, nil
			}
			return ctx, rt.Runner
		}))
		// Workspace events must flow even when the initial provider build
		// failed, or the worktree footer/rail never updates until a busy-tick.
		// The broker is created in runtime.go regardless of provider state.
		tuiOpts = append(tuiOpts, tui.WithWorkspaceBroker(ctx, workspaceBroker))
		tuiOpts = append(tuiOpts, tui.WithSubagentBroker(ctx, subagentBroker))
		if n, ok := state.Notice(); !ok || n.Category != session.NoticeProvider {
			tuiOpts = append(tuiOpts, tui.WithRunner(ctx, runner))
			tuiOpts = append(tuiOpts, tui.WithSwarmRunner(ctx, swarmRunner))
			tuiOpts = append(tuiOpts, tui.WithPipelineFactory(ctx, rt.PipelineFactory))
			if rt.SwarmOverrideFactory != nil {
				tuiOpts = append(tuiOpts, tui.WithSwarmOverrideFactory(rt.SwarmOverrideFactory))
			}
			tuiOpts = append(tuiOpts, tui.WithPlanAuthorFactory(ctx, rt.PlanAuthorFactory))
			tuiOpts = append(tuiOpts, tui.WithJobBroker(ctx, jobBroker))
			tuiOpts = append(tuiOpts, tui.WithWatchBroker(ctx, watchBroker))
			tuiOpts = append(tuiOpts, tui.WithWork(rt))
			tuiOpts = append(tuiOpts, tui.WithSteeringBroker(ctx, steeringBroker))
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
			tuiOpts = append(tuiOpts, tui.WithReviewDispatcher(
				func(ctx context.Context, focus, model, reviewRange string) error {
					// Register the review operation's stop group at the dispatch
					// entry point, before the subagent is built: a stop that
					// arrives while the review is still starting must have a
					// target, and the review worker inherits this group so a
					// command it launches is reachable from the same stop.
					releaseStop := beginSessionStop(rt.State, "")
					defer releaseStop()
					return runReviewSubagent(ctx, rt.State, rt.CustomAgentFactory, focus, model, reviewRange)
				},
			))
			// Phase 2 next-prompt suggestion LLM fallback: a background
			// one-shot call through the runner's provider when the
			// deterministic rules produce no suggestion. The strict prompt
			// asks for the user's most likely next reply in at most 10
			// words, or "NONE".
			tuiOpts = append(tuiOpts, tui.WithSuggestionProvider(
				func(ctx context.Context, lastMsg string) (string, error) {
					rt.mu.Lock()
					runner := rt.Runner
					rt.mu.Unlock()
					if runner == nil {
						return "", nil
					}
					res, err := runner.Chat(ctx, []schema.ChatMessage{
						{Role: schema.RoleUser, Content: lastMsg},
						{Role: schema.RoleSystem, Content: "Suggest the user's most likely next reply in at most 10 words, or NONE."},
					})
					if err != nil {
						return "", err
					}
					text := strings.TrimSpace(res.Content)
					if strings.EqualFold(text, "NONE") || text == "" {
						return "", nil
					}
					return text, nil
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
		// /new and /clear swap in a brand-new session via the runtime. This
		// is wired outside the ProviderError block so the commands stay
		// available even when the initial provider build failed.
		tuiOpts = append(tuiOpts, tui.WithSessionSwapper(tui.SessionSwapperFunc(func(name string) (tui.SessionSwapResult, error) {
			state, runner, swarmRunner, pipelineFactory, planAuthorFactory, swarmOverrideFactory, toolReg, err := rt.NewSession(name)
			if err != nil {
				return tui.SessionSwapResult{}, err
			}
			return tui.SessionSwapResult{
				State:                state,
				Runner:               runner,
				SwarmRunner:          swarmRunner,
				PipelineFactory:      pipelineFactory,
				SwarmOverrideFactory: swarmOverrideFactory,
				PlanAuthorFactory:    planAuthorFactory,
				ToolRegistry:         toolReg,
				ReviewDispatcher: func(ctx context.Context, focus, model, reviewRange string) error {
					// Same contract as the startup wiring: register the review
					// operation's stop group at dispatch, before any worker work.
					releaseStop := beginSessionStop(state, "")
					defer releaseStop()
					return runReviewSubagent(ctx, state, rt.CustomAgentFactory, focus, model, reviewRange)
				},
			}, nil
		})))

		logger.Info("marshal started", "project", cfg.Project.Name, "working_dir", workingDir)

		// ── Worker lifecycle ──────────────────────────────────────────────
		// Start injected workers (test seam) or construct the index watcher
		// when embeddings are configured and the watcher is enabled.
		workers := runOpts.workers
		if len(workers) == 0 {
			// Surface a hint when embedding indexing is enabled but the
			// embedding route cannot resolve — semantic search reports
			// "unavailable" until it does. embeddingStartupWarning
			// distinguishes "nothing configured" from a preset that is
			// named but unresolvable (e.g. its provider entry vanished),
			// so the hint points at the actual problem.
			if msg, ok := embeddingStartupWarning(cfg); ok {
				logger.Warn("embedding route not resolvable while use_embeddings is enabled")
				state.AddMessage(session.RoleSystem, msg, session.ContentTypePlain)
			}

			// Build the LSP symbol adapter for the index pass.
			var lspAdapter index.LSPSymbols
			if rt.LSPManager != nil {
				lspAdapter = lsp.NewSymbolAdapter(rt.LSPManager)
			}

			workers = buildIndexWorkers(cfg, state, database, projectID, workingDir, lspAdapter,
				must[*pubsub.Broker[index.Report]](rt.IndexBroker), logger)
		}
		workers = append(workers, buildOAuthWorkers(cfg, logger, rt.AuthResolver)...)
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

		// The TUI can raise a masked secret prompt; ACP/web cannot, so
		// shell.sudo refuses there rather than blocking on a prompt no
		// transport can answer.
		state.SetSecretPromptCapable(true)

		// Exit analysis (Task 8, step 5): the closure's optional postmortem pass
		// runs as a SCOPED exit-analysis operation. The runtime opens a dedicated
		// work epoch for it and stamps the authorization onto the context it
		// hands this operation, so the pass may start only during the postmortem
		// phase, its commands and children inherit the epoch, and the epoch is
		// stopped and joined before closure whatever the outcome. It never
		// reopens ordinary conversation work, and it cannot recursively trigger
		// the active-work warning.
		//
		// The TEMPORARY ELEVATION the pass needs lives here, not in the TUI. The
		// pass writes the report at an absolute path outside the workspace, so
		// it needs system access and an editing approval mode; granting either
		// from the model would leave an ordinary prompt elevated if the pass
		// failed. Both are saved before the run and restored after it, whatever
		// the outcome.
		rt.SetExitAnalysisOperation(func(analysisCtx context.Context, scope *session.ExitAnalysisScope, goal string) error {
			if rt.Runner == nil {
				return fmt.Errorf("exit analysis: no agent runner")
			}
			return runWithExitAnalysisElevation(rt, func() error {
				return rt.Runner.Run(analysisCtx, goal)
			})
		})

		// The TUI's structural end seam: acceptance, settlement and the scoped
		// exit analysis are reached through neutral data, so the model decides
		// nothing about policy or processes.
		tuiOpts = append(tuiOpts, tui.WithEndController(&tuiEndController{rt: rt}))

		progResult := runOpts.programRunner(ctx, tui.New(state, tuiOpts...), stdout)

		if progResult.ResumeSession != "" && progResult.Err == nil {
			// User chose to resume a different session. Tear down the
			// current runtime (the current transcript is already persisted,
			// and that session is marked ended when it is eventually exited)
			// and restart startRuntime in
			// existing-session mode.
			_ = rt.Close(context.Background())
			runOpts.sessionID = ""
			runOpts.existingSessionID = progResult.ResumeSession
			continue
		}

		progErr := progResult.Err
		if !reloadForTrust {
			// Phase 1: quiesce — cancel and join active work/jobs without
			// closing persistence so the session can be marked ended.
			quiesceCtx, cancelQuiesce := context.WithTimeout(context.Background(), 1*time.Second)
			quiesceErr := rt.Quiesce(quiesceCtx)
			cancelQuiesce()

			// Workers are cancelled via rt.workCtx (cancelled in Quiesce
			// above) and reclaimed by process exit — no need to wait.

			// Phase 2: mark the session ended while the DB is still open.
			if err := database.EndSession(sessionID, runOpts.now(), ""); err != nil {
				logger.Warn("mark session ended failed", "error", err, "session_id", sessionID)
			}
			if runOpts.shutdownHook != nil {
				runOpts.shutdownHook(context.Background(), state, database)
			}

			// Clean up spill files from this session.
			spillDir := filepath.Join(workingDir, ".marshal", "tool-results")
			if err := agent.CleanupSpillFiles(spillDir); err != nil {
				logger.Warn("spill file cleanup failed", "error", err)
			}

			// Phase 3: close — tear down MCP, brokers, snapshots, DB, logger.
			closeErr := rt.Close(context.Background())
			return errors.Join(progErr, quiesceErr, closeErr)
		}

		// Trust granted inline: tear down quietly and reinitialise via the
		// same startRuntime path as startup — now with the project config in
		// force. No Quiesce: no agent work ever ran.
		reloadForTrust = false
		// Use Background: the user may have cancelled ctx during the trust
		// prompt; the deferred Close covers the final rt, this covers reload
		// iterations.
		_ = rt.Close(context.Background())
	}
}

// layerReloaderFor returns the TUI's layer-reload function: it re-runs
// LoadLayers with the same home/working context as startup and replays the
// session's trust decision through config.LoadSessionLayers, which never
// prompts and never persists. The interactive TerminalResolver must never
// run here: the reloader executes inside the TUI event loop, where its
// stdin prompt would freeze the interface (the TUI owns stdin, and the fd
// is still a terminal in a real launch). A session that answered the trust
// question at startup — permanently, per-session, or not at all — keeps
// that answer for the rest of the session.
func layerReloaderFor(homeDir, workingDir string, trusted func() bool) func() (config.Layers, bool) {
	return func() (config.Layers, bool) {
		t := false
		if trusted != nil {
			t = trusted()
		}
		layers, err := config.LoadSessionLayers(homeDir, workingDir, t)
		if err != nil {
			return config.Layers{}, false
		}
		// Same template-default rebind as the startup loader so an
		// in-session reload can't resurrect a stale false.
		provider.RebindTemplateDefaults(&layers.Merged)
		return layers, true
	}
}

func reloadAgentRuntime(ctx context.Context, cfg config.Config, rt *Runtime) error {
	// A reload mutates the runtime in several phases that are not
	// individually atomic: the dry-run build, the in-place field/pointer
	// swap, and the old-generation cleanup. Serialize the whole operation.
	// The headless /mcp auth completion fires this from a different goroutine
	// than a turn's own config.*.set reload, so without this gate two reloads
	// could interleave.
	rt.reloadMu.Lock()
	defer rt.reloadMu.Unlock()

	// A reload loads config from disk, which knows nothing of the session's
	// own routing; carry it over so the session keeps its profile and
	// overrides.
	if cfg.SessionRouting.Empty() && rt.State != nil {
		cfg.SessionRouting = rt.State.Config.SessionRouting
	}

	// The incoming config is the new *configured* baseline; the mode the
	// session actually runs with may differ, because a manual selection or an
	// approved grant is a session-scoped override that an unrelated reload
	// must not silently revert. Build from the effective mode so the rebuilt
	// policy engine agrees with what the session reports, and keep the
	// configured value for the baseline update below.
	effectiveCfg := cfg
	if rt.State != nil {
		effectiveCfg.Agent.ApprovalMode = rt.State.ModeForConfig(cfg)
	}

	db := must[*db.DB](rt.DB)
	jb := must[*pubsub.Broker[native.JobEvent]](rt.JobBroker)
	// Reuse the runtime's stable WriteLock: in-flight background children
	// from the pre-reload generation hold it, so the rebuilt parent and
	// subagent factory must serialize on the same gate.
	lock := rt.WriteLock
	if lock == nil {
		lock = &swarm.WriteLock{}
	}
	// Borrow the conversation's command service. A reload changes FUTURE
	// launches only: the candidate build validates first and the new launch
	// generation is committed last, so a failed candidate leaves the service,
	// its callbacks and the previous generation untouched — and the service is
	// never closed here. A reload that shut the old manager down would kill
	// still-running commands and orphan every handle the transcript, the TUI
	// lane and agent.await already hold.
	service := rt.CommandService
	if service == nil && rt.JobManager != nil {
		// A Runtime assembled without the service field (test fixtures from
		// before the service existed) still owns a manager. Adopt it rather
		// than minting a second supervisor: that keeps the handles and
		// callbacks the fixture already holds, which is exactly the
		// ownership rule production follows.
		service = &conversationCommandService{manager: rt.JobManager, generation: 1}
		rt.CommandService = service
	}
	// The build uses effectiveCfg, not cfg: a session mode override must
	// survive the reload, so the rebuilt policy engine has to agree with what
	// the session reports.
	newRunner, newReg, newSwarmRunner, newMCP, newSnap, newJobMgr, newWatchMgr, newDesktopCloser, newSubagentFactory, newPipelineFactory, newPlanAuthorFactory, newSwarmOverrideFactory, err := buildAgentRunnerWithLock(rt.workCtx, effectiveCfg, rt.State, db, rt.ProjectID, rt.SkillIndex, rt.DataDir, rt.additionalDirs, jb, must[*pubsub.Broker[watch.Event]](rt.WatchBroker), rt.WatchResume, rt.ConfigReloader, rt.HomeDir, lock, service, rt.LSPManager, rt.AuthResolver)
	if err != nil {
		// A failed dry run leaves the configured baseline, the override, the
		// system flag and the existing runner exactly as they were.
		slog.Default().Warn("reload: dry-run build failed; keeping previous config",
			"err", err)
		rt.State.AddMessage(session.RoleSystem,
			"Config reload failed; keeping previous settings.",
			session.ContentTypePlain)
		return err
	}

	// Capture old values for cleanup under the pointer mutex.
	rt.mu.Lock()
	// Config validated — install it under both the reload gate (one writer
	// at a time) and the pointer mutex. The session rebases against the
	// configured value: an unchanged configured mode keeps the session
	// override, an explicit change to it clears the override, and the system
	// flag survives either way.
	rt.State.RebaseSessionConfig(cfg)
	// Runtime.Config stays the configured baseline, never the effective mode:
	// it is what a later reload and a new session start from.
	rt.Config = cfg
	oldMCP := rt.MCPManager
	oldWatchMgr := rt.WatchManager
	oldDesktopCloser := rt.DesktopCloser

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
	// The live runner keeps the runtime's snapshot-warning suppression state,
	// whether it was mutated in place or adopted wholesale: a reload must not
	// re-warn the user about a snapshot condition they have already been told
	// about, and a capture that succeeds later must still clear the state that
	// suppressed it.
	if rt.SnapshotWarnings != nil {
		rt.Runner.SetSnapshotWarningState(rt.SnapshotWarnings)
	}
	// The rebuilt policy engine was constructed from the effective mode, but
	// CopyFrom may have carried over the pre-reload engine's state; re-assert
	// the session's effective access pair so the runner and the session agree
	// after the swap. This is presentation of already-decided state, not a new
	// grant: it applies the mode the session already reports.
	if rt.Runner != nil {
		effective := rt.State.AccessState()
		rt.Runner.SetForceClass(forceClassForMode(effective.Mode))
		rt.Runner.SetApprovalMode(policy.ApprovalMode(effective.Mode))
	}
	if rt.SwarmRunner == nil {
		rt.SwarmRunner = newSwarmRunner
	} else if newSwarmRunner != nil {
		*rt.SwarmRunner = *newSwarmRunner
	}
	if newPipelineFactory != nil {
		rt.PipelineFactory = newPipelineFactory
	}
	if newSwarmOverrideFactory != nil {
		rt.SwarmOverrideFactory = newSwarmOverrideFactory
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
	rt.JobManager = newJobMgr.manager
	rt.CommandService = newJobMgr
	rt.WatchManager = newWatchMgr
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
	// The command service is deliberately NOT shut down here. A reload is not
	// the end of the conversation: its commands keep running under the same
	// supervisor, which is what lets a foreground wait hand off and an
	// agent.await keep resolving the same handle across the reload. The
	// runtime's own Close (after Quiesce) is the only place the service ends.
	if oldWatchMgr != nil {
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := oldWatchMgr.Close(sc); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		// Reconcile the joined manager's terminal snapshot into durable history
		// before its pointer is dropped, so no settled registration is lost.
		if err := oldWatchMgr.RetryTerminalSaves(); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	// Bind the replacement watch manager to the projection only after the old
	// one has been joined and reconciled. The replacement mints its own epoch,
	// so a reused numeric registration ID is never mistaken for one of the old
	// conversation's: those results stay history under their own epoch.
	if wc := rt.currentWork(); wc != nil {
		wc.rebindWatchManager(rt.WatchManager)
	}
	if oldDesktopCloser != nil {
		oldDesktopCloser()
	}
	// The server set may have changed: re-point the LSP handle (the pool
	// keeps the running manager when it did not), and give the host the new
	// config for its index passes.
	rt.bindLSP(cfg, rt.State.Workspace().ActiveRoot)
	rt.Host.noteConfig(cfg)

	// Update the active route so the status bar reflects the new model
	// immediately, without waiting for the next turn to call resolveRoute.
	// This mirrors the startup wiring in buildAgentRunner, and reuses the
	// runtime's selected auth resolver: this throwaway resolver has its own
	// provider cache but must not reach a different credential backend. The
	// runtime's own resolver (whose engine and store caches survive the
	// reload) is deliberately untouched here — rebuilds never clear it.
	resolver := newRoutedProviderResolver(cfg, rt.DataDir, rt.AuthResolver).withSessionID(rt.State.SessionID())
	if route, rp, rErr := resolver.Resolve("edit"); rErr == nil {
		rt.State.SetActiveRoute(session.RouteInfo{
			Role:             route.Role,
			Profile:          route.Profile,
			Preset:           route.Preset.Name,
			Provider:         route.Preset.Provider,
			Model:            route.Preset.Model,
			LocalOnly:        route.Preset.LocalOnly,
			Thinking:         route.Preset.Thinking,
			ReasoningCapable: rp != nil && rp.Capabilities(ctx).Reasoning,
			Active:           true,
		})
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
// routing config, or returns nil when no embedding preset is configured.
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
