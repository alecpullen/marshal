package contract

// Budgets mirrors the engine's [budgets] section on the wire.
type Budgets struct {
	DailyUSD    float64 `json:"dailyUsd"`
	PerAgentUSD float64 `json:"perAgentUsd"`
	OnDailyCap  string  `json:"onDailyCap"`
	OnAgentCap  string  `json:"onAgentCap"`
}

// BudgetAgentSpend is one agent's spend against its cap.
type BudgetAgentSpend struct {
	AgentID    string  `json:"agentId"`
	SpentUSD   float64 `json:"spentUsd"`
	Paused     bool    `json:"paused"`
	Overridden bool    `json:"overridden"`
}

// BudgetDaily is today's spend against the daily cap.
type BudgetDaily struct {
	Day      string  `json:"day"`
	SpentUSD float64 `json:"spentUsd"`
	Blocked  bool    `json:"blocked"`
}

// BudgetReport is GET /api/budgets' answer.
type BudgetReport struct {
	Budgets Budgets            `json:"budgets"`
	Daily   BudgetDaily        `json:"daily"`
	Agents  []BudgetAgentSpend `json:"agents"`
	// Loaded is false while the caps could not be read from the engine, so
	// clients know Budgets is the zero value rather than the real caps.
	Loaded bool `json:"loaded"`
}
