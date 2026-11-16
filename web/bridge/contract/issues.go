package contract

// A repository's issues as GET /api/repos/{id}/issues answers it, for the
// new-work dialog's Issues list (F3).
//
// The answer is web/bridge/forge.go's Issue, which carries no struct tags, so
// its JSON keys are the Go field names — not this package's lower-camel
// style. The keys are transcribed rather than reshaped: the route's answer is
// what the Svelte client already reads, and a client outside the agent
// process must decode what the bridge sends, not what it would prefer.
//
// Labels is `omitempty` because a forge that reported none sends null rather
// than a list, and the generated clients model an absent key as null.

// RepoIssue is one issue GET /api/repos/{id}/issues lists.
type RepoIssue struct {
	Number int      `json:"Number"`
	Title  string   `json:"Title"`
	Body   string   `json:"Body"`
	URL    string   `json:"URL"`
	Labels []string `json:"Labels,omitempty"`
}
