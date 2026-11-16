# D1 — install hardening (linger): design

Status: approved 2026-11-16. Part of [the distribution programme](design.md).

**Sequencing.** PR #101's P6 plans change `daemonVersion` in
`cmd/marshal/daemon.go` but nothing in `internal/daemon/manager.go`, which
is where this phase lives. This spec and its plan therefore anchor on
function names, never line numbers. See the programme's "Sequencing against
the daemon's P6".

## Context

`marshal daemon install` writes a systemd user unit on Linux
(`internal/daemon/units.go`, `SystemdUnit`) whose only install target is
`WantedBy=default.target`, and loads it with `systemctl --user
daemon-reload`, `enable --now` and `restart`
(`internal/daemon/manager.go`, `Manager.Install`). Nothing enables
lingering.

The consequence is a wrong answer to a documented promise.
`docs/daemon/design.md` §4.3 says a pinned project keeps running "through
the linger", and `docs/daemon/p4-local-daemon.md` D9 builds pinning on
the same assumption. On a desktop that only matters after logout; on a
headless box reached over SSH the daemon dies with the last session, and
with it every pinned project's child and its index watcher. The failure is
silent: `status` reports a healthy daemon right up until the user logs
out, and nothing reports that it will not come back.

`loginctl enable-linger` is a per-user systemd bit, not a per-unit one. It
is also not owned by Marshal: the user's other user units depend on the
same bit.

## Design

### 1. Manager primitives

`internal/daemon/manager.go` gains two methods and one type. Linger is
Linux-only; on darwin both methods answer without running anything, so
the command needs no platform branch of its own.

```go
// LingerState is systemd's per-user linger bit.
type LingerState struct {
    Supported bool `json:"supported"`
    Enabled   bool `json:"enabled"`
}

// Linger reads the user's linger bit.
func (m *Manager) Linger(ctx context.Context) (LingerState, error)

// EnableLinger sets the user's linger bit.
func (m *Manager) EnableLinger(ctx context.Context) error
```

- On darwin: `Linger` returns `LingerState{Supported: false}` and no
  error; `EnableLinger` is a no-op returning nil.
- On Linux, `Linger` runs `loginctl show-user <uid> -p Linger` and reads
  the `Linger=yes` / `Linger=no` line. It is called **without**
  `--value`, because `--value` is missing from older systemd and the
  keyed line parses everywhere.
- The subject is `m.UID`, already on `Manager`, rendered with
  `strconv.Itoa`. `loginctl` accepts a UID, so no user-name lookup can
  fail or pick the wrong account.
- A runner error whose text matches the existing `notLoaded` phrase list
  (which already covers "not found") or an `exec.ErrNotFound` from the
  binary being absent yields `LingerState{Supported: false}` with no
  error: no `loginctl` is not a Marshal failure.
- Any other error is returned.

### 2. `install` enables linger

The command owns the sequence and the wording; the manager owns the
system call. This follows the contract already stated at the top of
`cmd/marshal/daemon.go`: the command is the user-facing half and never
runs a service-manager command itself.

`runDaemonInstall` gains a `--no-linger` flag, and after `m.Install`
succeeds:

| Situation | Line written to stdout |
|---|---|
| darwin | none |
| already enabled | `linger: already enabled` |
| enable succeeded | `linger: enabled` |
| `--no-linger` | `linger: left off (--no-linger)` |
| `loginctl` missing | `linger: not supported (loginctl not found)` |
| any other failure | `linger: not enabled (<the error>)` |

**No linger outcome changes the exit code.** The unit is written and
loaded by then, and the daemon works for the rest of the login session;
linger only decides whether it survives logout. Failing the install
because a machine has no systemd user manager would turn a working
install into a broken one.

The line goes after `socket:` and before the readiness note, so
`answering` / `not answering yet` stays the last word on whether the
install worked.

### 3. `uninstall` leaves linger alone

`Manager.Uninstall` is unchanged. `runDaemonService` prints
`linger: left enabled (per user, not per unit)` after its usual line when
linger is supported and enabled, so the user is told that Marshal set the
bit and is deliberately not clearing it. Clearing it would stop
everything else the user has installed as a systemd user unit.

### 4. `status` reports linger

`runDaemonStatus` gains one line, Linux only, immediately after the
service line:

| Situation | Line |
|---|---|
| supported, enabled | `linger: enabled` |
| supported, disabled | `linger: disabled (the daemon stops at logout)` |
| not supported | `linger: not supported (loginctl not found)` |
| read failed | `linger: unknown (<the error>)` |

macOS prints nothing: there is no equivalent, and a "not supported" line
for a platform that does not need it is noise. A failed read is a line,
not an error — `status` already exits non-zero only for a silent socket.

## Decisions and trade-offs

1. **Enable by default, `--no-linger` to opt out.** Lingering is what
   makes pinning and background work behave as documented, so the default
   is the promise; the flag exists for a user who does not want their
   user manager to survive logout.
2. **A warning, never an error.** See §2. The alternative — failing the
   install — punishes the exact machines (containers, minimal images,
   non-systemd init) where the rest of the install still works.
3. **Never clear linger on uninstall.** The bit is shared and per-user.
   Marshal clearing a setting it did not exclusively own is a bug, not a
   cleanup.
4. **A UID, not a user name.** Fewer failure modes, one fewer lookup, and
   `Manager.UID` already exists for the launchd target.
5. **The manager gains primitives, not a policy.** `Install` keeps its
   present meaning, so the existing tests and callers are unaffected and
   the flag stays where flags belong.

## Assumptions

- CI's `ubuntu-latest` runner has `loginctl` and a user manager, since
  D1's command tests drive the seam rather than the service manager; the
  manager tests use the scripted `Runner` already in `manager_test.go`,
  and never run `loginctl`.
- `cmd/marshal/daemon_test.go` replaces `daemonManagerFor` with a
  hand-built `&daemon.Manager{...}` (it does, at the time of writing), so
  no new seam is needed to test the command half.
- `loginctl show-user <uid> -p Linger` exits 0 for an existing user with
  no lingering; a non-zero exit means the read failed, not "disabled".

## Non-goals

- Init systems other than systemd (OpenRC, runit, s6). The install path
  already assumes `systemctl`; this phase does not broaden it.
- Linger for other users, or a `--user` flag.
- A `marshal daemon linger on|off` subcommand. The flag on `install` plus
  the `status` line answer the question.
- Enabling linger from `start` or from the tray. `install` is where the
  machine is configured; `start` runs a unit that already exists.

## Open questions

None.
