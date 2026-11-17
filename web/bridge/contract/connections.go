package contract

// Credential is one forge credential as GET /api/credentials answers it
// (web/bridge/repos_http.go). It never carries a value: Set says whether the
// secret it points at can be read now, and nothing on the wire can write or
// read that secret.
type Credential struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // none, pat, ssh or vault
	// EnvVar is where a pat credential's token is read from.
	EnvVar string `json:"envVar,omitempty"`
	// KeyPath is the private key an ssh credential names.
	KeyPath string `json:"keyPath,omitempty"`
	// Ref is the vault path a vault credential names.
	Ref  string `json:"ref,omitempty"`
	User string `json:"user,omitempty"`
	// Set reports whether the secret exists; the credential is refused at use
	// when it does not.
	Set bool `json:"set"`
}

// CredentialInput is POST /api/credentials' body: the same record without the
// read-only Set.
type CredentialInput struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	EnvVar  string `json:"envVar,omitempty"`
	KeyPath string `json:"keyPath,omitempty"`
	Ref     string `json:"ref,omitempty"`
	User    string `json:"user,omitempty"`
}

// Repo is one registered repository: GET /api/repos' row and POST /api/repos'
// body (web/bridge/workspace.go).
type Repo struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Branch  string `json:"branch,omitempty"`
	CredRef string `json:"credRef,omitempty"`
	OwnerID string `json:"ownerId"`
	// Forge names the hosting provider's API family: github or gitea.
	Forge string `json:"forge,omitempty"`
	// APIBase overrides the API root for a self-hosted instance.
	APIBase string `json:"apiBase,omitempty"`
	// Watch turns the issue poller on, and WatchLabel is the label that
	// triggers a spawn.
	Watch      bool   `json:"watch,omitempty"`
	WatchLabel string `json:"watchLabel,omitempty"`
	// LastPolled and LastPollErr are what the poller last did.
	LastPolled  string `json:"lastPolled,omitempty"`
	LastPollErr string `json:"lastPollErr,omitempty"`
}
