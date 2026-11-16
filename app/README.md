# marshal_app

Marshal's native app. What exists today is its tray screen: a menu-bar item
whose menu reports the local daemon's state — not installed, running with its
pid, or stopped — and offers that daemon's own commands, `Open Fleet…`, the
daemon's log, launch-at-login and quit. It opens no window, never opens the
daemon's socket and never reads its files: everything it knows comes from
`marshal daemon status --json` and the commands it runs, so the app works
against the same daemon the terminal does and cannot drift from it.

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
