package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Budget actions, matching the engine's [budgets] values.
const (
	budgetWarn  = "warn"
	budgetBlock = "block"
	budgetPause = "pause"
)

// Budget retry and timeout tuning. Loading budgets needs the control
// agent, which may not be up, so a failure is remembered briefly instead
// of being retried on every spawn and every usage row.
const (
	budgetLoadTimeout = 5 * time.Second
	budgetLoadBackoff = 30 * time.Second
	// agentSpendWindow is how far back the per-agent totals are seeded.
	agentSpendWindow = 30 * 24 * time.Hour
	// seedRaceWindow is how recent a seeded row must be to be tracked as
	// possibly double-counted.
	seedRaceWindow = 5 * time.Minute
)

// ErrBudget is returned when a spend cap refuses new work. Scope is
// "daily" or "agent". The HTTP layer maps it to 429.
type ErrBudget struct{ Scope string }

func (e ErrBudget) Error() string { return "budget_exceeded: " + e.Scope }

// Budgets mirrors the engine's [budgets] section on the wire.
type Budgets struct {
	DailyUSD    float64 `json:"dailyUsd"`
	PerAgentUSD float64 `json:"perAgentUsd"`
	OnDailyCap  string  `json:"onDailyCap"`
	OnAgentCap  string  `json:"onAgentCap"`
}

// budgetDelta is a "budget" fleet delta. Its fields are flat, as the spec
// lists them.
type budgetDelta struct {
	Kind      string  `json:"kind"`
	SessionID string  `json:"sessionId"`
	Scope     string  `json:"scope"`
	AgentID   string  `json:"agentId,omitempty"`
	SpentUSD  float64 `json:"spentUsd"`
	CapUSD    float64 `json:"capUsd"`
	Action    string  `json:"action"`
}

// budgetState enforces the caps. The engine only stores them: spend spans
// agents, so the bridge, which sees every usage row, does the arithmetic.
type budgetState struct {
	f *Fleet

	// loadMu serializes loads so concurrent callers share one fetch.
	loadMu   sync.Mutex
	failedAt time.Time

	mu     sync.Mutex
	loaded bool
	cfg    Budgets
	// day is the UTC date daySpend refers to.
	day        string
	daySpend   int64 // micro-dollars
	dayAlerted bool
	blockedDay string
	agentSpend map[string]int64 // micro-dollars
	agentAlert map[string]bool
	paused     map[string]bool
	overridden map[string]bool
	seededRows map[string]struct{}
}

func newBudgetState(f *Fleet) *budgetState {
	return &budgetState{
		f:          f,
		agentSpend: map[string]int64{},
		agentAlert: map[string]bool{},
		paused:     map[string]bool{},
		overridden: map[string]bool{},
		seededRows: map[string]struct{}{},
	}
}

func dayOf(t time.Time) string { return t.UTC().Format("2006-01-02") }

// fetchBudgets reads the stored caps from the control agent.
func (f *Fleet) fetchBudgets(ctx context.Context) (Budgets, error) {
	raw, err := f.controlCall(ctx, "config/get", nil, false)
	if err != nil {
		return Budgets{}, err
	}
	var res struct {
		Budgets Budgets `json:"budgets"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return Budgets{}, fmt.Errorf("decode config/get: %w", err)
	}
	return res.Budgets, nil
}

// ensureLoaded loads the caps and seeds spend from the ledger on first
// use. A failed load leaves no caps in force and is retried after a
// backoff.
func (b *budgetState) ensureLoaded() {
	b.mu.Lock()
	loaded := b.loaded
	b.mu.Unlock()
	if loaded {
		return
	}
	b.loadMu.Lock()
	defer b.loadMu.Unlock()
	b.mu.Lock()
	loaded = b.loaded
	b.mu.Unlock()
	if loaded || (!b.failedAt.IsZero() && b.f.now().Sub(b.failedAt) < budgetLoadBackoff) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), budgetLoadTimeout)
	defer cancel()
	cfg, err := b.f.fetchBudgets(ctx)
	if err != nil {
		b.failedAt = b.f.now()
		slog.Default().Debug("webbridge: budgets not loaded", "err", err)
		return
	}
	now := b.f.now()
	today := dayOf(now)
	rows, _ := b.f.usage.Range(now.Add(-agentSpendWindow), now.Add(time.Nanosecond))

	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfg, b.loaded, b.day = cfg, true, today
	b.daySpend = 0
	b.agentSpend = map[string]int64{}
	for _, r := range rows {
		b.agentSpend[r.AgentID] += r.micro()
		if dayOf(r.started()) == today {
			b.daySpend += r.micro()
		}
		// A row appended while this seed ran is also counted by its own
		// checkBudgets call; remembering it lets that call skip the add.
		// Only a very recent row can be in that window.
		if now.Sub(r.started()) < seedRaceWindow {
			b.seededRows[r.key()] = struct{}{}
		}
	}
}

// rollover starts a new day's tally when the UTC date changed. A daily
// block set on an earlier day lapses by itself: it only holds when
// blockedDay is today. Called with b.mu held.
func (b *budgetState) rollover(now time.Time) {
	if today := dayOf(now); b.day != today {
		b.day, b.daySpend, b.dayAlerted = today, 0, false
	}
}

// checkBudgets adds one new usage row to the tallies and applies the
// configured action when a cap is reached.
func (f *Fleet) checkBudgets(row UsageRow) {
	b := f.budgets
	b.ensureLoaded()
	now := f.now()

	var deltas []budgetDelta
	var cancelAgent string

	b.mu.Lock()
	if !b.loaded {
		b.mu.Unlock()
		return
	}
	b.rollover(now)
	if _, counted := b.seededRows[row.key()]; counted {
		delete(b.seededRows, row.key())
	} else {
		b.agentSpend[row.AgentID] += row.micro()
		if dayOf(row.started()) == b.day {
			b.daySpend += row.micro()
		}
	}
	if cap := b.cfg.DailyUSD; cap > 0 && b.daySpend >= usdToMicro(cap) && !b.dayAlerted {
		b.dayAlerted = true
		action := b.cfg.OnDailyCap
		if action != budgetBlock {
			action = budgetWarn
		}
		if action == budgetBlock {
			b.blockedDay = b.day
		}
		deltas = append(deltas, budgetDelta{Scope: "daily", SpentUSD: microToUSD(b.daySpend), CapUSD: cap, Action: action})
	}
	if cap := b.cfg.PerAgentUSD; cap > 0 && b.agentSpend[row.AgentID] >= usdToMicro(cap) &&
		!b.agentAlert[row.AgentID] && !b.overridden[row.AgentID] {
		b.agentAlert[row.AgentID] = true
		action := b.cfg.OnAgentCap
		if action != budgetPause {
			action = budgetWarn
		}
		if action == budgetPause {
			b.paused[row.AgentID] = true
			cancelAgent = row.AgentID
		}
		deltas = append(deltas, budgetDelta{Scope: "agent", AgentID: row.AgentID,
			SpentUSD: microToUSD(b.agentSpend[row.AgentID]), CapUSD: cap, Action: action})
	}
	b.mu.Unlock()

	for _, d := range deltas {
		d.Kind, d.SessionID = "budget", d.AgentID
		_, _ = f.fleetLog.Append(fleetStreamKey, d)
	}
	if cancelAgent != "" {
		if rt, err := f.runtimeForAgent(cancelAgent); err == nil {
			// Detached from any request: pausing is the bridge's own act.
			if err := rt.reg.Cancel(context.Background(), f.sessionIDFor(rt)); err != nil {
				slog.Default().Warn("webbridge: cancel paused agent failed", "agent", cancelAgent, "err", err)
			}
		}
	}
}

// budgetGate refuses new work while the day is blocked or the agent is
// paused. An empty agentID checks the daily cap only (a spawn has no
// agent yet).
func (f *Fleet) budgetGate(agentID string) error {
	b := f.budgets
	b.ensureLoaded()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blockedDay != "" && b.blockedDay == dayOf(f.now()) {
		return ErrBudget{Scope: "daily"}
	}
	if agentID != "" && b.paused[agentID] {
		return ErrBudget{Scope: "agent"}
	}
	return nil
}

// agentIDOf resolves a session id or an agent id to the agent id.
func (f *Fleet) agentIDOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if agentID, ok := f.sessionAgent[id]; ok {
		return agentID
	}
	return id
}

// applyBudgets installs new caps and lifts any block or pause the new
// caps no longer justify, so raising a cap takes effect at once.
func (b *budgetState) applyBudgets(cfg Budgets, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfg, b.loaded = cfg, true
	b.rollover(now)
	b.dayAlerted = false
	if cfg.DailyUSD <= 0 || b.daySpend < usdToMicro(cfg.DailyUSD) || cfg.OnDailyCap != budgetBlock {
		b.blockedDay = ""
	}
	for id := range b.agentAlert {
		delete(b.agentAlert, id)
	}
	for id := range b.paused {
		if cfg.PerAgentUSD <= 0 || b.agentSpend[id] < usdToMicro(cfg.PerAgentUSD) || cfg.OnAgentCap != budgetPause {
			delete(b.paused, id)
		} else {
			b.agentAlert[id] = true
		}
	}
}

type budgetAgentSpend struct {
	AgentID    string  `json:"agentId"`
	SpentUSD   float64 `json:"spentUsd"`
	Paused     bool    `json:"paused"`
	Overridden bool    `json:"overridden"`
}

type budgetReport struct {
	Budgets Budgets `json:"budgets"`
	Daily   struct {
		Day      string  `json:"day"`
		SpentUSD float64 `json:"spentUsd"`
		Blocked  bool    `json:"blocked"`
	} `json:"daily"`
	Agents []budgetAgentSpend `json:"agents"`
	// Loaded is false while the caps could not be read from the engine, so
	// clients know Budgets is the zero value rather than the real caps.
	Loaded bool `json:"loaded"`
}

func (f *Fleet) budgetReport() budgetReport {
	b := f.budgets
	b.ensureLoaded()
	b.mu.Lock()
	defer b.mu.Unlock()
	now := f.now()
	b.rollover(now)
	var rep budgetReport
	rep.Budgets, rep.Loaded = b.cfg, b.loaded
	rep.Daily.Day, rep.Daily.SpentUSD = b.day, microToUSD(b.daySpend)
	rep.Daily.Blocked = b.blockedDay != "" && b.blockedDay == b.day
	rep.Agents = []budgetAgentSpend{}
	for id, spent := range b.agentSpend {
		rep.Agents = append(rep.Agents, budgetAgentSpend{AgentID: id, SpentUSD: microToUSD(spent),
			Paused: b.paused[id], Overridden: b.overridden[id]})
	}
	sort.Slice(rep.Agents, func(i, j int) bool { return rep.Agents[i].SpentUSD > rep.Agents[j].SpentUSD })
	return rep
}

func (s *Server) getBudgets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.fleet.budgetReport())
}

func (s *Server) putBudgets(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Budgets *Budgets `json:"budgets"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Budgets == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "budgets is required"})
		return
	}
	if _, err := s.fleet.controlCall(r.Context(), "config/set_budgets",
		map[string]any{"budgets": body.Budgets}, false); err != nil {
		writeErr(w, err)
		return
	}
	// Re-read what the engine stored: it may default or clamp values, and
	// enforcement must follow the stored caps, not the request.
	cfg, err := s.fleet.fetchBudgets(r.Context())
	if err != nil {
		cfg = *body.Budgets
	}
	s.fleet.budgets.applyBudgets(cfg, s.fleet.now())
	s.fleet.auditf(AuditEvent{Event: AuditBudgetsChanged, OwnerID: DefaultOwnerID,
		Detail: fmt.Sprintf("daily=%g agent=%g", cfg.DailyUSD, cfg.PerAgentUSD)})
	writeJSON(w, http.StatusOK, s.fleet.budgetReport())
}

// overrideBudget lets a paused agent continue past its cap.
func (s *Server) overrideBudget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.fleet.ws.Agent(id); !ok {
		writeErr(w, fmt.Errorf("%w: agent %s", ErrUnknownAgent, id))
		return
	}
	b := s.fleet.budgets
	b.mu.Lock()
	b.overridden[id] = true
	delete(b.paused, id)
	b.mu.Unlock()
	s.fleet.auditf(AuditEvent{Event: AuditBudgetOverride, OwnerID: DefaultOwnerID, AgentID: id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "overridden"})
}

func (s *Server) budgetRoutes() {
	s.mux.HandleFunc("GET /api/budgets", s.getBudgets)
	s.mux.HandleFunc("PUT /api/budgets", s.putBudgets)
	s.mux.HandleFunc("POST /api/agents/{id}/budget/override", s.overrideBudget)
	s.mux.HandleFunc("GET /api/usage", s.getUsage)
}
