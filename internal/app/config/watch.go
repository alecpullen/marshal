package config

// WatchEnabled applies the watcher enablement rule: an explicit watch value
// wins; otherwise the watcher defaults to off so indexing never auto-starts
// in the background just because an embedding preset is configured.
func WatchEnabled(watch *bool, embeddingConfigured bool) bool {
	if watch != nil {
		return *watch
	}
	return false
}
