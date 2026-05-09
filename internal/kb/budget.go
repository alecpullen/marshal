// Package kb implements Phase 3.8 budget tracking for LLM-backed summaries.
// This file provides daily cost caps and priority queue management.
package kb

import (
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Default budget settings
const (
	DefaultDailyBudgetCents = 50  // $0.50/day default
	BudgetResetHour         = 0   // Reset at midnight UTC
)

// BudgetTracker enforces daily spending caps for summarization.
type BudgetTracker struct {
	dailyCapCents  int
	spentTodayCents int
	lastResetDate  string // YYYY-MM-DD format
	
	db *sql.DB
	mu sync.RWMutex
}

// BudgetStats holds current budget state for reporting.
type BudgetStats struct {
	DailyCapCents   int
	SpentTodayCents int
	RemainingCents  int
	LastResetDate   string
	IsExhausted     bool
}

// NewBudgetTracker creates a budget tracker with daily cap.
func NewBudgetTracker(db *sql.DB, dailyCapCents int) (*BudgetTracker, error) {
	if dailyCapCents <= 0 {
		dailyCapCents = DefaultDailyBudgetCents
	}
	
	bt := &BudgetTracker{
		dailyCapCents: dailyCapCents,
		db:            db,
	}
	
	// Initialize schema
	if err := bt.initSchema(); err != nil {
		return nil, fmt.Errorf("initializing budget schema: %w", err)
	}
	
	// Load state from DB
	if err := bt.loadState(); err != nil {
		return nil, fmt.Errorf("loading budget state: %w", err)
	}
	
	// Check if we need to reset for a new day
	bt.checkAndReset()
	
	return bt, nil
}

// initSchema creates the budget tracking table.
func (bt *BudgetTracker) initSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS kb_budget (
    key TEXT PRIMARY KEY,
    value INTEGER NOT NULL
);

-- Seed default values
INSERT OR IGNORE INTO kb_budget (key, value) VALUES ('daily_cap_cents', ?);
INSERT OR IGNORE INTO kb_budget (key, value) VALUES ('spent_today_cents', 0);
INSERT OR IGNORE INTO kb_budget (key, value) VALUES ('last_reset_date', '');
`
	_, err := bt.db.Exec(schema, DefaultDailyBudgetCents)
	return err
}

// loadState loads budget state from database.
func (bt *BudgetTracker) loadState() error {
	// Load daily cap
	var cap int
	err := bt.db.QueryRow(`SELECT value FROM kb_budget WHERE key = 'daily_cap_cents'`).Scan(&cap)
	if err == nil {
		bt.dailyCapCents = cap
	}
	
	// Load spent today
	var spent int
	err = bt.db.QueryRow(`SELECT value FROM kb_budget WHERE key = 'spent_today_cents'`).Scan(&spent)
	if err == nil {
		bt.spentTodayCents = spent
	}
	
	// Load last reset date
	var date string
	err = bt.db.QueryRow(`SELECT value FROM kb_budget WHERE key = 'last_reset_date'`).Scan(&date)
	if err == nil {
		bt.lastResetDate = date
	}
	
	return nil
}

// saveState persists current state to database.
func (bt *BudgetTracker) saveState() error {
	tx, err := bt.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	
	_, err = tx.Exec(`INSERT OR REPLACE INTO kb_budget (key, value) VALUES ('spent_today_cents', ?)`, 
		bt.spentTodayCents)
	if err != nil {
		return err
	}
	
	_, err = tx.Exec(`INSERT OR REPLACE INTO kb_budget (key, value) VALUES ('last_reset_date', ?)`, 
		bt.lastResetDate)
	if err != nil {
		return err
	}
	
	_, err = tx.Exec(`INSERT OR REPLACE INTO kb_budget (key, value) VALUES ('daily_cap_cents', ?)`, 
		bt.dailyCapCents)
	if err != nil {
		return err
	}
	
	return tx.Commit()
}

// checkAndReset resets the budget if it's a new day.
func (bt *BudgetTracker) checkAndReset() {
	today := time.Now().UTC().Format("2006-01-02")
	
	if bt.lastResetDate != today {
		// New day - reset spent counter
		bt.spentTodayCents = 0
		bt.lastResetDate = today
		bt.saveState()
	}
}

// Allowed returns true if the budget allows spending `cents` more.
func (bt *BudgetTracker) Allowed(cents int) bool {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	
	bt.checkAndReset()
	
	return bt.spentTodayCents+cents <= bt.dailyCapCents
}

// Charge records a cost against the budget.
// Returns error if it would exceed the cap (but still charges up to cap).
func (bt *BudgetTracker) Charge(cents int) error {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	
	bt.checkAndReset()
	
	// Check if already exhausted
	if bt.spentTodayCents >= bt.dailyCapCents {
		return fmt.Errorf("budget exhausted: spent %d/%d cents", bt.spentTodayCents, bt.dailyCapCents)
	}
	
	// Calculate actual charge (don't exceed cap)
	available := bt.dailyCapCents - bt.spentTodayCents
	actualCharge := cents
	if actualCharge > available {
		actualCharge = available
	}
	
	bt.spentTodayCents += actualCharge
	
	// Persist
	if err := bt.saveState(); err != nil {
		return fmt.Errorf("saving budget state: %w", err)
	}
	
	// Warn if charged less than requested
	if actualCharge < cents {
		return fmt.Errorf("partial charge: %d/%d cents (budget exhausted)", actualCharge, cents)
	}
	
	return nil
}

// ChargeFloat charges a floating-point dollar amount.
func (bt *BudgetTracker) ChargeFloat(dollars float64) error {
	cents := int(dollars * 100)
	return bt.Charge(cents)
}

// Remaining returns how much budget is left (in cents).
func (bt *BudgetTracker) Remaining() int {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	
	bt.checkAndReset()
	
	remaining := bt.dailyCapCents - bt.spentTodayCents
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Stats returns current budget statistics.
func (bt *BudgetTracker) Stats() BudgetStats {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	
	bt.checkAndReset()
	
	return BudgetStats{
		DailyCapCents:   bt.dailyCapCents,
		SpentTodayCents: bt.spentTodayCents,
		RemainingCents:  bt.Remaining(),
		LastResetDate:   bt.lastResetDate,
		IsExhausted:     bt.spentTodayCents >= bt.dailyCapCents,
	}
}

// SetDailyCap updates the daily budget cap (persists to DB).
func (bt *BudgetTracker) SetDailyCap(cents int) error {
	if cents <= 0 {
		return fmt.Errorf("daily cap must be positive")
	}
	
	bt.mu.Lock()
	bt.dailyCapCents = cents
	bt.mu.Unlock()
	
	return bt.saveState()
}

// GetDailyCap returns the current daily cap.
func (bt *BudgetTracker) GetDailyCap() int {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	return bt.dailyCapCents
}

// Reset manually resets the budget (for testing or admin override).
func (bt *BudgetTracker) Reset() {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	
	bt.spentTodayCents = 0
	bt.lastResetDate = time.Now().UTC().Format("2006-01-02")
	bt.saveState()
}

// SummaryQueue manages pending summarization tasks with priority.
type SummaryQueue struct {
	items  []SummaryTask
	budget *BudgetTracker
	mu     sync.Mutex
}

// SummaryTask represents a pending summarization job.
type SummaryTask struct {
	Path     string    // File or package path
	Type     string    // "file", "package", "project"
	Priority int       // Higher = more urgent
	AddedAt  time.Time
	Attempts int       // Retry count
}

// Priority levels for different task types.
const (
	PriorityUserRequest   = 100 // User explicitly requested
	PriorityUserFile    = 80  // File in user session
	PriorityRecent      = 60  // Recently modified
	PriorityBackground  = 40  // Standard background
	PriorityBulk        = 20  // Bulk operations
	PriorityVendored    = 10  // Vendored deps (lowest)
)

// NewSummaryQueue creates a priority queue for summary tasks.
func NewSummaryQueue(budget *BudgetTracker) *SummaryQueue {
	return &SummaryQueue{
		items:  make([]SummaryTask, 0),
		budget: budget,
	}
}

// Add inserts a task into the priority queue.
func (q *SummaryQueue) Add(task SummaryTask) {
	q.mu.Lock()
	defer q.mu.Unlock()
	
	if task.AddedAt.IsZero() {
		task.AddedAt = time.Now()
	}
	
	// Insert in priority order (highest first)
	insertIdx := len(q.items)
	for i, existing := range q.items {
		if task.Priority > existing.Priority {
			insertIdx = i
			break
		}
	}
	
	// Shift and insert
	q.items = append(q.items, SummaryTask{})
	copy(q.items[insertIdx+1:], q.items[insertIdx:])
	q.items[insertIdx] = task
}

// Next retrieves and removes the highest priority task.
// Returns nil if queue is empty or budget exhausted.
func (q *SummaryQueue) Next() *SummaryTask {
	q.mu.Lock()
	defer q.mu.Unlock()
	
	// Check budget first
	if q.budget.Remaining() <= 0 {
		return nil
	}
	
	if len(q.items) == 0 {
		return nil
	}
	
	// Pop highest priority
	task := q.items[0]
	q.items = q.items[1:]
	
	return &task
}

// Peek returns the highest priority task without removing it.
func (q *SummaryQueue) Peek() *SummaryTask {
	q.mu.Lock()
	defer q.mu.Unlock()
	
	if len(q.items) == 0 {
		return nil
	}
	
	return &q.items[0]
}

// Len returns the queue length.
func (q *SummaryQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// IsEmpty returns true if queue has no items.
func (q *SummaryQueue) IsEmpty() bool {
	return q.Len() == 0
}

// Reprioritize updates priorities based on age.
// Tasks that have been waiting get boosted.
func (q *SummaryQueue) Reprioritize() {
	q.mu.Lock()
	defer q.mu.Unlock()
	
	now := time.Now()
	for i := range q.items {
		age := now.Sub(q.items[i].AddedAt)
		
		// Boost priority based on wait time
		switch {
		case age > 1*time.Hour:
			q.items[i].Priority += 10
		case age > 10*time.Minute:
			q.items[i].Priority += 5
		}
	}
	
	// Re-sort
	q.sortByPriority()
}

// sortByPriority re-sorts items by priority (descending).
func (q *SummaryQueue) sortByPriority() {
	// Simple bubble sort for small queues
	for i := 0; i < len(q.items); i++ {
		for j := i + 1; j < len(q.items); j++ {
			if q.items[j].Priority > q.items[i].Priority {
				q.items[i], q.items[j] = q.items[j], q.items[i]
			}
		}
	}
}

// RemoveCompleted removes a task by path (called after successful processing).
func (q *SummaryQueue) RemoveCompleted(path string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	
	for i, task := range q.items {
		if task.Path == path {
			// Remove by slicing
			q.items = append(q.items[:i], q.items[i+1:]...)
			return
		}
	}
}

// GetStats returns queue statistics.
func (q *SummaryQueue) GetStats() (total int, byPriority map[int]int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	
	byPriority = make(map[int]int)
	for _, task := range q.items {
		byPriority[task.Priority]++
	}
	
	return len(q.items), byPriority
}