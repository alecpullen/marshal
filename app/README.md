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
