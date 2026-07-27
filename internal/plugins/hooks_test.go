package plugins

import (
	"path/filepath"
	"testing"
)

const testHooksTOML = `[[hooks.entries]]
event = "pre_tool_use"
matcher = "shell.run"
command = "./scripts/lint.sh"
timeout_ms = 5000

[[hooks.entries]]
event = "turn_end"
command = "./scripts/notify.sh"
`

func TestParseHooksFileValid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "hooks.toml"), testHooksTOML)

	entries, err := parseHooksFile(dir)
	if err != nil {
		t.Fatalf("parseHooksFile: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries length = %d, want 2", len(entries))
	}
	if entries[0].Event != "pre_tool_use" || entries[0].Matcher != "shell.run" || entries[0].Command != "./scripts/lint.sh" || entries[0].TimeoutMS != 5000 {
		t.Fatalf("entries[0] = %+v", entries[0])
	}
	if entries[1].Event != "turn_end" || entries[1].Command != "./scripts/notify.sh" {
		t.Fatalf("entries[1] = %+v", entries[1])
	}
}

func TestParseHooksFileMissing(t *testing.T) {
	entries, err := parseHooksFile(t.TempDir())
	if err != nil {
		t.Fatalf("missing hooks.toml should not error: %v", err)
	}
	if entries != nil {
		t.Fatalf("entries = %v, want nil", entries)
	}
}

func TestParseHooksFileSkipsInvalidEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "hooks.toml"), `[[hooks.entries]]
event = "pre_tool_use"
command = "./ok.sh"

[[hooks.entries]]
event = "on_startup"
command = "./bad-event.sh"

[[hooks.entries]]
event = "turn_end"
`)

	entries, err := parseHooksFile(dir)
	if err != nil {
		t.Fatalf("parseHooksFile: %v", err)
	}
	if len(entries) != 1 || entries[0].Command != "./ok.sh" {
		t.Fatalf("entries = %+v, want only ./ok.sh", entries)
	}
}

func TestParseHooksFileMalformed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "hooks.toml"), "{{{ not toml")
	if _, err := parseHooksFile(dir); err == nil {
		t.Fatal("malformed hooks.toml should error")
	}
}
