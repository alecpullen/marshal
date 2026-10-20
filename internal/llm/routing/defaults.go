package routing

// Default model limits. Marshal no longer consults a context/output-token
// table: model datasheets have converged enough that a single documented
// default is more accurate than the stale cross-provider inferences the old
// OpenRouter/LiteLLM table produced (it once reported 64k for a model that
// serves 1M). New presets are seeded with these figures and the user can edit
// them in the confirm-limits screen or config.toml.
//
// Remote models get the modern 1M/128k pair. Local endpoints vary far more
// (a pulled model's real window is reported by Ollama's /api/show probe, which
// overrides this), so they keep the conservative pair Marshal used before.
const (
	DefaultRemoteContextWindow   = 1_000_000
	DefaultRemoteMaxOutputTokens = 128_000

	DefaultLocalContextWindow   = 8_192
	DefaultLocalMaxOutputTokens = 4_096
)

// DefaultModelLimits returns the fallback context window and max output tokens
// for a model with no saved preset and no provider-reported limits. local
// selects the conservative local pair; everything else gets the remote pair.
func DefaultModelLimits(local bool) (contextWindow, maxOutput int) {
	if local {
		return DefaultLocalContextWindow, DefaultLocalMaxOutputTokens
	}
	return DefaultRemoteContextWindow, DefaultRemoteMaxOutputTokens
}
