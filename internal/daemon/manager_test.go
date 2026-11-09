package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scripted is one command's answer.
type scripted struct {
	out string
	err error
}

// fakeRunner records every command line and answers the ones a row
// scripts; an unscripted command succeeds with no output.
type fakeRunner struct {
	calls   []string
	answers map[string]scripted
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	answer := f.answers[line]
	if answer.out != "" {
		return []byte(answer.out), answer.err
	}
	return nil, answer.err
}

// managerRow is one platform/command pair.
type managerRow struct {
	name    string
	goos    string
	answers map[string]scripted
	act     func(context.Context, *Manager, UnitSpec) (ServiceState, error)

	want     []string // command lines, in order; {unit} is the unit path
	wantUnit bool     // the unit file exists afterwards
	goneUnit bool     // the unit file is gone afterwards
	wantDaem bool     // the daemon directory exists afterwards
}

func TestTheManagerRunsTheRightCommands(t *testing.T) {
	const uid = 4242

	for _, tc := range []managerRow{
		{
			name: "darwin install",
			goos: "darwin",
			answers: map[string]scripted{
				"launchctl print gui/4242/dev.marshal.daemon": {
					out: `Could not find service "dev.marshal.daemon" in domain for user gui: 4242`,
					err: errors.New("exit status 113"),
				},
			},
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Install(ctx, u)
			},
			want: []string{
				"launchctl print gui/4242/dev.marshal.daemon",
				"launchctl bootstrap gui/4242 {unit}",
			},
			wantUnit: true,
			wantDaem: true,
		},
		{
			name: "darwin install over a loaded agent",
			goos: "darwin",
			answers: map[string]scripted{
				"launchctl print gui/4242/dev.marshal.daemon": {out: "gui/4242/dev.marshal.daemon = {\n\tstate = running\n}\n"},
			},
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Install(ctx, u)
			},
			want: []string{
				"launchctl print gui/4242/dev.marshal.daemon",
				"launchctl bootout gui/4242/dev.marshal.daemon",
				"launchctl bootstrap gui/4242 {unit}",
			},
			wantUnit: true,
			wantDaem: true,
		},
		{
			name: "darwin start loaded",
			goos: "darwin",
			answers: map[string]scripted{
				"launchctl print gui/4242/dev.marshal.daemon": {out: "gui/4242/dev.marshal.daemon = {\n\tstate = running\n}\n"},
			},
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Start(ctx)
			},
			want: []string{
				"launchctl print gui/4242/dev.marshal.daemon",
				"launchctl kickstart -k gui/4242/dev.marshal.daemon",
			},
		},
		{
			name: "darwin start not loaded",
			goos: "darwin",
			answers: map[string]scripted{
				"launchctl print gui/4242/dev.marshal.daemon": {
					out: "Could not find service in domain for user gui: 4242",
					err: errors.New("exit status 113"),
				},
			},
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Start(ctx)
			},
			want: []string{
				"launchctl print gui/4242/dev.marshal.daemon",
				"launchctl bootstrap gui/4242 {unit}",
			},
		},
		{
			name: "darwin stop",
			goos: "darwin",
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Stop(ctx)
			},
			want: []string{"launchctl bootout gui/4242/dev.marshal.daemon"},
		},
		{
			name: "darwin uninstall",
			goos: "darwin",
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Uninstall(ctx)
			},
			want:     []string{"launchctl bootout gui/4242/dev.marshal.daemon"},
			goneUnit: true,
			wantDaem: true,
		},
		{
			name: "linux install",
			goos: "linux",
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Install(ctx, u)
			},
			want: []string{
				"systemctl --user daemon-reload",
				"systemctl --user enable --now " + Unit,
				"systemctl --user restart " + Unit,
			},
			wantUnit: true,
			wantDaem: true,
		},
		{
			name: "linux start",
			goos: "linux",
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Start(ctx)
			},
			want: []string{"systemctl --user start " + Unit},
		},
		{
			name: "linux stop",
			goos: "linux",
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Stop(ctx)
			},
			want: []string{"systemctl --user stop " + Unit},
		},
		{
			name: "linux uninstall",
			goos: "linux",
			act: func(ctx context.Context, m *Manager, u UnitSpec) (ServiceState, error) {
				return ServiceState{}, m.Uninstall(ctx)
			},
			want: []string{
				"systemctl --user stop " + Unit,
				"systemctl --user disable --now " + Unit,
			},
			goneUnit: true,
			wantDaem: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			m, err := newManager(tc.goos, home)
			if err != nil {
				t.Fatalf("newManager(%q): %v", tc.goos, err)
			}
			m.UID = uid
			runner := &fakeRunner{answers: tc.answers}
			m.Run = runner.run

			unitPath := m.UnitPath()
			if tc.goos == "darwin" {
				wantPlist := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
				if unitPath != wantPlist {
					t.Errorf("UnitDir gives %q, want %q", unitPath, wantPlist)
				}
			} else {
				wantUnit := filepath.Join(home, ".config", "systemd", "user", Unit)
				if unitPath != wantUnit {
					t.Errorf("UnitDir gives %q, want %q", unitPath, wantUnit)
				}
			}

			// Uninstall has to have something to remove.
			if tc.goneUnit {
				if err := os.MkdirAll(m.UnitDir, 0o755); err != nil {
					t.Fatalf("make unit dir: %v", err)
				}
				if err := os.WriteFile(unitPath, []byte("previous unit\n"), 0o644); err != nil {
					t.Fatalf("write previous unit: %v", err)
				}
			}
			// An existing install's data directory has to survive
			// an uninstall (decision 11).
			if tc.wantDaem {
				if err := os.MkdirAll(PathsFor(home).Dir, 0o700); err != nil {
					t.Fatalf("make daemon dir: %v", err)
				}
			}

			u := UnitSpec{
				Webbridge: "/opt/marshal/webbridge",
				Marshal:   "/opt/marshal/marshal",
				Paths:     PathsFor(home),
				Env:       map[string]string{"PATH": "/usr/bin:/bin"},
			}
			if _, err := tc.act(context.Background(), m, u); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			want := make([]string, len(tc.want))
			for i, line := range tc.want {
				want[i] = strings.ReplaceAll(line, "{unit}", unitPath)
			}
			if got := runner.calls; !equalLines(got, want) {
				t.Errorf("commands = %v, want %v", got, want)
			}

			if tc.wantUnit {
				info, err := os.Stat(unitPath)
				if err != nil {
					t.Fatalf("the unit was not written: %v", err)
				}
				if got := info.Mode().Perm(); got != 0o644 {
					t.Errorf("unit mode = %v, want 0644", got)
				}
				if tc.goos == "darwin" {
					if !strings.Contains(readFile(t, unitPath), "<key>ProgramArguments</key>") {
						t.Errorf("the plist does not name the arguments")
					}
				} else if !strings.Contains(readFile(t, unitPath), "ExecStart=") {
					t.Errorf("the unit does not name the command")
				}
			}
			if tc.goneUnit {
				if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
					t.Errorf("the unit is still at %s (stat error %v)", unitPath, err)
				}
			}
			if tc.wantDaem {
				info, err := os.Stat(u.Paths.Dir)
				if err != nil {
					t.Fatalf("the daemon directory was not created: %v", err)
				}
				if got := info.Mode().Perm(); got != 0o700 {
					t.Errorf("daemon directory mode = %v, want 0700", got)
				}
			}
		})
	}
}

func TestStopAndUninstallTolerateANotLoadedService(t *testing.T) {
	const uid = 4242

	for _, tc := range []struct {
		name    string
		goos    string
		answers map[string]scripted
	}{
		{
			name: "darwin bootout not loaded",
			goos: "darwin",
			answers: map[string]scripted{
				"launchctl bootout gui/4242/dev.marshal.daemon": {
					out: `Could not find service "dev.marshal.daemon" in domain for user gui: 4242`,
					err: errors.New("exit status 113"),
				},
			},
		},
		{
			name: "linux stop not loaded",
			goos: "linux",
			answers: map[string]scripted{
				"systemctl --user stop " + Unit: {
					out: "Failed to stop " + Unit + ": Unit " + Unit + " not loaded.",
					err: errors.New("exit status 5"),
				},
				"systemctl --user disable --now " + Unit: {
					out: "Unit " + Unit + " not loaded.",
					err: errors.New("exit status 5"),
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			m, err := newManager(tc.goos, home)
			if err != nil {
				t.Fatalf("newManager(%q): %v", tc.goos, err)
			}
			m.UID = uid
			m.Run = (&fakeRunner{answers: tc.answers}).run

			if err := m.Stop(context.Background()); err != nil {
				t.Errorf("Stop() on a service that is not loaded: %v", err)
			}

			if err := os.MkdirAll(m.UnitDir, 0o755); err != nil {
				t.Fatalf("make unit dir: %v", err)
			}
			if err := os.WriteFile(m.UnitPath(), []byte("unit\n"), 0o644); err != nil {
				t.Fatalf("write unit: %v", err)
			}
			if err := m.Uninstall(context.Background()); err != nil {
				t.Errorf("Uninstall() on a service that is not loaded: %v", err)
			}
			if _, err := os.Stat(m.UnitPath()); !os.IsNotExist(err) {
				t.Errorf("Uninstall left the unit behind (stat error %v)", err)
			}

			// A real failure is still an error.
			m.Run = (&fakeRunner{answers: map[string]scripted{
				"launchctl bootout gui/4242/dev.marshal.daemon": {
					out: "Operation not permitted",
					err: errors.New("exit status 1"),
				},
				"systemctl --user stop " + Unit: {
					out: "Operation not permitted",
					err: errors.New("exit status 1"),
				},
			}}).run
			if err := m.Stop(context.Background()); err == nil {
				t.Errorf("Stop() hid a real failure")
			}
		})
	}
}

func TestStateReadsTheServiceManager(t *testing.T) {
	const uid = 4242

	for _, tc := range []struct {
		name    string
		goos    string
		answers map[string]scripted
		want    ServiceState
	}{
		{
			name: "darwin running",
			goos: "darwin",
			answers: map[string]scripted{
				"launchctl print gui/4242/dev.marshal.daemon": {out: "gui/4242/dev.marshal.daemon = {\n" +
					"\tactive count = 1\n" +
					"\tstate = running\n" +
					"\tpid = 9876\n" +
					"}\n"},
			},
			want: ServiceState{Installed: true, Running: true, PID: 9876},
		},
		{
			name: "darwin stopped",
			goos: "darwin",
			answers: map[string]scripted{
				"launchctl print gui/4242/dev.marshal.daemon": {out: "gui/4242/dev.marshal.daemon = {\n" +
					"\tstate = not running\n" +
					"}\n"},
			},
			want: ServiceState{Installed: true},
		},
		{
			name: "darwin not installed",
			goos: "darwin",
			answers: map[string]scripted{
				"launchctl print gui/4242/dev.marshal.daemon": {
					out: `Could not find service "dev.marshal.daemon" in domain for user gui: 4242`,
					err: errors.New("exit status 113"),
				},
			},
			want: ServiceState{},
		},
		{
			name: "linux running",
			goos: "linux",
			answers: map[string]scripted{
				"systemctl --user show --property=ActiveState,MainPID,LoadState " + Unit: {
					out: "LoadState=loaded\nActiveState=active\nMainPID=9876\n",
				},
			},
			want: ServiceState{Installed: true, Running: true, PID: 9876},
		},
		{
			name: "linux stopped",
			goos: "linux",
			answers: map[string]scripted{
				"systemctl --user show --property=ActiveState,MainPID,LoadState " + Unit: {
					out: "LoadState=loaded\nActiveState=inactive\nMainPID=0\n",
				},
			},
			want: ServiceState{Installed: true},
		},
		{
			// What systemctl really answers for a unit that was never
			// installed: exit 0, LoadState=not-found. The row below it
			// covers a systemctl that fails instead.
			name: "linux never installed",
			goos: "linux",
			answers: map[string]scripted{
				"systemctl --user show --property=ActiveState,MainPID,LoadState " + Unit: {
					out: "LoadState=not-found\nActiveState=inactive\nMainPID=0\n",
				},
			},
			want: ServiceState{},
		},
		{
			name: "linux unit unknown to systemd",
			goos: "linux",
			answers: map[string]scripted{
				"systemctl --user show --property=ActiveState,MainPID,LoadState " + Unit: {
					out: "Failed to get properties: Unit " + Unit + " not found.",
					err: errors.New("exit status 1"),
				},
			},
			want: ServiceState{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			m, err := newManager(tc.goos, home)
			if err != nil {
				t.Fatalf("newManager(%q): %v", tc.goos, err)
			}
			m.UID = uid
			m.Run = (&fakeRunner{answers: tc.answers}).run

			got, err := m.State(context.Background())
			if err != nil {
				t.Fatalf("State(): %v", err)
			}
			if got != tc.want {
				t.Errorf("State() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// equalLines compares two command lists element by element.
func equalLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
