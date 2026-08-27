// Package logging contains repo-wide hygiene guards that keep the TTY clean.
//
// Marshal renders the Bubble Tea UI directly to the terminal (no alt-screen),
// so any stray write to stdout/stderr garbles the frame. These tests enforce:
//
//  1. No fmt.Print*, println(), print(), log.Print*/log.Fatal* in
//     non-test .go files anywhere in the module — CLI subcommands must write
//     via the io.Writer passed down from main, not the process streams.
//  2. os.Stdout is referenced only in an explicit allowlist of files (the
//     entrypoint wiring and the deferred trust prompt).
//  3. slog.SetDefault appears only inside internal/app (production) or
//     _test.go files; other packages must take a *slog.Logger parameter so
//     callers can route output.
package logging

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stdoutAllowlist files may reference os.Stdout: cmd/marshal/main.go wires it
// into run() as an injected stream, and internal/trust/prompt.go only prints
// to it when running as a real terminal outside the TUI lifecycle.
var stdoutAllowlist = map[string]bool{
	"cmd/marshal/main.go":                           true,
	"cmd/marshal/calibrate.go":                      true,
	"cmd/marshal/history.go":                        true,
	"cmd/marshal/plugin.go":                         true,
	"cmd/webbridge/main.go":                         true,
	filepath.Join("internal", "trust", "prompt.go"): true,
}

// setDefaultAllowlist files may call slog.SetDefault outside _test.go.
// internal/app installs the file-backed logger at startup.
var setDefaultAllowlist = map[string]bool{
	filepath.Join("internal", "app"): true,
}

var (
	directPrintRe = regexp.MustCompile(`\bfmt\.Print(?:f|ln)?\s*\(|\bfmt\.Print\s*\(|\bprintln\s*\(|\bprint\s*\(|\blog\.Print|\blog\.Fatal`)
	osStdoutRe    = regexp.MustCompile(`\bos\.Stdout\b`)
	setDefaultRe  = regexp.MustCompile(`\bslog\.SetDefault\b`)
)

func TestNoDirectWritesToProcessStreams(t *testing.T) {
	// This test lives at <module>/internal/app/logging; walk the whole module.
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	moduleRoot := filepath.Dir(filepath.Dir(filepath.Dir(pkgDir)))

	err = filepath.WalkDir(moduleRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			switch name {
			case ".git", ".docs-archive", "node_modules", ".marshal":
				return filepath.SkipAll
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(moduleRoot, path)
		if rerr != nil {
			return rerr
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}

		for _, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if directPrintRe.MatchString(trimmed) {
				t.Errorf("%s: direct process-stream print: %s", rel, trimmed)
			}
			if osStdoutRe.MatchString(trimmed) && !stdoutAllowlist[rel] {
				t.Errorf("%s: references os.Stdout outside the allowlist: %s", rel, trimmed)
			}
			if setDefaultRe.MatchString(trimmed) {
				inApp := false
				for allowed := range setDefaultAllowlist {
					if strings.HasPrefix(rel, allowed+string(filepath.Separator)) || filepath.Dir(rel) == allowed {
						inApp = true
						break
					}
				}
				if !inApp {
					t.Errorf("%s: slog.SetDefault outside internal/app: %s", rel, trimmed)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
