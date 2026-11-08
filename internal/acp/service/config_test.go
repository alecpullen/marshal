package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// newConfigState builds a session state over dir, carrying cfg.
func newConfigState(t *testing.T, id string, cfg config.Config, dir string) *session.State {
	t.Helper()
	return session.New(cfg, dir, time.Unix(100, 0), session.Persistence{SessionID: id})
}

// TestStatusNamesTheConfigFiles pins the two status fields: the user config is
// found under the home, or is the one the caller injected, and the project
// config is the working directory's.
func TestStatusNamesTheConfigFiles(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)

	_, sess := New(Deps{State: state, HomeDir: home})
	status := sess.Status()
	if want := config.UserConfigPath(home); status.UserConfigPath != want {
		t.Errorf("UserConfigPath = %q, want %q", status.UserConfigPath, want)
	}
	if want := filepath.Join(dir, ".marshal", "config.toml"); status.ProjectConfigPath != want {
		t.Errorf("ProjectConfigPath = %q, want %q", status.ProjectConfigPath, want)
	}

	explicit := filepath.Join(t.TempDir(), "explicit.toml")
	_, injected := New(Deps{State: state, HomeDir: home, UserConfigPath: explicit})
	if got := injected.Status().UserConfigPath; got != explicit {
		t.Errorf("UserConfigPath = %q, want the injected %q", got, explicit)
	}
}

// TestClosedSessionConfigCallsFail pins C4 for the calls this plan adds: a
// session a swap replaced refuses them all, and keeps answering its reads with
// its final values.
func TestClosedSessionConfigCallsFail(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Project.Name = "read-me"
	state := newConfigState(t, "s1", cfg, t.TempDir())
	layers := config.Default()
	layers.Project.Name = "merged"
	state.SetLayers(config.Layers{Default: config.Default(), Merged: layers})
	h, old := New(Deps{State: state})
	h.swap(newConfigState(t, "s2", config.Default(), t.TempDir()))

	calls := map[string]error{
		"ApplyConfig":        old.ApplyConfig(ctx, config.Default()),
		"SaveProjectConfig":  old.SaveProjectConfig(ctx, config.Default()),
		"SaveVerifyCommands": old.SaveVerifyCommands(ctx, "go build", "go test"),
	}
	_, reloadErr := old.ReloadConfig(ctx, config.Default())
	calls["ReloadConfig"] = reloadErr
	_, reconnectErr := old.ReconnectMCP(ctx)
	calls["ReconnectMCP"] = reconnectErr

	for name, err := range calls {
		if !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
			t.Errorf("%s on a closed session = %v, want %q", name, err, acpwire.CodeSessionClosed)
		}
	}

	// The two reads keep answering with the values the session ended on.
	if got := old.Config().Project.Name; got != "read-me" {
		t.Errorf("closed session Config().Project.Name = %q, want its final value %q", got, "read-me")
	}
	if got := old.Layers().Merged.Project.Name; got != "merged" {
		t.Errorf("closed session Layers().Merged.Project.Name = %q, want its final value %q", got, "merged")
	}
}
