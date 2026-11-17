# marshal_app

Marshal's native app. What exists today is its tray screen: a menu-bar item
whose menu reports the local daemon's state — not installed, running with its
pid, or stopped — and offers that daemon's own commands, `Open Fleet…`, the
daemon's log, launch-at-login and quit. It opens no window, never opens the
daemon's socket and never reads its files: everything it knows comes from
`marshal daemon status --json` and the commands it runs, so the app works
against the same daemon the terminal does and cannot drift from it.

The Studio is being built in this same project
([`docs/studio-flutter/`](../docs/studio-flutter/programme.md)); until its
window ships, the tray is the only thing the app runs.

## Layout and the boundary rule

```
lib/main.dart          the tray's entry point
lib/src/daemon/        the tray's CLI path: cli.dart runs marshal, status.dart parses it
lib/src/menu/          the tray's menu model, its host interface and its renderer
lib/src/client/        the bridge client: the only code that opens a socket (from F1)
lib/src/model/         stores and view models; Flutter's foundation only (from F1)
lib/src/ui/            widgets: tokens.dart, the kit in kit/, the gallery in gallery/
```

`test/boundary_test.dart` enforces what each may import:

- `dart:io` only in `lib/src/daemon/cli.dart` and in files under
  `lib/src/client/` whose name ends `_io.dart`.
- Browser APIs (`dart:js_interop`, `package:web`, …) only in files under
  `lib/src/client/` whose name ends `_web.dart`.
- `lib/src/model/` imports neither Flutter nor `dart:io` nor a browser API.
- `lib/src/ui/` never imports `lib/src/client/`.
- Material and Cupertino (the `material_ui` and `cupertino_ui` packages; the
  SDK's own `material.dart` and `cupertino.dart` are deprecated and imported
  nowhere) only under `lib/src/ui/kit/`, in `lib/src/ui/tokens.dart`, and in
  the tray's existing files.
- The tray still learns everything from `marshal daemon status --json` and
  the commands in its menu. It could open the daemon's socket — Dart's
  `HttpClient` can, through `connectionFactory` — but keeps its CLI path by
  choice.

Anything under `lib/src/ui/` also follows
[the design system](../docs/studio-flutter/design-system.md). The kit's
gallery runs from its own entry, never from the shipped app:

```sh
flutter run -t lib/gallery_main.dart -d chrome     # or -d macos
flutter build web -t lib/gallery_main.dart --release --no-web-resources-cdn
```

`--no-web-resources-cdn` keeps the engine in the build, and
`web/flutter_bootstrap.js` points the engine's font fallback at the app's
own assets, so a web build requests nothing from any other host.

F1's connection check is a second debug entry, on the gallery's own entry
point: it lists the hosts this machine can reach — the local daemon, then each
server profile in `fleet.toml` — with each one's state, when it was last heard
from, and how many agents it runs. It is not in the shipped app.

```sh
flutter run -t lib/gallery_main.dart -d linux --dart-define=connection-check=true
```

`lib/src/client/` is the bridge client: the transport (a unix socket to the
local daemon, HTTPS with a bearer token to a server, or the browser's own
`fetch`), the typed routes, the fleet stream with its resume and backoff, and
the host registry. It is the only code in the app that opens a socket. The
generated contract types beside it come from `web/bridge/contract`; regenerate
them with `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.

The token tests in `test/client/transport_web_test.dart` run in a browser:

```sh
flutter test --platform chrome test/client/transport_web_test.dart
```

Two commands run it from a checkout (`flutter` must be on `PATH`):

```sh
flutter pub get
flutter run -d macos    # or: flutter run -d linux
```

Linux desktop builds need GTK3 and AppIndicator development headers
(`clang cmake ninja-build pkg-config libgtk-3-dev
libayatana-appindicator3-dev` on Debian/Ubuntu). `tool/generate_tray_icon.py`
regenerates `assets/tray.png`, a 22×22 black-on-transparent template image:
macOS themes the menu bar itself, so the icon carries no colour of its own.

## On macOS

The bundle is `Marshal.app`. `macos/Runner/Configs/AppInfo.xcconfig` sets
`PRODUCT_NAME = Marshal` and `PRODUCT_BUNDLE_IDENTIFIER = dev.marshal.app`,
and leaves `EXECUTABLE_NAME = marshal_app` so the binary inside the bundle is
never something that shadows `marshal` on `PATH`. `macos/Runner/Info.plist`
sets `LSUIElement`, which is what makes the app a menu-bar item with no Dock
icon and no window.

`Launch at Login` is answered by a `launch_at_startup` method channel in
`macos/Runner/MainFlutterWindow.swift`, over `ServiceManagement`'s
`SMAppService` — the API the plugin's own README reaches for via the
`LaunchAtLogin` Swift package. Adding that package in Xcode is therefore not
needed: nothing has to be fetched, and the channel reports the login item as
off with a reason on macOS 12 and earlier, which `main.dart` already handles.

Build and check it on a Mac (this cannot be done on Linux — `flutter build
macos` needs Xcode, and there is no substitute):

```sh
flutter build macos --debug
plutil -p build/macos/Build/Products/Debug/Marshal.app/Contents/Info.plist \
  | grep LSUIElement          # must print: "LSUIElement" => 1
open build/macos/Build/Products/Debug/Marshal.app
```

Launching it must show a menu-bar item and no Dock icon. Ticking
`Launch at Login` must add the app under System Settings → General → Login
Items, and survive a restart.
