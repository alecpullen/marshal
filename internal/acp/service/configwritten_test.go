package service

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
)

// writeFixture is one session's host and service over temporary config files,
// recording every path ConfigWritten is told.
type writeFixture struct {
	home  string
	dir   string
	state *session.State
	host  *Host
	sess  *Session

	mu    sync.Mutex
	paths []string
}

// newWriteFixture builds a fixture whose user and project config files are
// under temporary directories.
func newWriteFixture(t *testing.T) *writeFixture {
	t.Helper()
	return newWriteFixtureAt(t, t.TempDir(), t.TempDir(), "")
}

// newWriteFixtureAt builds a fixture over home and dir, with userConfig as the
// user config file when it is not empty.
func newWriteFixtureAt(t *testing.T, home, dir, userConfig string) *writeFixture {
	t.Helper()
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{SessionID: "s1"})
	f := &writeFixture{home: home, dir: dir, state: state}
	f.host, f.sess = New(Deps{
		State:          state,
		HomeDir:        home,
		UserConfigPath: userConfig,
		ConfigWritten: func(path string) {
			f.mu.Lock()
			f.paths = append(f.paths, path)
			f.mu.Unlock()
		},
	})
	return f
}

// newBrokenWriteFixture builds a fixture whose config writes cannot succeed:
// the user config's directory and the project's .marshal are regular files.
func newBrokenWriteFixture(t *testing.T) *writeFixture {
	t.Helper()
	home := t.TempDir()
	dir := t.TempDir()
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write the blocker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write the project blocker: %v", err)
	}
	return newWriteFixtureAt(t, home, dir, filepath.Join(blocker, "config.toml"))
}

// reported is every path ConfigWritten was told.
func (f *writeFixture) reported() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// userConfigPathOf is the user config file the fixture's writes reach.
func (f *writeFixture) userConfigPathOf(t *testing.T) string {
	t.Helper()
	path, err := userConfigPath(Deps{HomeDir: f.home, UserConfigPath: f.sess.userConfigFile})
	if err != nil {
		t.Fatalf("user config path: %v", err)
	}
	return path
}

// projectConfigPathOf is the project config file the fixture's writes reach.
func (f *writeFixture) projectConfigPathOf(t *testing.T) string {
	t.Helper()
	path := config.ProjectConfigPath(f.dir)
	if path == "" {
		t.Fatal("the fixture has no project config path")
	}
	return path
}

// TestEveryConfigWriteReportsItself pins decision 3 for the eight writes this
// package makes: each tells ConfigWritten the path it wrote, once the write
// succeeded, and a write that fails tells nothing.
func TestEveryConfigWriteReportsItself(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		// write makes the call and answers the path it wrote.
		write func(t *testing.T, f *writeFixture) string
		// fail makes the same call against a fixture whose write cannot
		// succeed. It checks the failure itself; the caller checks that
		// nothing was reported.
		fail func(t *testing.T, f *writeFixture)
	}{
		{
			name: "Host.SaveUserValue",
			write: func(t *testing.T, f *writeFixture) string {
				if err := f.host.SaveUserValue(ctx, "project.name", "value"); err != nil {
					t.Fatalf("SaveUserValue: %v", err)
				}
				return f.userConfigPathOf(t)
			},
			fail: func(t *testing.T, f *writeFixture) {
				if err := f.host.SaveUserValue(ctx, "project.name", "value"); err == nil {
					t.Fatal("SaveUserValue on an unwritable config reported no error")
				}
			},
		},
		{
			name: "Host.SaveUserProviders",
			write: func(t *testing.T, f *writeFixture) string {
				providers := map[string]config.ProviderConfig{"local": {Type: "openai", BaseURL: "http://127.0.0.1:1/v1", ToolCalling: true}}
				if err := f.host.SaveUserProviders(ctx, providers, nil, nil); err != nil {
					t.Fatalf("SaveUserProviders: %v", err)
				}
				return f.userConfigPathOf(t)
			},
			fail: func(t *testing.T, f *writeFixture) {
				providers := map[string]config.ProviderConfig{"local": {Type: "openai"}}
				if err := f.host.SaveUserProviders(ctx, providers, nil, nil); err == nil {
					t.Fatal("SaveUserProviders on an unwritable config reported no error")
				}
			},
		},
		{
			name: "Host.SaveUserPresets",
			write: func(t *testing.T, f *writeFixture) string {
				presets := map[string]routing.ModelPreset{"local": {Provider: "local", Model: "model"}}
				if err := f.host.SaveUserPresets(ctx, presets, nil); err != nil {
					t.Fatalf("SaveUserPresets: %v", err)
				}
				return f.userConfigPathOf(t)
			},
			fail: func(t *testing.T, f *writeFixture) {
				presets := map[string]routing.ModelPreset{"local": {Provider: "local", Model: "model"}}
				if err := f.host.SaveUserPresets(ctx, presets, nil); err == nil {
					t.Fatal("SaveUserPresets on an unwritable config reported no error")
				}
			},
		},
		{
			name: "Host.SaveProviderKey",
			write: func(t *testing.T, f *writeFixture) string {
				if err := f.host.SaveProviderKey(ctx, "local", "sk-test"); err != nil {
					t.Fatalf("SaveProviderKey: %v", err)
				}
				return f.userConfigPathOf(t)
			},
			fail: func(t *testing.T, f *writeFixture) {
				if err := f.host.SaveProviderKey(ctx, "local", "sk-test"); err == nil {
					t.Fatal("SaveProviderKey on an unwritable config reported no error")
				}
			},
		},
		{
			name: "Session.SaveProjectConfig",
			write: func(t *testing.T, f *writeFixture) string {
				cfg := config.Default()
				cfg.Project.Name = "recorded"
				if err := f.sess.SaveProjectConfig(ctx, cfg); err != nil {
					t.Fatalf("SaveProjectConfig: %v", err)
				}
				return f.projectConfigPathOf(t)
			},
			fail: func(t *testing.T, f *writeFixture) {
				if err := f.sess.SaveProjectConfig(ctx, config.Default()); err == nil {
					t.Fatal("SaveProjectConfig on an unwritable project reported no error")
				}
			},
		},
		{
			name: "Session.SaveVerifyCommands",
			write: func(t *testing.T, f *writeFixture) string {
				if err := f.sess.SaveVerifyCommands(ctx, "go build ./...", "go test ./..."); err != nil {
					t.Fatalf("SaveVerifyCommands: %v", err)
				}
				return f.projectConfigPathOf(t)
			},
			fail: func(t *testing.T, f *writeFixture) {
				if err := f.sess.SaveVerifyCommands(ctx, "go build ./...", "go test ./..."); err == nil {
					t.Fatal("SaveVerifyCommands on an unwritable project reported no error")
				}
			},
		},
		{
			name: "Session.SetSkillAutoload",
			write: func(t *testing.T, f *writeFixture) string {
				if err := f.sess.SetSkillAutoload(ctx, "marshal-writing-plans", true); err != nil {
					t.Fatalf("SetSkillAutoload: %v", err)
				}
				return f.userConfigPathOf(t)
			},
			fail: func(t *testing.T, f *writeFixture) {
				if err := f.sess.SetSkillAutoload(ctx, "marshal-writing-plans", true); err == nil {
					t.Fatal("SetSkillAutoload on an unwritable config reported no error")
				}
			},
		},
		{
			name: "Session.alwaysRule",
			write: func(t *testing.T, f *writeFixture) string {
				dir := userConfigDir()
				if dir == "" {
					t.Fatal("the test has no user config directory")
				}
				apply := f.sess.alwaysRule(&session.PendingToolCall{Name: "shell.run", Command: "go test ./..."})
				if err := apply(); err != nil {
					t.Fatalf("alwaysRule: %v", err)
				}
				return filepath.Join(dir, "config.toml")
			},
			fail: func(t *testing.T, f *writeFixture) {
				// The always rule writes under the home directory, whatever
				// Deps says, so its failure is the home's.
				blocker := filepath.Join(t.TempDir(), "home")
				if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("write the home blocker: %v", err)
				}
				t.Setenv("HOME", blocker)
				apply := f.sess.alwaysRule(&session.PendingToolCall{Name: "shell.run", Command: "go test ./..."})
				// A failed save keeps the rule on the session, so the call
				// itself still succeeds.
				if err := apply(); err != nil {
					t.Fatalf("alwaysRule fell back with an error: %v", err)
				}
				if _, err := os.Stat(filepath.Join(userConfigDir(), "config.toml")); err == nil {
					t.Fatal("the always rule wrote its config after the save was supposed to fail")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWriteFixture(t)
			want := tc.write(t, f)
			if got := f.reported(); len(got) != 1 || got[0] != want {
				t.Fatalf("%s told ConfigWritten %v, want exactly [%s]", tc.name, got, want)
			}

			t.Run("a write that fails tells nothing", func(t *testing.T) {
				broken := newBrokenWriteFixture(t)
				tc.fail(t, broken)
				if got := broken.reported(); len(got) != 0 {
					t.Fatalf("%s told ConfigWritten %v for a write that failed", tc.name, got)
				}
			})
		})
	}
}
