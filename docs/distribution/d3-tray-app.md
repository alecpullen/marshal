# D3 — the tray app (Flutter): design

Status: approved 2026-11-16. Part of [the distribution programme](design.md).

**Sequencing.** `marshal daemon status` is the one Go function both this
phase and PR #101's P6 plans edit. This phase anchors on function names and
writes its fixture with a stamped release version, never a `dev+<commit>`
build stamp, so P6's version change does not move the golden. See the
programme's "Sequencing against the daemon's P6".

## Context

The daemon answers everything a menu needs. `marshal daemon status` renders
the service state, the socket, the daemon's version and start time, each
project child, each pin that no child serves, and the OAuth refresh helper
(`cmd/marshal/daemon.go`). `GET /api/daemon` answers the same facts as
`contract.DaemonInfo` over the socket. `marshal fleet` opens the fleet
screen. There is no GUI of any kind, and no tray dependency in `go.mod`.

The programme decided the native apps will be Flutter, one repository, and
that the tray is the **first screen** of that app rather than a stopgap to
be rewritten later. It also decided the app **bundles** `marshal` and
`webbridge` and offers to install the daemon from its own menu — which
makes it the GUI for D2's installer.

Two constraints shape the design:

- **Dart cannot use `HttpClient` on a unix socket.** Speaking the daemon's
  socket protocol from Flutter would mean hand-rolling HTTP over
  `Socket.connect(..., type: unix)`. The app therefore shells out to a
  versioned `marshal daemon status --json` and never opens the socket.
- **`marshal daemon status --json` does not exist.** No subcommand of
  `marshal` emits JSON today. That is the only new dependency on the Go
  side, and it is small.

## Design

### 1. The app's position

- Directory `app/`, a Flutter project named `marshal_app`, beside `web/ui`
  and built by the same release tag.
- **Boundary rule, enforced by a test:** the app reaches Marshal only
  through `marshal daemon status --json` and the commands in the menu. It
  never opens the socket, never reads `fleet.json`, the state directory or
  the config, and never embeds an agent-side Go type. All process and file
  I/O lives in `lib/src/daemon/cli.dart`; every other file under `lib/`
  imports neither `dart:io` nor `package:flutter` unless it is a rendering
  adapter.
- This is the `web/` shape applied to Dart, so a later split into its own
  repository is a directory move rather than an untangling.

### 2. Go: `marshal daemon status --json`

`internal/daemon/status.go` gains the document type; `cmd/marshal/daemon.go`
gains the flag.

```go
// Status is the `daemon status --json` document. SchemaVersion is the
// contract's: a consumer refuses a version it does not know.
type Status struct {
    SchemaVersion int          `json:"schemaVersion"`
    Answering     bool         `json:"answering"`
    Service       ServiceState `json:"service"`
    Linger        LingerState  `json:"linger"`
    Daemon        *Info        `json:"daemon,omitempty"`
}
```

`ServiceState` already exists in `manager.go`; it gains JSON tags
(`installed`, `running`, `pid`) rather than a second declaration.
`LingerState` arrives from D1 with its tags already on it.

- `SchemaVersion` is `1`. `Linger` is always present, with
  `supported: false` on darwin, so the app has one code path.
- `Daemon` is omitted when the socket did not answer; `Answering` carries
  the same fact explicitly.
- **With `--json` the command exits 0 whenever it wrote the document.** A
  machine consumer must not special-case an exit code to learn a fact the
  document states. Text mode keeps today's exit behaviour exactly.
- The document is written to stdout, one object, newline-terminated; all
  human lines are suppressed.

### 3. The shared fixture

`test/contract/daemon-status.json` holds one documented example. A Go test
in `cmd/marshal` compares the marshalled document against it and
regenerates it with `-update`; a Dart test in `app` parses the same file.
One file, two languages, and drift fails CI on the side that changed. This
is `tsgen`'s currency test without a generator, which is the right size
for a single document.

### 4. The Flutter app

```
app/lib/main.dart
app/lib/src/daemon/cli.dart      the only file allowed dart:io
app/lib/src/daemon/status.dart   Status/Service/Linger models and the parser
app/lib/src/menu/model.dart      pure: Status -> menu items
app/lib/src/menu/host.dart       TrayHost interface: setMenu, setTitle, onSelect, notify
app/lib/src/menu/render.dart     the tray_manager adapter
```

- `status.dart` and `model.dart` are pure Dart: no `dart:io`, no Flutter,
  no plugin. That is what the widget test and the boundary test act on.
- `render.dart` owns every `tray_manager` call, so the plugin is one file
  that can be swapped for the eventual app's own shell.
- **No window.** macOS sets `LSUIElement` in
  `macos/Runner/Info.plist`; Linux creates none. The app is a status item
  with a menu.
- The app bundle is `Marshal.app`, the executable `marshal-app`, so
  nothing shadows `marshal` on `PATH`.

Menu, built from one `Status`:

```
marshal daemon — running (pid 1234) · 3 projects     (disabled)
────────────────────────────────────────────
Open Fleet…                                          marshal fleet
Install Marshal Daemon…   (not installed)
Start Daemon              (installed, stopped)
Stop Daemon               (running)
────────────────────────────────────────────
Open Log…                 (disabled when the log is absent)
Launch at Login           (checkbox)
────────────────────────────────────────────
Quit
```

- Exactly one of install/start/stop is shown, from `service.installed` and
  `service.running`.
- The status line reads `not installed`, `running (pid N)`, `stopped` or
  `not answering`, and appends `· N projects` when the daemon answered.
- Version skew is visible rather than silent: when the daemon's version
  differs from the app's, the status line appends `· daemon V (app W)`.
  The app's version arrives as `--dart-define=APP_VERSION` at build time.
- Refresh: every 5 s, and immediately after any action. A failed call
  keeps the last good state and adds `· status unavailable` rather than
  emptying the menu.
- `Open Log…` and `Open Fleet…` use `open` on macOS and `xdg-open` on
  Linux, from `cli.dart`.

### 5. Locating the bundled binaries

Order, in `cli.dart`:

1. macOS: `Contents/Resources/bin/marshal`, derived from
   `Platform.resolvedExecutable` (`…/Marshal.app/Contents/MacOS/marshal-app`).
2. Linux: `bin/marshal` beside the resolved executable.
3. `PATH`, for a developer running from source.

A missing binary is not fatal: the menu status line reads `marshal not
found` and the daemon actions are disabled, so the app never presents an
action that cannot work.

### 6. Bundling and the release job

The Go release must finish before the app can be built, so `.goreleaser`
cannot own this. `.github/workflows/release.yaml` gains two jobs, both
`needs: release`:

- **`app-darwin`** on `macos-latest`: `gh release download` the tag's
  darwin archive, extract `marshal` and `webbridge` to a staging dir,
  `flutter build macos --release`, copy them post-build into
  `Marshal.app/Contents/Resources/bin/`, then sign **each nested binary
  first**, sign the bundle with the hardened runtime, `notarytool submit`,
  and `stapler staple`. Finally `gh release upload` a `.dmg` (built with
  `hdiutil`).
- **`app-linux`** on `ubuntu-latest`: apt-install `clang cmake
  ninja-build pkg-config libgtk-3-dev libayatana-appindicator3-dev`,
  extract the linux archive the same way, `flutter build linux --release`,
  copy the binaries into `build/linux/x64/release/bundle/bin/`, and upload
  a `.tar.gz` of the bundle.

Order matters and is easy to get wrong: a nested Mach-O signed after its
enclosing bundle invalidates the bundle's signature and notarization fails.
The darwin job signs inside-out and stamps the order in a comment.

**Signing gate.** Without `MACOS_SIGN_IDENTITY` the darwin job still builds
and runs the tests, but uploads **no** release asset: an unsigned `.app`
would be refused by Gatekeeper and would look like a Marshal bug. A fork
gets a CI artifact and a log line saying why.

### 7. Launch at login

Off by default; the menu checkbox writes or removes the login item. macOS:
a login item (`launch_at_startup`, which uses `SMAppService` on 13+).
Linux: `~/.config/autostart/marshal-app.desktop`. An app that adds itself
to login items unasked is the kind of thing users uninstall.

### 8. Tests

- Go: the `status --json` golden test against
  `test/contract/daemon-status.json`, plus the existing daemon tests
  extended for `--json` (exit 0 when not answering, `answering: false`,
  text mode unchanged).
- Dart: `status_test.dart` parses the shared fixture (including a
  `daemon: null` variant written inline); `menu_model_test.dart` asserts
  the install/start/stop selection and the disabled items for each state;
  `no_io_test.dart` walks `lib/` and fails if any file other than
  `lib/src/daemon/cli.dart` mentions `dart:io`; a widget test drives
  `model.dart` with a fake `TrayHost`.
- CI: an `app` job in `.github/workflows/go-checks.yml` on `ubuntu-latest`
  with a pinned Flutter SDK: `flutter analyze`, `flutter test`. Building
  the macOS and Linux bundles stays in the release workflow.

## Decisions and trade-offs

1. **The CLI is the app's only interface.** It keeps the app free of the
   socket protocol, of contract codegen, and of any knowledge of the
   daemon's files, and it makes the front-end replaceable. The cost is a
   process per poll, which is milliseconds.
2. **Exit 0 under `--json`.** A machine consumer should read the document,
   not interpret an exit code. Text mode is untouched.
3. **A shared fixture instead of a generator.** One document does not
   justify a Dart code generator; a checked-in example parsed by both sides
   catches the same drift.
4. **No window in v1.** The tray is the deliverable; a window would need
   decisions (transcript, sessions, config) that belong to the native app
   phase, not to a menu.
5. **Post-build copy, not Xcode project surgery.** Copying the binaries
   into `Contents/Resources/bin/` after `flutter build macos` is
   inspectable and survives a Flutter upgrade better than adding a Copy
   Files build phase by hand.
6. **Unsigned app builds are not released.** See §6: a Gatekeeper refusal
   on a first launch is indistinguishable from a broken app.
7. **Launch at login is opt-in.** See §7.

## Assumptions

- `tray_manager` (or `system_tray`, whichever the plan pins) supports
  dynamic menus on both macOS and Linux at the pinned Flutter version. If
  its Linux menu cannot be rebuilt after a state change, the plan falls
  back to rebuilding the tray, which the `TrayHost` seam makes a one-file
  change.
- Flutter Linux desktop builds on `ubuntu-latest` with GTK3 and
  `libayatana-appindicator3-dev` from apt; Flutter macOS builds on
  `macos-latest` with the bundled Xcode.
- `gh release download`/`upload` against the draft release works with the
  workflow's `GITHUB_TOKEN` (`contents: write`), as the existing release
  job already has.
- A bare GNOME session shows no status icon without the AppIndicator
  extension. The app still runs and its menu is reachable in a host that
  supports SNI; this is a platform property, documented, not worked
  around.

## Non-goals

- A window, a transcript view, a session sheet, or any fleet interaction
  beyond launching `marshal fleet`.
- The `needs you N` badge. `Info` carries no fleet queue, so it needs a
  headless fleet-queue read — a follow-up phase.
- Pin/unpin from the tray: without a working directory there is no "current
  project", and a project picker is a window's job.
- Windows, auto-update, the Mac App Store, Flathub.
- Replacing the TUI, the Studio, or the daemon's own commands.
- Starting the daemon's OAuth helper or managing sessions from the menu.

## Open questions

None.
