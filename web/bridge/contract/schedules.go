package contract

// Schedule is one cron entry that runs a recipe: GET /api/schedules' answer,
// and the body POST /api/schedules and PUT /api/schedules/{id} take
// (web/bridge/schedules.go). `lastRun` is omitted until the schedule has
// run, so a client reading it must allow for its absence.
type Schedule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Recipe  string `json:"recipe"`
	Project string `json:"project,omitempty"`
	// RepoID and Ref run the recipe on a fresh checkout of a registered repo
	// instead of a local project.
	RepoID       string            `json:"repoId,omitempty"`
	Ref          string            `json:"ref,omitempty"`
	Inputs       map[string]string `json:"inputs,omitempty"`
	Cron         string            `json:"cron"`
	Enabled      bool              `json:"enabled"`
	LastRun      string            `json:"lastRun,omitempty"`
	LastRunAgent string            `json:"lastRunAgent,omitempty"`
	LastResult   string            `json:"lastResult,omitempty"`
	// OwnerID is the owner the bridge stored the schedule under.
	OwnerID string `json:"ownerId"`
}
