package contract

// The secrets routes (web/bridge/secrets_http.go). A value goes in and never
// comes out: GET /api/secrets answers references, and no route answers a
// value at all.

// SecretsStatus is GET /api/secrets/status' answer: which backend holds the
// secrets and whether it answers.
type SecretsStatus struct {
	Backend string `json:"backend"` // env, local or openbao
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

// SecretList is GET /api/secrets' answer: references only, each as
// `vault:<path>`.
type SecretList struct {
	Refs []string `json:"refs"`
}

// SecretWrite is PUT /api/secrets/{ref}'s body. It goes one way.
type SecretWrite struct {
	Value string `json:"value"`
}
