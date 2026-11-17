# D3 plan: the tray app — implementation plan

Date: 2026-11-16. Status: ready for review.

Authored against `main` at `87c41ad9`.

Approved design: [d3-tray-app.md](../d3-tray-app.md).
Programme: [design.md](../design.md), including "Sequencing against the
daemon's P6".

This plan runs after D1 and D2. It uses D1's `Manager.Linger` and
`daemonLingerLine`, and it bundles the binaries D2's release produces.

PR #101's P6 plans change `daemonVersion` in `cmd/marshal/daemon.go` and
add per-child lines to `runDaemonStatus`. Task 1 below replaces that
function wholesale, so if P6 has landed, re-read the function and carry its
child-line additions into the replacement rather than dropping them.

## Goal

- `marshal daemon status --json` emits one versioned document, and exits 0
  whenever it wrote it.
- A checked-in fixture is parsed by both a Go test and a Dart test, so the
  two sides cannot drift.
- A Flutter app in `app/` shows a tray icon whose menu reports daemon state
  and offers the daemon's actions, with no window.
- A release builds the app for macOS and Linux, bundles the signed daemon
  binaries, and signs and notarizes the macOS app.

## Non-goals

- A window, a transcript view, or fleet interaction beyond launching
  `marshal fleet`.
- The `needs you N` badge.
- Pin/unpin from the tray.
- Windows, auto-update, the App Store, Flathub.
- Any change to what the daemon's socket answers.

## Assumptions

- `contract.DaemonInfo` (aliased as `daemon.Info`) already carries
  `version`, `profile`, `pid`, `startedAt`, `socket`, `projects`, `pinned`,
  `children` and `refresh` (`web/bridge/contract/daemon.go`).
- `test/` exists at the repository root (`test/usability`, `test/daemon`),
  so `test/contract/daemon-status.json` is a new directory there.
- Flutter stable is installed by CI through `subosito/flutter-action`; the
  exact version is pinned in Task 6 and recorded in the spec's
  implementation record.
- `tray_manager` and `launch_at_startup` exist on pub.dev and support
  macOS and Linux. Their exact API is checked in Task 4 against the pinned
  version, not guessed here.
- The macOS release runner has Xcode; the Linux runner can apt-install
  `clang cmake ninja-build pkg-config libgtk-3-dev
  libayatana-appindicator3-dev`.

## Contracts

```go
// internal/daemon/status.go

// StatusSchema is the version of the daemon status document.
const StatusSchema = 1

// Status is the `marshal daemon status --json` document.
type Status struct {
	SchemaVersion int          `json:"schemaVersion"`
	Answering     bool         `json:"answering"`
	Service       ServiceState `json:"service"`
	Linger        LingerState  `json:"linger"`
	Daemon        *Info        `json:"daemon,omitempty"`
}
```

```go
// internal/daemon/manager.go — ServiceState gains JSON tags.

type ServiceState struct {
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
	PID       int  `json:"pid"`
}
```

```dart
// app/lib/src/daemon/status.dart
class DaemonStatus {
  const DaemonStatus({required this.schemaVersion, required this.answering,
      required this.service, required this.linger, this.daemon});
  static const supportedSchema = 1;
  factory DaemonStatus.fromJson(Map<String, dynamic> json);
  final int schemaVersion;
  final bool answering;
  final ServiceState service;
  final LingerState linger;
  final DaemonInfo? daemon;
}
```

```dart
// app/lib/src/menu/model.dart
class TrayItem {
  const TrayItem({required this.id, required this.label,
      this.enabled = true, this.checked = false});
}
class TraySeparator { const TraySeparator(); }
class TrayMenu {
  const TrayMenu({required this.title, required this.entries});
  final String title;
  final List<Object> entries;
}
TrayMenu menuFor({
  required String appVersion,
  required bool logExists,
  required bool launchAtLogin,
  DaemonStatus? status,
  String? failure,
});

// app/lib/src/menu/host.dart
abstract class TrayHost {
  Future<void> show(TrayMenu menu);
  Future<void> notify(String message);
}
```

---

## Task 1 — `marshal daemon status --json`

**Goal.** The status document exists, is versioned, and exits 0 whenever it
was written.

**Files.** `internal/daemon/status.go` (new), `internal/daemon/manager.go`,
`cmd/marshal/daemon.go`, `cmd/marshal/daemon_test.go`.

**Steps.**

1. Create `internal/daemon/status.go`:

```go
package daemon

// StatusSchema is the version of the daemon status document. A consumer
// refuses a version it does not know.
const StatusSchema = 1

// Status is the `marshal daemon status --json` document. Linger is always
// present, with Supported false on macOS, so a consumer has one code path.
type Status struct {
	SchemaVersion int          `json:"schemaVersion"`
	Answering     bool         `json:"answering"`
	Service       ServiceState `json:"service"`
	Linger        LingerState  `json:"linger"`
	Daemon        *Info        `json:"daemon,omitempty"`
}
```

2. Give `ServiceState` in `manager.go` its JSON tags, exactly as in
   Contracts. Nothing else about it changes.

3. Replace `runDaemonStatus` in `cmd/marshal/daemon.go` with:

```go
// runDaemonStatus prints decision 12's lines, or writes the JSON document
// the tray app reads.
//
// With --json it exits 0 whenever it wrote the document: the answer to
// "is the daemon answering" is in the document, so a machine consumer must
// not have to read an exit code for it. Text mode keeps the old behaviour,
// including the non-zero exit for a silent socket.
func runDaemonStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "write the status as one JSON document")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) != 0 {
		return fmt.Errorf("marshal daemon status: unexpected argument %q", rest[0])
	}

	home, err := daemonHome()
	if err != nil {
		return err
	}
	m, err := daemonManagerFor(home)
	if err != nil {
		return err
	}
	paths := daemon.PathsFor(home)

	state, err := m.State(ctx)
	if err != nil {
		return fmt.Errorf("read the marshal daemon service: %w", err)
	}

	status := daemon.Status{SchemaVersion: daemon.StatusSchema, Service: state}

	infoCtx, cancel := context.WithTimeout(ctx, daemonStatusTimeout)
	defer cancel()
	info, infoErr := daemonClientFor(paths.Socket).Info(infoCtx)
	if infoErr == nil {
		status.Answering = true
		status.Daemon = &info
	}

	if *asJSON {
		// One loginctl call per invocation: text mode asks
		// daemonLingerLine instead of reading it here.
		if m.GOOS == "linux" {
			if linger, err := m.Linger(ctx); err == nil {
				status.Linger = linger
			}
		}
		return json.NewEncoder(stdout).Encode(status)
	}

	fmt.Fprintln(stdout, daemonServiceLine(state))
	if line, ok := daemonLingerLine(ctx, m); ok {
		fmt.Fprintln(stdout, line)
	}
	if infoErr != nil {
		fmt.Fprintf(stdout, "socket: %s (not answering)\n", paths.Socket)
		return fmt.Errorf("the daemon is not answering on %s", paths.Socket)
	}
	fmt.Fprintf(stdout, "socket: %s (answering)\n", paths.Socket)
	marshalVersion := daemonVersion()
	fmt.Fprintln(stdout, daemonInfoLine(info, marshalVersion))
	fmt.Fprintln(stdout, daemonRefreshLine(info))
	for _, child := range info.Children {
		fmt.Fprintln(stdout, daemonChildLine(child, marshalVersion))
		if warning := strings.TrimSpace(child.EnvWarning); warning != "" {
			fmt.Fprintf(stdout, "  environment: %s\n", warning)
		}
	}
	for _, line := range daemonPinnedLines(info) {
		fmt.Fprintln(stdout, line)
	}
	return nil
}
```

   Add `"encoding/json"` to `cmd/marshal/daemon.go`'s imports.

   **If P6 has landed**, carry its per-child pending-restart lines from the
   old function body into this one, after `daemonChildLine`. Do not drop
   them.

4. Create `test/contract/daemon-status.json` with exactly this content:

```json
{
  "schemaVersion": 1,
  "answering": true,
  "service": {
    "installed": true,
    "running": true,
    "pid": 1234
  },
  "linger": {
    "supported": true,
    "enabled": true
  },
  "daemon": {
    "version": "0.9.1",
    "profile": "local",
    "pid": 4321,
    "startedAt": 1763300000000,
    "socket": "/home/u/.local/share/marshal/daemon/daemon.sock",
    "projects": [
      "/home/u/src/marshal"
    ],
    "pinned": [
      "/home/u/src/marshal"
    ],
    "children": [
      {
        "root": "/home/u/src/marshal",
        "version": "0.9.1",
        "pinned": true,
        "sessions": [
          {
            "id": "s1",
            "viewers": 1,
            "lastActive": 1763300000000
          }
        ]
      }
    ],
    "refresh": {
      "pid": 4322,
      "restarts": 0
    }
  }
}
```

5. Add to `cmd/marshal/daemon_test.go`:

```go
// TestTheStatusDocumentIsCurrent holds the JSON the tray app reads to the
// example checked in beside the Flutter tests. A field added to the Go
// document, dropped from it, or renamed fails here; regenerate with -update.
func TestTheStatusDocumentIsCurrent(t *testing.T) {
	const path = "../../test/contract/daemon-status.json"

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var status daemon.Status
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	got, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	got = append(got, '\n')

	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	if string(got) != string(raw) {
		t.Errorf("%s is stale; regenerate with\n  go test ./cmd/marshal -run TestTheStatusDocumentIsCurrent -update", path)
	}
}
```

   Declare the flag once in the same file's package:

```go
var updateGolden = flag.Bool("update", false, "rewrite the checked-in fixtures")
```

6. Add two command tests, both through `fakeDaemonSocket`:

   - `TestDaemonStatusJSONExitsZeroWhenSilent`: a client whose `Info`
     returns an error; assert `status --json` exits 0 and the decoded
     document has `"answering": false` and no `daemon` key.
   - `TestDaemonStatusJSONCarriesTheDaemon`: an answering client; assert
     `schemaVersion` is 1 and `daemon.version` matches the client's.

**Verify.**

- `scripts/test.sh quick ./internal/daemon ./cmd/marshal -run 'TestTheStatusDocumentIsCurrent|TestDaemonStatus'`
  — passes, and `git diff --exit-code test/contract/daemon-status.json`
  prints nothing.
- `CGO_ENABLED=1 go build ./cmd/marshal && ./marshal daemon status --json | python3 -m json.tool`
  — valid JSON, exit 0, even with no daemon installed.
- `gofmt -l internal/daemon cmd/marshal` — prints nothing.

---

## Task 2 — the Flutter project

**Goal.** `app/` is a Flutter desktop project whose pure Dart parts need no
plugin and no display.

**Files.** `app/` (new), `app/pubspec.yaml`, `app/analysis_options.yaml`,
`app/README.md`.

**Steps.**

1. `cd app` after scaffolding with
   `flutter create --org dev.marshal --project-name marshal_app --platforms=macos,linux .`
   from an empty `app/` directory.
2. Replace `pubspec.yaml`'s dependency and flutter sections with:

```yaml
environment:
  sdk: ^3.5.0

dependencies:
  flutter:
    sdk: flutter
  tray_manager: ^0.2.4
  launch_at_startup: ^0.3.3

dev_dependencies:
  flutter_test:
    sdk: flutter
  flutter_lints: ^4.0.0

flutter:
  uses-material-design: true
  assets:
    - assets/
```

   Run `flutter pub add tray_manager launch_at_startup` and keep whatever
   version constraints it resolves; record them in the spec's
   implementation record. The numbers above are the floor, not a claim
   about the latest.
3. Create `app/assets/tray.png`: a 22×22 black-on-transparent template
   image. On macOS the tray sets it as a template image, so macOS themes it;
   a coloured icon looks wrong in dark mode.
4. Replace `app/README.md` with two paragraphs: what the app is (the tray
   screen of Marshal's native app), and the two commands that run it
   (`flutter pub get`, `flutter run -d macos`).

**Verify.**

- `flutter pub get` — resolves.
- `flutter analyze` — no issues.
- `flutter build linux --debug` or `flutter build macos --debug` on the
  matching host — succeeds. CI builds the release bundles in Task 7.

---

## Task 3 — the CLI seam and the status models

**Goal.** The app can read a status document, and only one file touches the
system.

**Files.** `app/lib/src/daemon/status.dart`,
`app/lib/src/daemon/cli.dart`, `app/test/status_test.dart`.

**Steps.**

1. Create `app/lib/src/daemon/status.dart` with these classes and
   `fromJson` factories, all in pure Dart — no `dart:io`, no
   `package:flutter`:

```dart
class DaemonStatus {
  const DaemonStatus({
    required this.schemaVersion,
    required this.answering,
    required this.service,
    required this.linger,
    this.daemon,
  });

  static const supportedSchema = 1;

  final int schemaVersion;
  final bool answering;
  final ServiceState service;
  final LingerState linger;
  final DaemonInfo? daemon;

  factory DaemonStatus.fromJson(Map<String, dynamic> json) {
    final version = json['schemaVersion'] as int? ?? 0;
    if (version != supportedSchema) {
      throw FormatException(
        'daemon status schema $version, this app speaks $supportedSchema',
      );
    }
    final daemon = json['daemon'];
    return DaemonStatus(
      schemaVersion: version,
      answering: json['answering'] as bool? ?? false,
      service: ServiceState.fromJson(
        (json['service'] as Map<String, dynamic>?) ?? const {},
      ),
      linger: LingerState.fromJson(
        (json['linger'] as Map<String, dynamic>?) ?? const {},
      ),
      daemon: daemon == null
          ? null
          : DaemonInfo.fromJson(daemon as Map<String, dynamic>),
    );
  }
}

class ServiceState {
  const ServiceState({
    required this.installed,
    required this.running,
    required this.pid,
  });

  final bool installed;
  final bool running;
  final int pid;

  factory ServiceState.fromJson(Map<String, dynamic> json) => ServiceState(
        installed: json['installed'] as bool? ?? false,
        running: json['running'] as bool? ?? false,
        pid: json['pid'] as int? ?? 0,
      );
}

class LingerState {
  const LingerState({required this.supported, required this.enabled});

  final bool supported;
  final bool enabled;

  factory LingerState.fromJson(Map<String, dynamic> json) => LingerState(
        supported: json['supported'] as bool? ?? false,
        enabled: json['enabled'] as bool? ?? false,
      );
}

class DaemonInfo {
  const DaemonInfo({
    required this.version,
    required this.projects,
    required this.children,
  });

  final String version;
  final List<String> projects;
  final List<DaemonChild> children;

  factory DaemonInfo.fromJson(Map<String, dynamic> json) => DaemonInfo(
        version: json['version'] as String? ?? '',
        projects:
            ((json['projects'] as List<dynamic>?) ?? const []).cast<String>(),
        children: ((json['children'] as List<dynamic>?) ?? const [])
            .map((e) => DaemonChild.fromJson(e as Map<String, dynamic>))
            .toList(),
      );
}

class DaemonChild {
  const DaemonChild({
    required this.root,
    required this.version,
    required this.pinned,
  });

  final String root;
  final String version;
  final bool pinned;

  factory DaemonChild.fromJson(Map<String, dynamic> json) => DaemonChild(
        root: json['root'] as String? ?? '',
        version: json['version'] as String? ?? '',
        pinned: json['pinned'] as bool? ?? false,
      );
}
```

2. Create `app/lib/src/daemon/cli.dart`. It is the **only** file under
   `lib/` allowed to import `dart:io`:

```dart
import 'dart:convert';
import 'dart:io';

import 'status.dart';

/// DaemonCli runs the marshal binary the app ships. Everything the app
/// knows about Marshal comes through it: the app never opens the daemon's
/// socket and never reads its files.
class DaemonCli {
  DaemonCli({String? binary}) : _binary = binary;

  final String? _binary;

  String get binary => _binary ?? _locate();

  /// _locate prefers the binary inside the app bundle, then a developer's
  /// own install, and finally PATH.
  String _locate() {
    final exe = File(Platform.resolvedExecutable).parent;
    final candidates = <String>[
      if (Platform.isMacOS)
        '${exe.parent.path}/Resources/bin/marshal',
      if (Platform.isLinux) '${exe.path}/bin/marshal',
      '/usr/local/bin/marshal',
      '${Platform.environment['HOME'] ?? ''}/.local/bin/marshal',
    ];
    for (final path in candidates) {
      if (File(path).existsSync()) return path;
    }
    return 'marshal';
  }

  Future<DaemonStatus> status() async {
    final result = await Process.run(binary, const ['daemon', 'status', '--json']);
    final out = (result.stdout as String).trim();
    if (out.isEmpty) {
      final err = (result.stderr as String).trim();
      throw DaemonException(
        err.isEmpty ? 'marshal daemon status said nothing' : err,
      );
    }
    return DaemonStatus.fromJson(jsonDecode(out) as Map<String, dynamic>);
  }

  Future<void> run(List<String> args) async {
    final result = await Process.run(binary, args);
    if (result.exitCode != 0) {
      final err = (result.stderr as String).trim();
      throw DaemonException(
        err.isEmpty ? 'marshal ${args.join(' ')} exited ${result.exitCode}' : err,
      );
    }
  }

  Future<void> open(String target) async {
    final opener = Platform.isMacOS ? 'open' : 'xdg-open';
    await Process.run(opener, [target]);
  }
}

class DaemonException implements Exception {
  DaemonException(this.message);

  final String message;

  @override
  String toString() => message;
}
```

3. Create `app/test/status_test.dart`:

   - `parses the shared fixture`: reads
     `../test/contract/daemon-status.json` with `dart:io`, decodes it, and
     asserts schema 1, `answering`, `service.pid == 1234`,
     `linger.enabled`, and one child whose `pinned` is true.
   - `refuses an unknown schema`: `{"schemaVersion": 2}` throws
     `FormatException`.
   - `tolerates a silent daemon`: `{"schemaVersion": 1, "answering": false,
     "service": {"installed": true}}` parses with `daemon == null` and
     `linger.supported == false`.

**Verify.**

- `cd app && flutter test test/status_test.dart` — passes.
- `cd app && flutter analyze` — no issues.

---

## Task 4 — the menu model, the renderer, and the app

**Goal.** The tray shows a menu built from one status, and every plugin call
lives in one file.

**Files.** `app/lib/src/menu/model.dart`, `app/lib/src/menu/host.dart`,
`app/lib/src/menu/render.dart`, `app/lib/main.dart`,
`app/test/menu_model_test.dart`.

**Steps.**

1. Create `app/lib/src/menu/model.dart` in pure Dart:

```dart
import '../daemon/status.dart';

/// TrayItem is one clickable row. id is what the app switches on.
class TrayItem {
  const TrayItem({
    required this.id,
    required this.label,
    this.enabled = true,
    this.checked = false,
  });

  final String id;
  final String label;
  final bool enabled;
  final bool checked;
}

/// TraySeparator draws a dividing line.
class TraySeparator {
  const TraySeparator();
}

/// TrayMenu is the menu as data, so it can be tested without a tray.
class TrayMenu {
  const TrayMenu({required this.title, required this.entries});

  final String title;
  final List<Object> entries;
}

/// menuFor turns one status into the menu. A null status with a failure is
/// the app's own failure; a null status without one means "not read yet".
TrayMenu menuFor({
  required String appVersion,
  required bool logExists,
  required bool launchAtLogin,
  DaemonStatus? status,
  String? failure,
}) {
  final entries = <Object>[
    TrayItem(id: 'status', label: _statusLine(appVersion, status, failure), enabled: false),
    const TraySeparator(),
    const TrayItem(id: 'fleet', label: 'Open Fleet…'),
  ];

  if (status != null) {
    if (!status.service.installed) {
      entries.add(const TrayItem(id: 'install', label: 'Install Marshal Daemon…'));
    } else if (status.service.running) {
      entries.add(const TrayItem(id: 'stop', label: 'Stop Daemon'));
    } else {
      entries.add(const TrayItem(id: 'start', label: 'Start Daemon'));
    }
  }

  entries
    ..add(const TraySeparator())
    ..add(TrayItem(id: 'log', label: 'Open Log…', enabled: logExists))
    ..add(TrayItem(id: 'login', label: 'Launch at Login', checked: launchAtLogin))
    ..add(const TraySeparator())
    ..add(const TrayItem(id: 'quit', label: 'Quit'));

  return TrayMenu(title: 'Marshal', entries: entries);
}

String _statusLine(String appVersion, DaemonStatus? status, String? failure) {
  if (failure != null) return 'Marshal — status unavailable';
  if (status == null) return 'Marshal — starting…';
  if (!status.service.installed) return 'Marshal — not installed';
  final daemon = status.daemon;
  if (daemon == null) return 'Marshal — daemon stopped';
  final projects = daemon.projects.length;
  final noun = projects == 1 ? 'project' : 'projects';
  final line = 'Marshal — running (pid ${status.service.pid}) · $projects $noun';
  if (daemon.version.isNotEmpty && daemon.version != appVersion) {
    return '$line · daemon ${daemon.version} (app $appVersion)';
  }
  return line;
}
```

2. Create `app/lib/src/menu/host.dart`:

```dart
import 'model.dart';

/// TrayHost is the shell the app draws through. Keeping it an interface is
/// what lets the menu model be tested without a tray, and lets the eventual
/// windowed app replace the renderer.
abstract class TrayHost {
  Future<void> show(TrayMenu menu);
  Future<void> notify(String message);
}
```

3. Create `app/lib/src/menu/render.dart`. It implements `TrayHost` over
   `tray_manager`, and is the only file allowed to import that plugin.
   Check the pinned plugin's own README for the exact calls, then:

   - initialise the tray once with `trayManager.setIcon` on
     `assets/tray.png`, and on macOS a template image
     (`trayManager.setIcon(..., isTemplate: true)` or the version's
     equivalent);
   - translate `TrayMenu.entries` into the plugin's `Menu`/`MenuItem`
     types, mapping `TraySeparator` to the plugin's separator item,
     `TrayItem.checked` to its checkbox item type, and `TrayItem.enabled`
     to its disabled item type;
   - route a click to a `void Function(String id)` the app supplies;
   - implement `notify` with a platform notification only if the pinned
     version has one; otherwise write the message to the app's log and
     return.

   The `TrayItem` ids the click handler must handle are exactly:
   `fleet`, `install`, `start`, `stop`, `log`, `login`, `quit`.

4. Create `app/lib/main.dart`:

   - Build `DaemonCli` and `RenderHost` once.
   - Poll `cli.status()` every 5 seconds, and immediately after every
     action.
   - Build the menu with
     `menuFor(appVersion: const String.fromEnvironment('APP_VERSION', defaultValue: 'dev'), logExists: File(logPath).existsSync(), launchAtLogin: <state>, status: <last good>, failure: <last error>)`,
     then `host.show(menu)`.
   - The log path is `daemon.Paths.Log`: `$MARSHAL_DATA_DIR` (or
     `$XDG_DATA_HOME/marshal`, or `~/.local/share/marshal`) +
     `/daemon/webbridge.log`.
   - Click handling:
     `fleet` → `cli.run(['fleet'])`; `install` → `cli.run(['daemon', 'install'])`;
     `start` → `cli.run(['daemon', 'start'])`;
     `stop` → `cli.run(['daemon', 'stop'])`;
     `log` → `cli.open(logPath)`; `login` → toggle through
     `launch_at_startup` and re-read it; `quit` → exit.
   - A `DaemonException` from any action is caught, shown with
     `host.notify`, and the menu keeps its last good status with
     `failure` set.
   - No window is created. Do not call `runApp` with a `MaterialApp`; call
     `WidgetsFlutterBinding.ensureInitialized()` and let the tray own the
     lifecycle.

5. Create `app/test/menu_model_test.dart` with a fake `TrayHost` recording
   `show` calls, and these cases:
   - `offers install when the service is missing`;
   - `offers stop when it is running, and start when it is stopped`;
   - `names the version skew` when `daemon.version` differs from the app
     version;
   - `disables the log item when the file is missing`;
   - `keeps the last status when a read fails` — pass `status` and
     `failure` together and assert the status item still names the daemon.

**Verify.**

- `cd app && flutter test` — passes.
- `cd app && flutter analyze` — no issues.
- `cd app && grep -rn 'dart:io' lib | grep -v 'lib/src/daemon/cli.dart'` —
  prints nothing.
- On a desktop: `flutter run -d macos` (or `-d linux`) shows a tray icon
  whose menu lists the state the terminal's
  `marshal daemon status` reports.

---

## Task 5 — bundle identity on macOS

**Goal.** The app runs as a menu-bar item with no dock icon and a name that
does not shadow `marshal`.

**Files.** `app/macos/Runner/Info.plist`,
`app/macos/Runner/Configs/AppInfo.xcconfig`.

**Steps.**

1. In `Info.plist`, add `LSUIElement` as `<true/>`.
2. In `AppInfo.xcconfig`, set `PRODUCT_NAME = Marshal` and
   `PRODUCT_BUNDLE_IDENTIFIER = dev.marshal.app`; the executable stays
   `marshal_app` so it never shadows `marshal` on `PATH`.

**Verify.**

- `flutter build macos --debug` — succeeds.
- `plutil -p build/macos/Build/Products/Debug/Marshal.app/Contents/Info.plist | grep LSUIElement`
  — prints `true`.
- Launching the built app shows no Dock icon and a menu-bar item.

---

## Task 6 — CI runs the Dart tests

**Goal.** A PR that breaks the app fails before a tag.

**Files.** `.github/workflows/go-checks.yml`.

**Steps.**

1. Add a job:

```yaml
  app:
    name: Tray app tests
    runs-on: ubuntu-latest
    timeout-minutes: 20
    defaults:
      run:
        working-directory: app
    steps:
      - uses: actions/checkout@v4
      - uses: subosito/flutter-action@v2
        with:
          channel: stable
          flutter-version: '3.35.1'
          cache: true
      - run: flutter pub get
      - run: flutter analyze
      - run: flutter test
```

2. Replace `3.35.1` with the exact stable version chosen when this task
   runs (`flutter --version` on the machine that implements it), and record
   it in the spec's implementation record. `flutter pub get` in CI must then
   match `app/pubspec.lock`, which is committed.

**Verify.**

- `python3 -c 'import yaml; yaml.safe_load(open(".github/workflows/go-checks.yml"))'`
  — parses.
- Locally: `cd app && flutter analyze && flutter test` — both clean.

---

## Task 7 — release the app bundle

**Goal.** A tag produces a signed, notarized macOS app and a Linux tarball,
each containing the daemon binaries.

**Files.** `.github/workflows/release.yaml`.

**Steps.**

1. Add a `app-darwin` job with `needs: release`, `permissions: contents:
   write`, `runs-on: macos-latest`:
   - `gh release download "$GITHUB_REF_NAME" --pattern '*darwin*' --dir dist`
   - extract `marshal` and `webbridge` for the runner's arch into
     `app-bins/`
   - `flutter build macos --release`
   - copy both binaries into
     `build/macos/Build/Products/Release/Marshal.app/Contents/Resources/bin/`
   - sign **inside-out**: `codesign --force --options runtime
     --timestamp --sign "$MACOS_SIGN_IDENTITY"` on each bundled binary
     first, then on `Marshal.app`
   - `ditto -c -k --keepParent Marshal.app Marshal.zip`,
     `xcrun notarytool submit Marshal.zip --keychain-profile "$NOTARY_PROFILE" --wait`,
     `xcrun stapler staple Marshal.app`, and `hdiutil create` a `.dmg`
   - `gh release upload` the `.dmg`
   - when `MACOS_SIGN_IDENTITY` is unset: build, run `flutter test`, upload
     nothing, and `echo "unsigned: not uploaded"`.
2. Add an `app-linux` job with `needs: release`, `runs-on: ubuntu-latest`:
   - `sudo apt-get install -y clang cmake ninja-build pkg-config
     libgtk-3-dev libayatana-appindicator3-dev`
   - `gh release download "$GITHUB_REF_NAME" --pattern '*linux*' --dir dist`
   - extract `marshal` and `webbridge` for the runner's arch
   - `flutter build linux --release`
   - copy both binaries into
     `build/linux/x64/release/bundle/bin/`
   - `tar -czf marshal-app_${GITHUB_REF_NAME}_linux_x64.tar.gz -C
     build/linux/x64/release bundle`
   - `gh release upload` the tarball.
3. Pass `APP_VERSION: ${{ github.ref_name }}` to both `flutter build`
   invocations so the menu can name the app's version.

**Verify.**

- The workflow YAML parses (`python3 -c 'import yaml; ...'`).
- After a tag: `gh release view "$TAG"` lists `Marshal.dmg` and the Linux
  tarball; `spctl --assess --type execute -vv Marshal.app` says
  `accepted`; `codesign -dv --verbose=2 Marshal.app/Contents/Resources/bin/marshal`
  names the Developer ID.
- Downloaded on macOS with a browser, the app launches with no Gatekeeper
  prompt and its menu's "Install Marshal Daemon…" produces an answering
  daemon (`marshal daemon status`).

---

## Task 8 — record the outcome and verify the whole tree

**Goal.** The spec records the pinned Flutter and plugin versions, the
fixture, and the manual checks.

**Files.** `docs/distribution/d3-tray-app.md`, `AGENTS.md`.

**Steps.**

1. Append an `## Implementation record` section to the D3 spec: the pinned
   Flutter version, the resolved `tray_manager`/`launch_at_startup`
   versions, the plugin calls the renderer actually uses, and anything this
   plan got short.
2. Add `app/` to `AGENTS.md`'s map, under a new "Native app" line, with the
   boundary rule: pure Dart except `lib/src/daemon/cli.dart`, and no socket.
3. Run the repository's gates.

**Verify.**

- `gofmt -l internal cmd test web` — prints nothing.
- `CGO_ENABLED=1 go vet ./...` — clean.
- `scripts/test.sh full` — passes.
- `cd app && flutter analyze && flutter test` — clean.
- Manual: the three checks in Task 7's last bullet, plus a Linux run with
  `libayatana-appindicator3` present showing an icon in KDE or XFCE.

## Done when

- `marshal daemon status --json` is versioned, always exits 0 with `--json`,
  and is pinned by a fixture both languages read.
- The tray shows the daemon's state and offers install/start/stop, the
  fleet, the log, launch-at-login and quit.
- The app installs the daemon it bundles, offline.
- A tag produces a notarized macOS app and a Linux tarball, or a Linux
  tarball and a CI artifact explaining why the macOS one is unsigned.
