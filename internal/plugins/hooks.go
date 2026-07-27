package plugins

import (
	"fmt"
	"os"
	"path/filepath"

	"marshal/internal/app/config"
	"marshal/internal/hooks"

	"github.com/pelletier/go-toml/v2"
)

// HooksFileName mirrors the [hooks] section of config.toml, so entries
// can be copied between a plugin and user config verbatim.
const HooksFileName = "hooks.toml"

type hooksFile struct {
	Hooks struct {
		Entries []config.HookConfig `toml:"entries"`
	} `toml:"hooks"`
}

// parseHooksFile reads dir/hooks.toml into config-ready hook entries.
// Entries with an unknown event or an empty command are skipped; a
// missing file yields nil, nil; a malformed file is an error (the caller
// skips the whole plugin). Entries are appended to cfg.Hooks.Entries at
// load time and run through the existing hooks runner — including its
// trust gate and secret-scrubbed environment.
func parseHooksFile(dir string) ([]config.HookConfig, error) {
	raw, err := os.ReadFile(filepath.Join(dir, HooksFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", HooksFileName, err)
	}
	var f hooksFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", HooksFileName, err)
	}
	var entries []config.HookConfig
	for _, e := range f.Hooks.Entries {
		if e.Command == "" {
			continue
		}
		if e.Event != hooks.EventPreToolUse && e.Event != hooks.EventTurnEnd {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}
