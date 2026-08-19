package config

import "net/url"

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
