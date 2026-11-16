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

## Implementation record

Implemented 2026-11-16 on branch `d3-tray-app`, in eight commits (one per
plan task). This section records what was actually built and, just as
importantly, what was **not** run — the app's native halves were never
compiled on this machine.

### Versions pinned

- **Flutter 3.35.1**, channel stable (Dart 3.9.0). This is the exact SDK
  the app was developed and last verified against; `go-checks.yml` and both
  release jobs pin it rather than a floating channel, because a Flutter
  upgrade changes both the analyzer's rules and `dart format`'s output and
  neither should arrive on a branch that did not touch the app.
  **Superseded by F0 of the Studio programme**
  ([`f0-foundations.md`](../studio-flutter/f0-foundations.md)): the app and
  all three pins moved to Flutter **3.50.0** (Dart 3.14.0), with Material and
  Cupertino from the `material_ui` and `cupertino_ui` packages.
- `tray_manager` **0.5.3**, `launch_at_startup` **0.5.1**,
  `flutter_lints` **4.0.0**, all locked in `app/pubspec.lock`. The plan's
  floors (0.2.4 / 0.3.3 / 4.0.0) were superseded by what `flutter pub add`
  resolved at these Flutter versions; 0.5.3 is the plugin whose API the
  renderer is written against.

### The plugin calls the renderer actually uses

`app/lib/src/menu/render.dart` is the only file importing `tray_manager`.
At 0.5.3 — which re-exports `menu_base` — it calls:

- `trayManager.setIcon('assets/tray.png', isTemplate: true)` — once, with
  the template flag, so macOS themes the menu-bar icon for the current
  appearance. A coloured icon looks wrong in dark mode.
- `trayManager.setToolTip('Marshal')`.
- `trayManager.setContextMenu(Menu(items: [...]))`, translating
  `TraySeparator` to `MenuItem.separator()`, a checked `TrayItem` to
  `MenuItem.checkbox(key:, label:, checked:, disabled:, onClick:)`, and any
  other to `MenuItem(key:, label:, disabled:, onClick:)`. A disabled item
  is given no click handler at all, so the plugin cannot deliver a click
  the app would have to ignore.
- `trayManager.addListener(this)` with `RenderHost … with TrayListener` —
  registering is what makes a click on an item reach its `onClick`.

The menu is rebuilt and re-set on every poll, so a plugin that cannot
mutate a live menu is not a problem: `TrayHost.show` replaces it wholesale.

`notify` has no platform notification in this plugin version, so it writes
to the daemon's log — the same log the menu's `Open Log…` opens.

### Two things the plan got short

1. **`launch_at_startup` 0.5.1 ships no macOS platform code.** Its README
   asks each app to add a `launch_at_startup` `FlutterMethodChannel` handler
   to `macos/Runner/MainFlutterWindow.swift` *and* to add sindresorhus'
   `LaunchAtLogin` Swift package in Xcode. Without the channel the plugin's
   `isEnabled` throws on macOS; `main.dart` catches it and reads the login
   item as off, so the gap is silent rather than loud — which is why it
   surfaced only on review. The handler is now in `MainFlutterWindow.swift`,
   answering `launchAtStartupIsEnabled` and `launchAtStartupSetEnabled` over
   `ServiceManagement`'s `SMAppService`, the API `LaunchAtLogin` itself
   wraps on macOS 13+. So **the Swift package is not needed**: nothing has
   to be fetched, and the Xcode project needs no dependency added. Below
   macOS 13 the channel returns an "unsupported" error, which `main.dart`
   already turns into an off checkbox plus a reported reason on toggle.
   Login at startup is therefore a macOS 13+ feature; older systems keep the
   checkbox off rather than pretending.

2. **The template's App Sandbox had to go.** Both
   `Runner/Release.entitlements` and `Runner/DebugProfile.entitlements` now
   set `com.apple.security.app-sandbox` to `false`. A child process inherits
   its parent's sandbox, and this app's entire interface to Marshal is
   exec'ing the bundled `marshal` — which must read the data directory and
   connect to the daemon's unix socket. A sandboxed tray would have shown
   "status unavailable" forever and could never have installed the daemon,
   i.e. it would have failed at both things the menu exists for. The App
   Store, which is what the sandbox is for, is a non-goal here. This is a
   deviation from the plan, which did not mention entitlements at all.

### Bundle identity (task 5)

`PRODUCT_NAME = Marshal` (so the bundle is `Marshal.app` and `CFBundleName`
is what the Finder shows), `PRODUCT_BUNDLE_IDENTIFIER = dev.marshal.app`,
and `EXECUTABLE_NAME = marshal_app`, so the binary inside the bundle never
shadows `marshal` on `PATH`. `Info.plist` sets `LSUIElement` `<true/>`: no
Dock icon, no window. The product file reference, the shared scheme's
`BuildableName`, and the `RunnerTests` `TEST_HOST` were updated to
`Marshal.app` so an Xcode build still resolves its own product; `RunnerTests`
bundle id became `dev.marshal.app.RunnerTests`.

### CI and release (tasks 6 and 7)

- `go-checks.yml` gained an `app` job: `flutter pub get`, `flutter analyze`,
  `flutter test`, and `dart format --output=none --set-exit-if-changed lib
  test`. It needs no GTK, no display and no C toolchain — the Dart half is
  pure — so it runs in about a minute on `ubuntu-latest`.
- `release.yaml` gained `app-darwin` and `app-linux`, both `needs: release`
  and both `contents: write`. Each downloads the archives GoReleaser just
  attached to the tag's **draft** release, matches the runner's arch,
  extracts `marshal` and `webbridge`, builds the app with
  `--dart-define=APP_VERSION=<tag minus v>` (GoReleaser's `.Version` has no
  leading `v`, so the menu's version-skew line does not fire on every
  release), and copies both binaries into the bundle.
- `app-darwin` signs **inside-out** — frameworks, then the two bundled
  binaries, then `Marshal.app` — because a nested Mach-O signed after its
  enclosing bundle invalidates the bundle's signature and notarization then
  fails on a signature nobody edited by hand. It notarizes the zip with
  `notarytool`, staples the bundle, and uploads a `Marshal.dmg`. It reads
  the **same** repository secrets GoReleaser's notarize block reads
  (`MACOS_SIGN_P12`, `MACOS_SIGN_P12_PASSWORD`, `MACOS_NOTARY_*`), so one
  secret set covers both the daemon binaries and the app. Without them it
  still builds and tests the app, uploads nothing, and logs
  `unsigned: not uploaded` — an unsigned `.app` is refused by Gatekeeper and
  looks like a Marshal bug.
- `app-linux` installs `clang cmake ninja-build pkg-config libgtk-3-dev
  libayatana-appindicator3-dev`, builds `--release`, and uploads
  `marshal-app_<tag>_linux_x64.tar.gz`.

### Tests

- Go: `TestTheStatusDocumentIsCurrent` holds the document to
  `test/contract/daemon-status.json`, regenerated with `-update`; two
  command tests cover `--json` exiting 0 when the socket is silent and
  carrying the daemon when it answers.
- Dart, now 12 tests: `status_test.dart` parses the shared fixture and
  refuses an unknown schema; `menu_model_test.dart` covers install/start/stop
  selection, version skew, the disabled log item, and keeping the last good
  status when a read fails; `no_io_test.dart` was added in task 8 — the plan
  and §8 required it, but tasks 1–4 stopped at ten tests and left the
  boundary rule unenforced. It walks `lib/` and fails if any file other than
  `lib/src/daemon/cli.dart` mentions `dart:io`, and asserts that exempt file
  still exists so the rule cannot become a no-op.

### What was run, and what was not

Run and passing on this machine (Linux, no Xcode):

- `flutter analyze` — no issues; `flutter test` — 12/12;
  `dart format --output=none --set-exit-if-changed lib test` — clean.
- `CGO_ENABLED=1 go build ./cmd/marshal`, the affected Go tests
  (`./internal/daemon ./cmd/marshal`), `go vet`, and `gofmt -l`.
- Both workflow files parse (`python3 -c 'import yaml; yaml.safe_load(...)'`).
- Static checks that stand in for a macOS build: `xmllint --noout` on
  `Info.plist`, both entitlements files and `MainMenu.xib`, plus
  `plistlib.load` confirming `LSUIElement` is `true`, the bundle id is
  `dev.marshal.app`, `PRODUCT_NAME = Marshal`, `EXECUTABLE_NAME =
  marshal_app`, and the sandbox keys are `false`. `plutil` does not exist on
  this host (`command -v plutil` fails), so the plan's `plutil -p … | grep
  LSUIElement` was replaced by those two. The Swift was **not** compiled: no
  `swiftc`, no Xcode, no macOS.

**Not run, and needing a machine that can:**

- `flutter build macos --debug|--release`, `flutter run -d macos`, Xcode
  indexing, and `RunnerTests`. There is no macOS host here; the Swift
  method channel's compilation and the inside-out codesign order are
  therefore **unverified**, as is whether `SMAppService.mainApp.register()`
  is accepted for a bundle run from `build/`, rather than from
  `/Applications`.
- `flutter build linux --release`. `cmake`, `ninja-build`, `clang` and the
  GTK3/ayatana development packages are absent and need elevation to
  install, which this work was not permitted to do. Nothing was faked: the
  Linux bundle was never produced, so the release job's
  `build/linux/x64/release/bundle` path and the tarball layout are unproven.
- Any part of the release workflow at tag time: `gh release download`
  against the *draft* release, `notarytool submit --wait`,
  `stapler staple`, `hdiutil create`, and both `gh release upload`s. These
  need a tag and the signing secrets.
- The manual checks: a menu-bar icon with no Dock icon, `Launch at Login`
  appearing under System Settings → General → Login Items and surviving a
  restart, `spctl --assess --type execute -vv Marshal.app` saying
  `accepted`, and an AppIndicator icon in a real KDE or XFCE session (a bare
  GNOME session shows none without the AppIndicator extension, which is a
  documented platform property, not a bug).

### Small deviations worth a reviewer's eye

- The Linux login item is written by the plugin to
  `~/.config/autostart/Marshal.desktop` (the `appName` passed to
  `launchAtStartup.setup`), not `marshal-app.desktop` as §7 sketched.
- `main.dart` reads the app version from
  `String.fromEnvironment('APP_VERSION', defaultValue: 'dev')`, so a
  developer's build says `dev` and only names a version when it differs
  from the daemon's.
- The release jobs pass `APP_VERSION` with its leading `v` stripped, so the
  version-skew suffix in the status line stays silent on a normal release.

### Review follow-ups

Four findings from the review, fixed in three commits beside this one, each
at the layer that had the wrong answer.

**`not answering` had no reading.** `_stateLine`
(`app/lib/src/menu/model.dart`) mapped every silent socket to
`Marshal — daemon stopped`, which the service manager contradicts while the
service is running — §4's missing fourth reading. A running service with a
silent socket now reads `Marshal — daemon not answering`; a service that is
not running still reads `stopped`, and the action selection is untouched: a
running daemon stays stoppable.

**A missing binary had no state.** `status()`
(`app/lib/src/daemon/cli.dart`) threw only `DaemonException`, so §5's
"missing binary is not fatal" came out as the generic
`· status unavailable` and was indistinguishable from a hung daemon. A
spawn's errno 2 is now `MarshalMissingException`; `menuFor`
(`app/lib/src/menu/model.dart`) takes the state, renders
`Marshal — marshal not found`, and offers no daemon action over it, and
`run` reports the same way whichever action hit it. `Open Fleet…`,
`Open Log…` and the login toggle are not daemon actions and stay. Two tests
pin the behavior: the menu's label and empty action set
(`app/test/menu_model_test.dart`), and the cli's classification of a
missing-binary spawn (`app/test/daemon_cli_test.dart`).

**A lazy cast could kill the tray.** `DaemonInfo.fromJson`
(`app/lib/src/daemon/status.dart`) left `projects` a lazily cast view, so a
mistyped entry threw only when the menu read `daemon.projects.length` — in
menu build, outside the poll's try, an uncaught async error from the refresh
timer that takes the isolate down. The cast is forced with `.toList()`.

**macOS opened a blank window, and closing it quit the app.** The template's
window (`app/macos/Runner/Base.lproj/MainMenu.xib`) is visible at launch and
nothing hid it, while `applicationShouldTerminateAfterLastWindowClosed`
returned true, so closing that window ended the tray app. `awakeFromNib`
(`app/macos/Runner/MainFlutterWindow.swift`) now orders the window out on
the main thread — synchronously, and again on the next main-loop turn, in
case AppKit honours the xib's visible-at-launch flag after the nib's
`awakeFromNib` — and the delegate answers false, so a window close can never
quit the app. **This fix is unverified**: no Mac and no Xcode on this host,
so the Swift was never compiled; `flutter build macos` and a launch are the
proof it needs.

Dart gates ran before each code commit and stay clean: `flutter analyze`
reports no issues, `flutter test` passes 15 of 15 (the menu tests gained the
`not answering` label and the not-found state, and `daemon_cli_test.dart`
gained the spawn classification), and
`dart format --output=none --set-exit-if-changed lib test` formats no file.

## Open questions

None.
