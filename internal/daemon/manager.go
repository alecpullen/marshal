package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Runner runs one service-manager command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Manager drives the service on one platform.
type Manager struct {
	GOOS    string // "darwin" or "linux"
	Home    string
	UID     int
	UnitDir string // ~/Library/LaunchAgents or the systemd user directory
	Run     Runner
}

// NewManager is the manager for this machine, with the real runner.
func NewManager(home string) (*Manager, error) {
	return newManager(runtime.GOOS, home)
}

// newManager is NewManager for one platform, so a test can drive both
// without cross-compiling.
func newManager(goos, home string) (*Manager, error) {
	if goos != "darwin" && goos != "linux" {
		return nil, errors.New("the local daemon runs on macOS and Linux only")
	}
	m := &Manager{
		GOOS: goos,
		Home: home,
		UID:  os.Getuid(),
		Run:  execRunner,
	}
	if goos == "darwin" {
		m.UnitDir = filepath.Join(home, "Library", "LaunchAgents")
		return m, nil
	}
	m.UnitDir = systemdUnitDir(home)
	return m, nil
}

// systemdUnitDir is $XDG_CONFIG_HOME/systemd/user, defaulting to
// ~/.config/systemd/user.
func systemdUnitDir(home string) string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user")
	}
	return filepath.Join(home, ".config", "systemd", "user")
}

// execRunner is the manager's only way to reach a service manager.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// unitPath is the file Install writes and Uninstall removes.
func (m *Manager) unitPath() string {
	if m.GOOS == "darwin" {
		return filepath.Join(m.UnitDir, Label+".plist")
	}
	return filepath.Join(m.UnitDir, Unit)
}

// domain is launchctl's gui domain for this user.
func (m *Manager) domain() string {
	return "gui/" + strconv.Itoa(m.UID)
}

// target is the launchctl service target inside the domain.
func (m *Manager) target() string {
	return m.domain() + "/" + Label
}

// Install writes the unit and makes the service run it. Over an existing
// install it rewrites the unit and restarts the service (decision 11).
func (m *Manager) Install(ctx context.Context, u UnitSpec) error {
	if err := os.MkdirAll(u.Paths.Dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", u.Paths.Dir, err)
	}
	if err := os.MkdirAll(m.UnitDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", m.UnitDir, err)
	}
	rendered := SystemdUnit(u)
	if m.GOOS == "darwin" {
		rendered = LaunchdPlist(u)
	}
	path := m.unitPath()
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if m.GOOS == "darwin" {
		if m.loaded(ctx) {
			// An agent that is already loaded keeps the old
			// program until it is unloaded and loaded again.
			if _, err := m.Run(ctx, "launchctl", "bootout", m.target()); err != nil {
				return fmt.Errorf("launchctl bootout: %w", err)
			}
		}
		if _, err := m.Run(ctx, "launchctl", "bootstrap", m.domain(), path); err != nil {
			return fmt.Errorf("launchctl bootstrap: %w", err)
		}
		return nil
	}

	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", Unit},
		{"--user", "restart", Unit},
	} {
		if _, err := m.Run(ctx, "systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

// Start loads the service (decision 11).
func (m *Manager) Start(ctx context.Context) error {
	if m.GOOS == "darwin" {
		if m.loaded(ctx) {
			if _, err := m.Run(ctx, "launchctl", "kickstart", "-k", m.target()); err != nil {
				return fmt.Errorf("launchctl kickstart: %w", err)
			}
			return nil
		}
		if _, err := m.Run(ctx, "launchctl", "bootstrap", m.domain(), m.unitPath()); err != nil {
			return fmt.Errorf("launchctl bootstrap: %w", err)
		}
		return nil
	}
	if _, err := m.Run(ctx, "systemctl", "--user", "start", Unit); err != nil {
		return fmt.Errorf("systemctl start: %w", err)
	}
	return nil
}

// Stop unloads the service (decision 11). A service that is not loaded
// is already stopped.
func (m *Manager) Stop(ctx context.Context) error {
	if m.GOOS == "darwin" {
		out, err := m.Run(ctx, "launchctl", "bootout", m.target())
		return tolerateNotLoaded("launchctl bootout", out, err)
	}
	out, err := m.Run(ctx, "systemctl", "--user", "stop", Unit)
	return tolerateNotLoaded("systemctl stop", out, err)
}

// Uninstall stops, disables and removes the unit, and keeps the data
// directory (decision 11).
func (m *Manager) Uninstall(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	if m.GOOS == "linux" {
		out, err := m.Run(ctx, "systemctl", "--user", "disable", "--now", Unit)
		if err := tolerateNotLoaded("systemctl disable", out, err); err != nil {
			return err
		}
	}
	if err := os.Remove(m.unitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", m.unitPath(), err)
	}
	return nil
}

// ServiceState is what the service manager reports.
type ServiceState struct {
	Installed, Running bool
	PID                int
}

// State reads the service manager's answer for the unit.
func (m *Manager) State(ctx context.Context) (ServiceState, error) {
	if m.GOOS == "darwin" {
		out, err := m.Run(ctx, "launchctl", "print", m.target())
		if err != nil {
			if notLoaded(out, err) {
				return ServiceState{}, nil
			}
			return ServiceState{}, fmt.Errorf("launchctl print: %w", err)
		}
		return parseLaunchdPrint(string(out)), nil
	}

	out, err := m.Run(ctx, "systemctl", "--user", "show", "--property=ActiveState,MainPID", Unit)
	if err != nil {
		if notLoaded(out, err) {
			return ServiceState{}, nil
		}
		return ServiceState{}, fmt.Errorf("systemctl show: %w", err)
	}
	return parseSystemctlShow(string(out)), nil
}

// loaded reports whether launchd holds the agent, which is what decides
// between bootstrap and kickstart.
func (m *Manager) loaded(ctx context.Context) bool {
	out, err := m.Run(ctx, "launchctl", "print", m.target())
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// parseLaunchdPrint reads the lines launchctl print puts the state and
// the pid on.
func parseLaunchdPrint(out string) ServiceState {
	state := ServiceState{Installed: true}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "state":
			state.Running = value == "running"
		case "pid":
			if pid, err := strconv.Atoi(value); err == nil {
				state.PID = pid
				state.Running = true
			}
		}
	}
	return state
}

// parseSystemctlShow reads ActiveState= and MainPID=.
func parseSystemctlShow(out string) ServiceState {
	state := ServiceState{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			state.Installed = true
			state.Running = value == "active"
		case "MainPID":
			if pid, err := strconv.Atoi(value); err == nil && pid > 0 {
				state.PID = pid
				state.Running = true
			}
		}
	}
	return state
}

// tolerateNotLoaded turns a "not loaded" or "not found" refusal into
// success: the wanted end state already holds.
func tolerateNotLoaded(what string, out []byte, err error) error {
	if err == nil {
		return nil
	}
	if notLoaded(out, err) {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

// notLoaded recognises the answers the two service managers give for a
// unit they do not hold.
func notLoaded(out []byte, err error) bool {
	text := strings.ToLower(string(out))
	if err != nil {
		text += " " + strings.ToLower(err.Error())
	}
	for _, phrase := range []string{
		"not loaded",
		"not found",
		"could not find",
		"no such process",
		"not-found",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
