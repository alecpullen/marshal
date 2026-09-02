package trust

import (
	"log/slog"
)

// HeadlessResolver implements Resolver for headless (ACP) sessions.
// It never prompts the user and never persists trust decisions.
// Record is a no-op.
//
// Because there is no user present to approve a project config, Resolve is
// fail-closed: it only grants trust when a stored permanent trust record
// exists and the on-disk project config hash matches the trusted hash.
type HeadlessResolver struct {
	store *Store
	log   *slog.Logger
}

// NewHeadlessResolver creates a HeadlessResolver that uses store for
// reading previously-recorded permanent trust and log for warnings.
func NewHeadlessResolver(store *Store, log *slog.Logger) *HeadlessResolver {
	return &HeadlessResolver{
		store: store,
		log:   log,
	}
}

// Resolve resolves trust for workingDir without user interaction.
//
//   - If no project config exists, returns DecisionDontTrust.
//   - If stored permanent trust exists and the config hash matches, returns DecisionTrustPermanent.
//   - If no stored trust exists, or the stored hash doesn't match (config changed),
//     returns DecisionDontTrust. Headless sessions cannot prompt, so they must not
//     extend trust to an unverified or changed config.
func (r *HeadlessResolver) Resolve(workingDir string, hasProjectConfig bool) (Decision, error) {
	if !hasProjectConfig {
		return DecisionDontTrust, nil
	}

	abs := Canonicalize(workingDir)

	trusted, err := r.store.IsTrusted(abs)
	if err != nil {
		return DecisionDontTrust, err
	}
	if !trusted {
		r.log.Warn("no stored trust for project; refusing trust in headless mode",
			"dir", abs,
		)
		return DecisionDontTrust, nil
	}

	currentHash, hashErr := ConfigHashFor(workingDir)
	if hashErr != nil {
		return DecisionDontTrust, hashErr
	}
	storedHash, _ := r.store.StoredConfigHash(abs)
	if storedHash != currentHash {
		r.log.Warn("project config changed since trust was recorded; refusing trust in headless mode",
			"dir", abs,
		)
		return DecisionDontTrust, nil
	}

	return DecisionTrustPermanent, nil
}

// Record is a no-op — headless mode never persists trust decisions.
func (r *HeadlessResolver) Record(workingDir string, decision Decision) error {
	return nil
}
