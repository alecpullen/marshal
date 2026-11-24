package config

import (
	"slices"
	"testing"
)

// TestMergeIndexingIgnoreIsAdditive pins the additive semantics of
// indexing.ignore: a user who names an ignore entry of their own keeps every
// shipped prune default instead of replacing the list.
func TestMergeIndexingIgnoreIsAdditive(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", "[indexing]\nignore = [\"my-cache/**\"]\n")

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	defaults := Default().Indexing.Ignore
	for _, want := range defaults {
		if !slices.Contains(cfg.Indexing.Ignore, want) {
			t.Errorf("Ignore lost the default %q: %q", want, cfg.Indexing.Ignore)
		}
	}
	if !slices.Contains(cfg.Indexing.Ignore, "my-cache/**") {
		t.Errorf("Ignore dropped the configured entry: %q", cfg.Indexing.Ignore)
	}
	// The defaults come first, in their shipped order, then the configured
	// entries: one stable order for every scan.
	assertPrefix(t, "Ignore", cfg.Indexing.Ignore, defaults)
	if got, want := len(cfg.Indexing.Ignore), len(defaults)+1; got != want {
		t.Errorf("len(Ignore) = %d, want %d: %q", got, want, cfg.Indexing.Ignore)
	}
	assertNoDuplicateStrings(t, "Ignore", cfg.Indexing.Ignore)
}

// TestMergeIndexingIgnoreUnionsProjectOverGlobal covers the second layer: the
// project list extends the user-merged list rather than replacing it, and an
// entry named twice appears once.
func TestMergeIndexingIgnoreUnionsProjectOverGlobal(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", "[indexing]\nignore = [\"from-user/**\"]\n")
	writeFile(t, work+"/.marshal/config.toml", "[indexing]\nignore = [\"from-user/**\", \"from-project/**\"]\n")

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	defaults := Default().Indexing.Ignore
	for _, want := range append(slices.Clone(defaults), "from-user/**", "from-project/**") {
		if !slices.Contains(cfg.Indexing.Ignore, want) {
			t.Errorf("Ignore lost %q: %q", want, cfg.Indexing.Ignore)
		}
	}
	assertPrefix(t, "Ignore", cfg.Indexing.Ignore, defaults)
	// Defaults, then the user entry, then the project's own entry: the
	// repeated "from-user/**" is not listed twice.
	want := append(slices.Clone(defaults), "from-user/**", "from-project/**")
	if !slices.Equal(cfg.Indexing.Ignore, want) {
		t.Errorf("Ignore = %q, want %q", cfg.Indexing.Ignore, want)
	}
	assertNoDuplicateStrings(t, "Ignore", cfg.Indexing.Ignore)
}

// assertPrefix checks that prefix is list's leading run, in order, without
// panicking on a shorter list.
func assertPrefix(t *testing.T, name string, list, prefix []string) {
	t.Helper()
	if len(list) < len(prefix) {
		t.Fatalf("%s = %q, want at least the %d leading entries %q", name, list, len(prefix), prefix)
	}
	if got := list[:len(prefix)]; !slices.Equal(got, prefix) {
		t.Errorf("%s leading entries = %q, want %q", name, got, prefix)
	}
}

func assertNoDuplicateStrings(t *testing.T, name string, list []string) {
	t.Helper()
	seen := make(map[string]bool, len(list))
	for _, v := range list {
		if seen[v] {
			t.Errorf("%s lists %q more than once: %q", name, v, list)
		}
		seen[v] = true
	}
}
