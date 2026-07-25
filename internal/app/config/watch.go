package config

// WatchEnabled applies the watcher enablement rule: an explicit watch value
// wins; otherwise the watcher is on iff an embedding role is configured.
func WatchEnabled(watch *bool, embeddingConfigured bool) bool {
	if watch != nil {
		return *watch
	}
	return embeddingConfigured
}
