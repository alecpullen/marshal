# D1 plan: install hardening (linger) — implementation plan

Date: 2026-11-16. Status: ready for review.

Authored against `main` at `87c41ad9`. Every anchor below was checked there.
PR #101 (docs only) is open on `main`; it changes `daemonVersion` in
`cmd/marshal/daemon.go` and nothing in `internal/daemon/manager.go`, so no
task here collides with it. Anchors are function names, not line numbers,
for that reason.

Approved design: [d1-install-hardening.md](../d1-install-hardening.md).
Programme: [design.md](../design.md), including "Sequencing against the
daemon's P6".

## Goal

- `marshal daemon install` enables systemd's per-user linger on Linux, or
  says why it could not, without ever failing an install that worked.
- `marshal daemon status` says whether a Linux install survives logout.
- `marshal daemon uninstall` leaves linger alone and says so.

## Non-goals

- Any init system other than systemd.
- A `marshal daemon linger` subcommand.
- Clearing linger on uninstall.
- Enabling linger from `start`, or from any restart path P6 adds.

## Assumptions

- `internal/daemon/manager.go` already imports `context`, `errors`, `fmt`,
  `os/exec`, `strconv` and `strings`, so the additions below need no import
  change.
- `loginctl show-user <uid> -p Linger` prints `Linger=yes` or `Linger=no`
  and exits 0 for an existing user. `--value` is not used: it is missing
  from older systemd.
- `Manager.GOOS` is exported and is `"darwin"` or `"linux"` by
  construction (`newManager`).
- `internal/daemon/manager_test.go` already has `scripted`, `fakeRunner`,
  and `equalLines`, and `cmd/marshal/daemon_test.go` already has
  `daemonTestEnv`, `fakeDaemonSocket` and a `fakeDaemonManager` that builds
  a darwin-shaped `*daemon.Manager`.
- No test in this plan runs a real `loginctl` or `systemctl`.

## Contracts

```go
// internal/daemon/manager.go

// LingerState is systemd's per-user linger bit, as loginctl reports it.
// Supported is false on darwin, and on a Linux without loginctl: neither
// is a failure.
type LingerState struct {
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
}

// Linger reads the user's linger bit.
func (m *Manager) Linger(ctx context.Context) (LingerState, error)

// EnableLinger sets the user's linger bit.
func (m *Manager) EnableLinger(ctx context.Context) error
```

```go
// cmd/marshal/daemon.go

// daemonEnsureLinger enables linger on Linux and says what it found. It
// never returns an error: the unit is already installed when it runs.
func daemonEnsureLinger(ctx context.Context, m *daemon.Manager, skip bool, stdout io.Writer)

// daemonLingerLine is status's linger line, and whether this platform has
// one at all.
func daemonLingerLine(ctx context.Context, m *daemon.Manager) (string, bool)
```

```go
// cmd/marshal/daemon_test.go

// fakeDaemonManagerOS is fakeDaemonManager for one platform.
func fakeDaemonManagerOS(t *testing.T, goos string, run daemon.Runner)
```

---

## Task 1 — the manager reads and sets linger

**Goal.** `daemon.Manager` can read and set the user's linger bit, on Linux,
without running a real `loginctl` in any test.

**Files.** `internal/daemon/manager.go`, `internal/daemon/manager_test.go`.

**Steps.**

1. Append to the end of `internal/daemon/manager.go`, after `notLoaded`:

```go
// LingerState is systemd's per-user linger bit, as loginctl reports it.
// Supported is false on darwin, and on a Linux without loginctl: neither is
// a failure, so the caller can report the fact instead of failing.
type LingerState struct {
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
}

// Linger reads the user's linger bit. It asks about m.UID rather than a user
// name: loginctl accepts either, and the UID is already how launchd is
// addressed.
func (m *Manager) Linger(ctx context.Context) (LingerState, error) {
	if m.GOOS != "linux" {
		return LingerState{}, nil
	}
	out, err := m.Run(ctx, "loginctl", "show-user", strconv.Itoa(m.UID), "-p", "Linger")
	if err != nil {
		// A machine with no loginctl, or no logind for this user, has no
		// linger bit to report. That is not a Marshal failure.
		if notLoaded(out, err) || errors.Is(err, exec.ErrNotFound) {
			return LingerState{}, nil
		}
		return LingerState{}, fmt.Errorf("loginctl show-user: %w", err)
	}
	state := LingerState{Supported: true}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key == "Linger" {
			state.Enabled = strings.TrimSpace(value) == "yes"
		}
	}
	return state, nil
}

// EnableLinger sets the user's linger bit.
func (m *Manager) EnableLinger(ctx context.Context) error {
	if m.GOOS != "linux" {
		return nil
	}
	if out, err := m.Run(ctx, "loginctl", "enable-linger", strconv.Itoa(m.UID)); err != nil {
		return fmt.Errorf("loginctl enable-linger: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
```

2. Add `"os/exec"` to `manager_test.go`'s import block (after `"os"`).

3. Append to `internal/daemon/manager_test.go`:

```go
// TestLingerReadsAndSetsTheUsersBit: loginctl's answer is the state, and
// enabling is a command that runs only when the bit is off.
func TestLingerReadsAndSetsTheUsersBit(t *testing.T) {
	const uid = 4242
	show := "loginctl show-user 4242 -p Linger"
	enable := "loginctl enable-linger 4242"

	for _, tc := range []struct {
		name    string
		goos    string
		answers map[string]scripted
		want    LingerState
		enable  bool
	}{
		{
			name:    "already enabled",
			goos:    "linux",
			answers: map[string]scripted{show: {out: "Linger=yes\n"}},
			want:    LingerState{Supported: true, Enabled: true},
		},
		{
			name:    "disabled, then enabled",
			goos:    "linux",
			answers: map[string]scripted{show: {out: "Linger=no\n"}},
			want:    LingerState{Supported: true},
			enable:  true,
		},
		{
			name: "loginctl is missing",
			goos: "linux",
			answers: map[string]scripted{
				show: {
					out: `exec: "loginctl": executable file not found in $PATH`,
					err: exec.ErrNotFound,
				},
			},
			want: LingerState{},
		},
		{
			name: "darwin has no linger",
			goos: "darwin",
			want: LingerState{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{answers: tc.answers}
			m := &Manager{GOOS: tc.goos, UID: uid, Run: runner.run}

			got, err := m.Linger(context.Background())
			if err != nil {
				t.Fatalf("Linger: %v", err)
			}
			if got != tc.want {
				t.Errorf("Linger = %+v, want %+v", got, tc.want)
			}

			if tc.enable {
				if err := m.EnableLinger(context.Background()); err != nil {
					t.Fatalf("EnableLinger: %v", err)
				}
			}

			// darwin answers without running anything, which is the point.
			var wantCalls []string
			if tc.goos == "linux" {
				wantCalls = append(wantCalls, show)
				if tc.enable {
					wantCalls = append(wantCalls, enable)
				}
			}
			if !equalLines(runner.calls, wantCalls) {
				t.Errorf("commands = %v, want %v", runner.calls, wantCalls)
			}
		})
	}
}
```

**Verify.**

- `scripts/test.sh quick ./internal/daemon -run TestLinger` — passes.
- `scripts/test.sh quick ./internal/daemon` — the whole package still
  passes (`TestTheManagerRunsTheRightCommands`,
  `TestStopAndUninstallTolerateANotLoadedService`).
- `gofmt -l internal/daemon` — prints nothing.
- `CGO_ENABLED=1 go vet ./internal/daemon/` — clean.

---

## Task 2 — `install` enables linger and reports it

**Goal.** `marshal daemon install` enables linger on Linux, honours
`--no-linger`, and never fails because of it.

**Files.** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

**Steps.**

1. In `runDaemonInstall`, add the flag beside `--webbridge`:

```go
	webbridge := fs.String("webbridge", "", "the webbridge binary to run (default: beside marshal, else on PATH)")
	noLinger := fs.Bool("no-linger", false, "do not enable systemd's linger for this user (Linux)")
```

2. Update the function's usage comment to
   `// Usage: marshal daemon install [--webbridge PATH] [--no-linger]`.

3. In `runDaemonInstall`'s tail, call the helper between the socket line and
   the readiness note:

```go
	fmt.Fprintf(stdout, "unit: %s\n", m.UnitPath())
	fmt.Fprintf(stdout, "socket: %s\n", spec.Paths.Socket)
	daemonEnsureLinger(ctx, m, *noLinger, stdout)
	daemonReadyNote(ctx, spec.Paths, stdout)
```

4. Add `daemonEnsureLinger` after `runDaemonInstall`, before
   `daemonUnitSpec`:

```go
// daemonEnsureLinger enables the user's linger bit on Linux and says what it
// found. It never fails the install: the unit is written and loaded by the
// time it runs, so linger decides only whether the daemon survives logout.
func daemonEnsureLinger(ctx context.Context, m *daemon.Manager, skip bool, stdout io.Writer) {
	if m.GOOS != "linux" {
		return
	}
	if skip {
		fmt.Fprintln(stdout, "linger: left off (--no-linger)")
		return
	}
	state, err := m.Linger(ctx)
	if err != nil {
		fmt.Fprintf(stdout, "linger: not enabled (read Linger: %v)\n", err)
		return
	}
	if !state.Supported {
		fmt.Fprintln(stdout, "linger: not supported (loginctl not found)")
		return
	}
	if state.Enabled {
		fmt.Fprintln(stdout, "linger: already enabled")
		return
	}
	if err := m.EnableLinger(ctx); err != nil {
		fmt.Fprintf(stdout, "linger: not enabled (%v)\n", err)
		return
	}
	fmt.Fprintln(stdout, "linger: enabled")
}
```

5. In `cmd/marshal/daemon_test.go`, add the platform-taking helper and make
   the existing one delegate:

```go
// fakeDaemonManagerOS replaces the service-manager seam for one platform, so
// a test can drive Linux and macOS without a real service manager.
func fakeDaemonManagerOS(t *testing.T, goos string, run daemon.Runner) {
	t.Helper()
	old := daemonManagerFor
	t.Cleanup(func() { daemonManagerFor = old })
	daemonManagerFor = func(home string) (*daemon.Manager, error) {
		m := &daemon.Manager{GOOS: goos, Home: home, UID: 501, Run: run}
		if goos == "darwin" {
			m.UnitDir = filepath.Join(home, "Library", "LaunchAgents")
		} else {
			m.UnitDir = filepath.Join(home, ".config", "systemd", "user")
		}
		return m, nil
	}
}
```

   Then replace `fakeDaemonManager`'s body with
   `fakeDaemonManagerOS(t, "darwin", run)`, keeping its comment and
   signature. No existing test changes.

6. Add two tests, each set up as `TestDaemonInstallPassesExplicitPaths` is
   (same `daemonTestEnv`, same explicit `--webbridge` argument, same
   `fakeDaemonSocket` so the readiness poll does not wait):

   - `TestDaemonInstallEnablesLinger`: Linux manager; script
     `loginctl show-user 501 -p Linger` → `Linger=no`; run `install`;
     assert stdout contains `linger: enabled` and the recorded commands
     contain `loginctl enable-linger 501`.
   - `TestDaemonInstallLeavesLingerOffWithTheFlag`: Linux manager; run
     `install --no-linger`; assert stdout contains
     `linger: left off (--no-linger)` and no recorded command mentions
     `loginctl`.

**Verify.**

- `scripts/test.sh quick ./cmd/marshal -run 'TestDaemonInstall'` — passes,
  including the two existing install tests.
- `gofmt -l cmd/marshal` — prints nothing.

---

## Task 3 — `status` and `uninstall` report linger

**Goal.** `status` says whether the install survives logout; `uninstall`
says it left the bit alone.

**Files.** `cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

**Steps.**

1. Add beside `daemonEnsureLinger`:

```go
// daemonLingerLine is daemon status's linger line, and whether this platform
// has one at all. macOS has no equivalent, so it gets no line rather than a
// "not supported" one for something it does not need.
func daemonLingerLine(ctx context.Context, m *daemon.Manager) (string, bool) {
	if m.GOOS != "linux" {
		return "", false
	}
	state, err := m.Linger(ctx)
	if err != nil {
		return fmt.Sprintf("linger: unknown (%v)", err), true
	}
	if !state.Supported {
		return "linger: not supported (loginctl not found)", true
	}
	if state.Enabled {
		return "linger: enabled", true
	}
	return "linger: disabled (the daemon stops at logout)", true
}
```

2. In `runDaemonStatus`, immediately after the service line and before the
   socket call:

```go
	fmt.Fprintln(stdout, daemonServiceLine(state))
	if line, ok := daemonLingerLine(ctx, m); ok {
		fmt.Fprintln(stdout, line)
	}
```

3. In `runDaemonService`'s `"uninstall"` branch, after the kept-data line:

```go
		if state, err := m.Linger(ctx); err == nil && state.Supported && state.Enabled {
			fmt.Fprintln(stdout, "linger: left enabled (per user, not per unit)")
		}
```

4. Add two tests:

   - `TestDaemonStatusReportsLinger`: Linux manager; `Linger=yes`; assert
     the output has `linger: enabled` on the line after the `service:`
     line. (Drive `status` through `fakeDaemonSocket` with an answering
     client, as `TestDaemonStatusReportsARunningDaemon` does.)
   - `TestDaemonUninstallLeavesLingerEnabled`: Linux manager; `Linger=yes`;
     run `uninstall`; assert the output has
     `linger: left enabled (per user, not per unit)` and that no recorded
     command contains `disable-linger`.

5. Confirm `TestDaemonStatusReportsARunningDaemon` is unchanged and still
   passes: its manager is darwin, so it gains no line.

**Verify.**

- `scripts/test.sh quick ./cmd/marshal -run 'TestDaemon'` — the whole daemon
  command surface passes.
- `gofmt -l cmd/marshal` — prints nothing.
- `CGO_ENABLED=1 go vet ./cmd/marshal/` — clean.

---

## Task 4 — record the outcome and verify the whole tree

**Goal.** The spec records what was built, and the repository is clean.

**Files.** `docs/distribution/d1-install-hardening.md`.

**Steps.**

1. Append an `## Implementation record` section to the D1 spec, in the style
   of `docs/daemon/p4-local-daemon.md`'s records: what was built (the two
   manager methods, the two command helpers, the four tests), anything this
   plan got short (say so plainly), and the verification that ran.

2. Run the repository's own gates.

**Verify.**

- `gofmt -l internal cmd test web` — prints nothing.
- `CGO_ENABLED=1 go vet ./...` — clean.
- `scripts/test.sh full` — passes.
- Manual, on a Linux box if one is available:
  `marshal daemon install` prints a `linger:` line, `marshal daemon status`
  repeats it, and `loginctl show-user "$(id -u)" -p Linger` agrees with it.

## Done when

- A Linux install enables linger by default, and `--no-linger` prevents it.
- Every linger outcome is a line, never a failed install.
- `status` reports linger exactly once, after the service line.
- `uninstall` never disables linger and says so.
- macOS behaviour and output are unchanged.
