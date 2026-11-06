package config

import (
	"fmt"
	"net/url"
	"sort"

	"marshal/internal/strutil"
)

// MaskSecrets returns a copy of cfg with all secret-bearing literal values
// (api_key, search_key, and any credentials embedded in Desktop.CDPURL)
// replaced by "***". Env-var-name fields (api_key_env) and all other fields
// are preserved. The input is not mutated.
//
// This is the single source of truth for secret masking at the config layer;
// the settings TUI's isSecretPath mirrors the same field set.
func MaskSecrets(cfg Config) Config {
	out := cfg
	if out.Providers != nil {
		out.Providers = make(map[string]ProviderConfig, len(cfg.Providers))
		for name, p := range cfg.Providers {
			if p.APIKey != "" {
				p.APIKey = "***"
			}
			out.Providers[name] = p
		}
	}
	if cfg.Web.SearchKey != "" {
		out.Web.SearchKey = "***"
	}
	if cfg.Desktop.CDPURL != "" {
		out.Desktop.CDPURL = maskURLUserinfo(cfg.Desktop.CDPURL)
	}
	return out
}

// RedactSecrets returns cfg with every literal secret of MaskSecrets' set
// replaced by its mask (P2 C3, plan 7d): each provider's API key and the web
// search key by strutil.MaskKey, and the desktop CDP URL's userinfo as
// MaskSecrets masks it. An MCP server's env and headers are outside that set
// and stay in the clear (a named follow-up in the 7d record). An empty secret
// stays empty. cfg and its maps are not changed.
func RedactSecrets(cfg Config) Config {
	out := cfg
	if cfg.Providers != nil {
		out.Providers = make(map[string]ProviderConfig, len(cfg.Providers))
		for name, p := range cfg.Providers {
			if p.APIKey != "" {
				p.APIKey = strutil.MaskKey(p.APIKey)
			}
			out.Providers[name] = p
		}
	}
	if cfg.Web.SearchKey != "" {
		out.Web.SearchKey = strutil.MaskKey(cfg.Web.SearchKey)
	}
	if cfg.Desktop.CDPURL != "" {
		out.Desktop.CDPURL = maskURLUserinfo(cfg.Desktop.CDPURL)
	}
	return out
}

// RedactLayers is RedactSecrets over the layers' Default, User and Merged
// configs. Every other field is kept.
func RedactLayers(l Layers) Layers {
	out := l
	out.Default = RedactSecrets(l.Default)
	out.User = RedactSecrets(l.User)
	out.Merged = RedactSecrets(l.Merged)
	return out
}

// RestoreSecrets returns client with every masked secret replaced by the
// stored secret it stands for, searching stored in order. renamed maps a
// provider's name in client to its name in stored. A literal or empty secret
// is kept. It fails, naming the secret's path, when a mask matches nothing.
// client and its maps are not changed.
func RestoreSecrets(client Config, renamed map[string]string, stored ...Config) (Config, error) {
	out := client
	if client.Providers != nil {
		out.Providers = make(map[string]ProviderConfig, len(client.Providers))
	}
	// Sorted so a config with several unmatched masks always reports the same
	// path.
	names := make([]string, 0, len(client.Providers))
	for name := range client.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		restored, ok := RestoreProviderKey(name, client.Providers[name], renamed, stored...)
		if !ok {
			return client, MaskError("providers." + name + ".api_key")
		}
		out.Providers[name] = restored
	}
	if strutil.IsMasked(client.Web.SearchKey) {
		key, ok := restoreSearchKey(client.Web.SearchKey, stored)
		if !ok {
			return client, MaskError("web.search_key")
		}
		out.Web.SearchKey = key
	}
	if cdpURLMasked(client.Desktop.CDPURL) {
		raw, ok := restoreCDPURL(client.Desktop.CDPURL, stored)
		if !ok {
			return client, MaskError("desktop.cdp_url")
		}
		out.Desktop.CDPURL = raw
	}
	return out, nil
}

// RestoreProviderKey restores one provider's masked key as RestoreSecrets
// does. It reports false when the key is masked and matches nothing.
func RestoreProviderKey(name string, pc ProviderConfig, renamed map[string]string, stored ...Config) (ProviderConfig, bool) {
	if !strutil.IsMasked(pc.APIKey) {
		return pc, true
	}
	mask := pc.APIKey
	// match reports the key a stored config holds under the given provider
	// name when masking it gives this mask.
	// A stored value that is itself a mask is skipped everywhere below:
	// masking it is a no-op, so it would match circularly and put a mask back
	// on the wire as though it were the secret.
	match := func(cfg Config, provider string) (string, bool) {
		p, ok := cfg.Providers[provider]
		if !ok || p.APIKey == "" || strutil.IsMasked(p.APIKey) || strutil.MaskKey(p.APIKey) != mask {
			return "", false
		}
		return p.APIKey, true
	}
	for _, cfg := range stored {
		if key, ok := match(cfg, name); ok {
			pc.APIKey = key
			return pc, true
		}
	}
	if old := renamed[name]; old != "" {
		for _, cfg := range stored {
			if key, ok := match(cfg, old); ok {
				pc.APIKey = key
				return pc, true
			}
		}
	}
	// Last resort: the mask's suffix identifies one stored key only.
	seen := make(map[string]bool)
	var keys []string
	for _, cfg := range stored {
		for _, p := range cfg.Providers {
			if p.APIKey == "" || strutil.IsMasked(p.APIKey) || seen[p.APIKey] || strutil.MaskKey(p.APIKey) != mask {
				continue
			}
			seen[p.APIKey] = true
			keys = append(keys, p.APIKey)
		}
	}
	if len(keys) != 1 {
		return pc, false
	}
	pc.APIKey = keys[0]
	return pc, true
}

// restoreSearchKey returns the first stored search key that masks to masked.
// A stored key that is itself a mask is skipped, as in RestoreProviderKey.
func restoreSearchKey(masked string, stored []Config) (string, bool) {
	for _, cfg := range stored {
		if cfg.Web.SearchKey == "" || strutil.IsMasked(cfg.Web.SearchKey) {
			continue
		}
		if strutil.MaskKey(cfg.Web.SearchKey) == masked {
			return cfg.Web.SearchKey, true
		}
	}
	return "", false
}

// restoreCDPURL returns the first stored CDP URL whose masked form is masked.
// A stored URL that already carries the mask is skipped, as in
// RestoreProviderKey.
func restoreCDPURL(masked string, stored []Config) (string, bool) {
	for _, cfg := range stored {
		if cfg.Desktop.CDPURL == "" || cdpURLMasked(cfg.Desktop.CDPURL) {
			continue
		}
		if maskURLUserinfo(cfg.Desktop.CDPURL) == masked {
			return cfg.Desktop.CDPURL, true
		}
	}
	return "", false
}

// cdpURLMasked reports whether raw carries the CDP URL mask MaskSecrets
// writes: the userinfo is the username "***" with no password. A deliberate
// ws://***@host URL is read as a mask; restoring it then either resolves it to
// a stored URL or fails with the pinned error, so nothing is lost silently.
func cdpURLMasked(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return false
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return false
	}
	return u.User.Username() == "***"
}

// MaskError is the failure a mask with nothing behind it reports at the given
// TOML path: config: <path>: no stored secret matches its mask.
func MaskError(path string) error {
	return fmt.Errorf("config: %s: no stored secret matches its mask", path)
}

// maskURLUserinfo strips credentials from a URL while preserving the host
// and path so the value remains useful for debugging.
func maskURLUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("***")
	return u.String()
}
