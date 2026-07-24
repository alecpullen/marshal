package trust

import (
	"log/slog"
	"path/filepath"
)

// HeadlessResolver implements Resolver for headless (ACP) sessions.
// It never prompts the user and never persists trust decisions.
// Record is a no-op.
type HeadlessResolver struct {
	store   *Store
	log     *slog.Logger
	session map[string]bool
}

// NewHeadlessResolver creates a HeadlessResolver that uses store for
// reading previously-recorded permanent trust and log for warnings.
func NewHeadlessResolver(store *Store, log *slog.Logger) *HeadlessResolver {
	return &HeadlessResolver{
		store:   store,
		log:     log,
		session: map[string]bool{},
	}
}

// Resolve resolves trust for workingDir without user interaction.
//
//   - If no stored trust exists, returns DecisionTrustSession and logs a warning.
//   - If stored permanent trust exists and the config hash matches, returns DecisionTrustPermanent.
//   - If stored hash doesn't match (config changed), degrades to DecisionTrustSession.
func (r *HeadlessResolver) Resolve(workingDir string, hasProjectConfig bool) (Decision, error) {
	if !hasProjectConfig {
		return DecisionDontTrust, nil
	}

	abs, _ := filepath.Abs(workingDir)

	// Check session cache first.
	if r.session[abs] {
		return DecisionTrustSession, nil
	}

	trusted, err := r.store.IsTrusted(abs)
	if err != nil {
		return DecisionDontTrust, err
	}

	if trusted {
		currentHash, hashErr := ConfigHashFor(workingDir)
		if hashErr != nil {
			return DecisionDontTrust, hashErr
		}
		storedHash, _ := r.store.StoredConfigHash(abs)
		if storedHash == currentHash {
			return DecisionTrustPermanent, nil
		}
		// Config changed: degrade to session trust.
		r.session[abs] = true
		r.log.Warn("project config changed since trust was recorded; degrading to session trust",
			"dir", abs,
		)
		return DecisionTrustSession, nil
	}

	// No stored trust: grant session trust with a warning.
	r.session[abs] = true
	r.log.Warn("no stored trust for project; granting session trust in headless mode",
		"dir", abs,
	)
	return DecisionTrustSession, nil
}

// Record is a no-op — headless mode never persists trust decisions.
func (r *HeadlessResolver) Record(workingDir string, decision Decision) error {
	return nil
}
