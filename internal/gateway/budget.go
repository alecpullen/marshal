package gateway

import (
	"fmt"
	"sync"
	"time"
)

// BudgetTracker manages per-session and per-role spending limits.
type BudgetTracker struct {
	mu sync.RWMutex

	// Session budget (total across all roles)
	sessionBudget float64
	sessionSpent  float64

	// Per-role budgets
	roleBudgets map[string]float64 // role -> budget
	roleSpent   map[string]float64 // role -> spent

	// Daily tracking for global limits
	dailyBudget   float64
	dailySpent    float64
	lastResetDate time.Time

	// Warning thresholds
	warnThresholds []float64 // e.g., [0.5, 0.8] for 50% and 80% warnings

	// Callbacks for budget events
	onWarning    func(role string, spent, budget float64, threshold float64)
	onExhausted  func(role string, spent, budget float64)
	onDailyReset func(budget, spent float64)
}

// BudgetOption configures the budget tracker.
type BudgetOption func(*BudgetTracker)

// WithSessionBudget sets the total session budget.
func WithSessionBudget(budget float64) BudgetOption {
	return func(b *BudgetTracker) {
		b.sessionBudget = budget
	}
}

// WithDailyBudget sets the daily global budget.
func WithDailyBudget(budget float64) BudgetOption {
	return func(b *BudgetTracker) {
		b.dailyBudget = budget
	}
}

// WithRoleBudget sets a budget for a specific role.
func WithRoleBudget(role string, budget float64) BudgetOption {
	return func(b *BudgetTracker) {
		b.roleBudgets[role] = budget
	}
}

// WithWarningThresholds sets warning thresholds (as fractions, e.g., 0.5, 0.8).
func WithWarningThresholds(thresholds []float64) BudgetOption {
	return func(b *BudgetTracker) {
		b.warnThresholds = thresholds
	}
}

// WithWarningCallback sets a callback for budget warnings.
func WithWarningCallback(cb func(role string, spent, budget float64, threshold float64)) BudgetOption {
	return func(b *BudgetTracker) {
		b.onWarning = cb
	}
}

// WithExhaustedCallback sets a callback for budget exhaustion.
func WithExhaustedCallback(cb func(role string, spent, budget float64)) BudgetOption {
	return func(b *BudgetTracker) {
		b.onExhausted = cb
	}
}

// NewBudgetTracker creates a new budget tracker.
func NewBudgetTracker(opts ...BudgetOption) *BudgetTracker {
	bt := &BudgetTracker{
		sessionBudget: 10.0, // Default $10.00 session budget
		dailyBudget:   50.0, // Default $50.00 daily budget
		roleBudgets:   make(map[string]float64),
		roleSpent:     make(map[string]float64),
		warnThresholds: []float64{0.5, 0.8, 0.95},
		lastResetDate: time.Now().UTC().Truncate(24 * time.Hour),
	}

	for _, opt := range opts {
		opt(bt)
	}

	return bt
}

// Check verifies if a request would exceed any budget limits.
// Returns ErrBudgetExceeded if any limit would be exceeded.
func (b *BudgetTracker) Check(role string, estimatedCost float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Check daily budget first
	b.maybeResetDaily()
	if b.dailySpent+estimatedCost > b.dailyBudget {
		return fmt.Errorf("%w: daily budget %.2f/%.2f exceeded",
			ErrBudgetExceeded, b.dailySpent, b.dailyBudget)
	}

	// Check session budget
	if b.sessionSpent+estimatedCost > b.sessionBudget {
		return fmt.Errorf("%w: session budget %.2f/%.2f exceeded",
			ErrBudgetExceeded, b.sessionSpent, b.sessionBudget)
	}

	// Check per-role budget if set
	if roleBudget, hasRoleBudget := b.roleBudgets[role]; hasRoleBudget {
		roleCurrent := b.roleSpent[role]
		if roleCurrent+estimatedCost > roleBudget {
			return fmt.Errorf("%w: role %s budget %.2f/%.2f exceeded",
				ErrBudgetExceeded, role, roleCurrent, roleBudget)
		}
	}

	return nil
}

// CheckRole is a convenience method that just checks the role budget.
func (b *BudgetTracker) CheckRole(role string, estimatedCost float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.maybeResetDaily()

	if b.sessionSpent+estimatedCost > b.sessionBudget {
		return ErrBudgetExceeded
	}

	if roleBudget, ok := b.roleBudgets[role]; ok {
		if b.roleSpent[role]+estimatedCost > roleBudget {
			return ErrBudgetExceeded
		}
	}

	return nil
}

// Record records actual usage against budgets.
func (b *BudgetTracker) Record(role string, binding Binding, usage Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.maybeResetDaily()

	// Calculate cost
	cost := binding.EstimateCost(usage.InputTokens, usage.OutputTokens)

	// Update session spending
	b.sessionSpent += cost

	// Update role spending
	b.roleSpent[role] += cost

	// Update daily spending
	b.dailySpent += cost

	// Check warning thresholds
	b.checkWarnings(role)
}

// RecordCost records a specific cost amount.
func (b *BudgetTracker) RecordCost(role string, cost float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.maybeResetDaily()

	b.sessionSpent += cost
	b.roleSpent[role] += cost
	b.dailySpent += cost

	b.checkWarnings(role)
}

// checkWarnings checks if any warning thresholds have been crossed.
func (b *BudgetTracker) checkWarnings(role string) {
	if b.onWarning == nil {
		return
	}

	// Check role budget warnings
	if roleBudget, ok := b.roleBudgets[role]; ok && roleBudget > 0 {
		spent := b.roleSpent[role]
		ratio := spent / roleBudget
		for _, threshold := range b.warnThresholds {
			// Trigger warning if we just crossed this threshold
			// (spent would be in [threshold*budget, (threshold+0.01)*budget] range)
			if ratio >= threshold && ratio < threshold+0.05 {
				b.onWarning(role, spent, roleBudget, threshold)
			}
		}
	}

	// Check session budget warnings
	if b.sessionBudget > 0 {
		ratio := b.sessionSpent / b.sessionBudget
		for _, threshold := range b.warnThresholds {
			if ratio >= threshold && ratio < threshold+0.05 {
				b.onWarning("session", b.sessionSpent, b.sessionBudget, threshold)
			}
		}
	}
}

// maybeResetDaily resets daily spending if a new day has started.
func (b *BudgetTracker) maybeResetDaily() {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if today.After(b.lastResetDate) {
		// Reset daily spending
		b.dailySpent = 0
		b.lastResetDate = today

		if b.onDailyReset != nil {
			b.onDailyReset(b.dailyBudget, 0)
		}
	}
}

// GetRemaining returns remaining budget for a role.
func (b *BudgetTracker) GetRemaining(role string) (sessionRemaining, roleRemaining float64) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	b.maybeResetDaily()

	sessionRemaining = b.sessionBudget - b.sessionSpent
	if sessionRemaining < 0 {
		sessionRemaining = 0
	}

	if roleBudget, ok := b.roleBudgets[role]; ok {
		roleRemaining = roleBudget - b.roleSpent[role]
		if roleRemaining < 0 {
			roleRemaining = 0
		}
	} else {
		roleRemaining = sessionRemaining // No role-specific limit
	}

	return sessionRemaining, roleRemaining
}

// GetStatus returns comprehensive budget status.
func (b *BudgetTracker) GetStatus() BudgetStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()

	b.maybeResetDaily()

	return BudgetStatus{
		SessionBudget:   b.sessionBudget,
		SessionSpent:    b.sessionSpent,
		SessionRemaining: b.sessionBudget - b.sessionSpent,
		DailyBudget:     b.dailyBudget,
		DailySpent:      b.dailySpent,
		DailyRemaining:  b.dailyBudget - b.dailySpent,
		RoleBudgets:     copyMap(b.roleBudgets),
		RoleSpent:       copyMap(b.roleSpent),
	}
}

// BudgetStatus captures the current budget state.
type BudgetStatus struct {
	SessionBudget    float64            `json:"session_budget"`
	SessionSpent     float64            `json:"session_spent"`
	SessionRemaining float64            `json:"session_remaining"`
	DailyBudget      float64            `json:"daily_budget"`
	DailySpent       float64            `json:"daily_spent"`
	DailyRemaining   float64            `json:"daily_remaining"`
	RoleBudgets      map[string]float64 `json:"role_budgets"`
	RoleSpent        map[string]float64 `json:"role_spent"`
}

// SetRoleBudget sets a budget for a role dynamically.
func (b *BudgetTracker) SetRoleBudget(role string, budget float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.roleBudgets[role] = budget
}

// Reset resets all spending counters.
func (b *BudgetTracker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.sessionSpent = 0
	b.roleSpent = make(map[string]float64)
	b.dailySpent = 0
	b.lastResetDate = time.Now().UTC().Truncate(24 * time.Hour)
}

// EstimateAndCheck combines estimation and budget check.
func (b *BudgetTracker) EstimateAndCheck(role string, binding Binding, inputTokens, outputTokens int) error {
	estimatedCost := binding.EstimateCost(inputTokens, outputTokens)
	return b.Check(role, estimatedCost)
}

// copyMap creates a shallow copy of a map.
func copyMap(m map[string]float64) map[string]float64 {
	if m == nil {
		return nil
	}
	result := make(map[string]float64, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
