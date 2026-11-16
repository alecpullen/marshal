# Tray and window: one app, a menu-bar item or a Dock app

F0 spike. Feeds F2 decisions 1 and 2 (`f2-shell-overview-inbox.md`).

## The question

How does one Flutter desktop app behave as a menu-bar/tray item with no Dock
icon while no window is open, and as a normal app with a Dock icon while a
Studio window is open, where closing the last window does not quit and a tray
item opens or focuses the window?

## Answer in one paragraph

**macOS: keep `LSUIElement = true` and switch `NSApp.activationPolicy`
between `.accessory` (no window) and `.regular` (window shown), from a small
method channel in the Runner. No plugin is needed.** Showing the window sets
`.regular`, orders the window front and activates the app; closing the window
(`windowShouldClose` returning false) orders it out and sets `.accessory`. The
window and its Flutter engine are never destroyed. **Linux: no policy exists;
the same shape is "hide, do not destroy" the one GTK window, plus
`gtk_window_present` to open or focus.** The Dock/taskbar entry follows window
visibility by itself there.

## What was verified by a real run, and what was not

Xcode became available mid-spike, so the macOS mechanism was built and run.

**VERIFIED on macOS (debug build, `flutter build macos --debug`, launched from
a script with a `--autotest` flag that ran the steps with no clicking; state
read with `lsappinfo` and `System Events`, plus the app's own log):**

| Step | Process type (`lsappinfo`) | Visible / windows |
|---|---|---|
| launch, window ordered out | `UIElement` | hidden, 0 windows |
| `showStudio()` | `Foreground`, in front | visible, 1 window |
| `performClose` (the path the red button and Cmd-W take) | `UIElement` | hidden, 0 windows |
| `showStudio()` again | `Foreground`, in front | visible, 1 window |
| `performClose` again | `UIElement` | hidden |
| `showStudio()` twice in a row (the "focus" path) | `Foreground`, one window | visible, 1 window |

So: `LSUIElement` plus `setActivationPolicy` does flip a running process
between accessory and Dock-resident repeatedly, closing the window does not
quit the app (it survived five transitions, and `applicationShouldTerminateAfterLastWindowClosed`
returns false), and the engine and the `tray_manager` plugin coexisted with it
in the same process. The `performClose` call goes through the same
`windowShouldClose` delegate as the red close button and Cmd-W, but a human
click was not performed. Also verified: `flutter analyze` is clean and the Dart
window-state unit test passes (`spikes/tray_and_window/test/window_state_test.dart`).

**NOT verified (needs a person at the screen):**
- That the menu-bar status item itself is drawn, shows the menu, and that
  clicking "Open Studio" reaches `show` (the channel and menu code are written
  and compile; the click was not made). `System Events` was denied assistive
  access, so no UI scripting was possible.
- The visual Dock icon appearing and disappearing (inferred from
  `Foreground` / `UIElement`, which is the Dock's own criterion, but not seen).
- A real red-button click and Cmd-W; that the app menu bar and keyboard
  focus are correct right after the policy switch; Cmd-Tab behaviour.
- Release/sandboxed/notarised builds (only a debug build was run).
- `Quit` from the tray: `SystemNavigator.pop()` in the spike is probably a
  no-op on macOS desktop; F2 should quit through a `quit` channel method that
  calls `NSApp.terminate(nil)` (the existing app already has `cli.quit()`).
- **All of Linux.** Nothing in this section about GTK was compiled or run.

## Mechanism for macOS (recommended)

1. `Info.plist` keeps `LSUIElement = true` (already present). The app starts
   as an accessory: no Dock icon, no app menu, only the status item.
2. `MainFlutterWindow` hides itself at launch (already done by the shipping
   Runner) and becomes its own `NSWindowDelegate`.
3. `windowShouldClose` calls `hideStudio()` and returns false: `orderOut(nil)`
   then `NSApp.setActivationPolicy(.accessory)`. The window is kept alive
   (`isReleasedWhenClosed = false`).
4. `showStudio()`: `setActivationPolicy(.regular)`, deminiaturize if needed,
   `makeKeyAndOrderFront`, `NSApp.activate(ignoringOtherApps: true)`. The same
   call opens a hidden window and focuses a visible one, so the tray item does
   not need to know the state.
5. A method channel `marshal/window` (`show`, `hide`, plus a `windowChanged`
   callback to Dart so Dart's `WindowState` stays right when the red button is
   used). Spike code: `spikes/tray_and_window/macos/Runner/MainFlutterWindow.swift`,
   `lib/src/window_channel.dart`, `lib/src/window_state.dart`.
6. `applicationShouldTerminateAfterLastWindowClosed` stays false (already so).
   `applicationShouldHandleReopen` shows the window if the user reopens a
   Dock-resident app with only a minimised window.

Caution (not exercised): after returning to `.accessory`, a later switch to
`.regular` on some macOS versions needs `activate` called after the policy
change; the spike's order (policy, then order front, then activate) worked on
this machine's OS, which is the only version tested.

## Mechanism for Linux (recommended, unverified)

Linux has no activation policy; the taskbar/dock entry exists exactly while a
mapped window does, which gives the required behaviour for free.

**Finding that corrects D3:** `docs/distribution/d3-tray-app.md` says "Linux
creates none" (no window). The checked-in `app/linux/runner/my_application.cc`
is the unmodified Flutter template: it creates a `GtkApplicationWindow` in
`activate` and shows it on the first Flutter frame (`first_frame_cb`). Today
the Linux app therefore opens a window at launch. F2 must change that file, not
merely add to it.

Changes for `my_application.cc`:
- Do not show the window in `first_frame_cb`. Keep `gtk_widget_realize(view)`
  so the engine runs and renders while hidden (unverified that frames are
  produced when the toplevel is never mapped; if not, show it off-screen once
  then hide, or `gtk_window_iconify`).
- Connect `delete-event` on the window to a handler that calls
  `gtk_widget_hide` and returns TRUE (closing hides, never destroys).
- Call `g_application_hold(application)` once in `activate` so the process
  survives with no visible window regardless of GtkApplication's window rules.
- Register an `FlMethodChannel` `marshal/window` whose `show` calls
  `gtk_window_present` (open and focus in one call; on Wayland, focus stealing
  may be denied by the compositor and the window is then only marked as
  needing attention) and whose `hide` calls `gtk_widget_hide`.
- Tray: `tray_manager` uses libayatana-appindicator (already a dependency per
  D3). A bare GNOME session shows no status icon without the AppIndicator
  extension, as D3 already records; that limitation matters more once the
  tray is the only way to open Studio, so F2 should also expose a way to open
  Studio from a second route on Linux (for example the `marshal` command
  starting `marshal-app --show`, or a `.desktop` launcher whose activation
  shows the window; the `G_APPLICATION_NON_UNIQUE` flag in the template would
  need to change to deliver a second launch to the running instance).

## Alternatives considered

- **`window_manager` plugin (0.7.0 resolves with Flutter 3.50 here).** Offers
  `hide`/`show`/`focus`, `setPreventClose` plus an `onWindowClose` callback,
  and `setSkipTaskbar`, which, from memory of its macOS implementation (not
  re-read; the package's native sources were not fetched), toggles the same
  activation policy. It works, but it takes over the window's delegate and
  styling, adds a dependency and a transitive `screen_retriever`, and the
  Runner already needs its own code (the existing `launch_at_startup` channel
  lives in `MainFlutterWindow.swift`). About forty lines of Swift do the whole
  job, so the plugin is not worth it. Revisit only if F2 needs window geometry
  persistence, minimum sizes or a custom title bar, where the plugin earns its
  place.
- **Stay `.accessory` and show a window anyway.** Accessory apps can show
  windows, but there is no Dock icon, no Cmd-Tab entry and no app menu, so it
  does not meet "a normal app while a window is open". Rejected.
- **Drop `LSUIElement` and go `.accessory` at launch in code.** Briefly shows a
  Dock icon bounce at launch. Keeping `LSUIElement` avoids it. Rejected.
- **A second process for the window.** Doubles the footprint and splits the
  state the tray already holds. Rejected.
- **`tray_manager`'s own window handling:** none; it only draws the status item.

## Changes F2 would make

Described in prose; none done here and nothing under `app/` was changed.

- `app/macos/Runner/MainFlutterWindow.swift`: add the delegate, `showStudio`,
  `hideStudio` and the `marshal/window` channel beside the existing
  `launch_at_startup` handler (copy from the spike, minus the `--autotest`
  hook, which must not ship). Add a `quit` method calling `NSApp.terminate`.
- `app/macos/Runner/AppDelegate.swift`: keep `applicationShouldTerminateAfterLastWindowClosed`
  false; add `applicationShouldHandleReopen`.
- `app/macos/Runner/Info.plist`: no change (`LSUIElement` stays).
- `app/linux/runner/my_application.cc`: the changes listed above.
- `app/lib/src/window/`: a `WindowState` and a `WindowChannel` (spike
  versions are the starting point) as the one place Dart touches the window;
  `main.dart` calls `show` on first "Open Studio". Keep the `dart:io` rule:
  the channel is `package:flutter/services.dart`, not `dart:io`, so
  `app/test/no_io_test.dart` is unaffected.
- `app/lib/main.dart` and `lib/src/menu/model.dart`: add the "Open Studio"
  item; the `TrayHost` seam stays. The browser build must not import
  `tray_manager` (F2 decision 2: no tray code compiled in), so tray and window
  wiring sit behind a conditional import.
- Tests: the pure `WindowState` rule gets a Dart unit test; the native side is
  covered only by the manual steps below.

## Manual verification (a person with a display)

Build and run the spike (it needs nothing from `app/`):

```
cd spikes/tray_and_window
flutter build macos --debug
open build/macos/Build/Products/Debug/tray_and_window.app
```

1. A small black square appears in the menu bar. No Dock icon, no window.
   (Check: `lsappinfo info -only applicationType -pid <pid>` says `UIElement`.)
2. Click the square, choose "Open Studio". A window with "Studio window"
   opens and a Dock icon appears; the menu bar shows the app's menus; the
   window has keyboard focus. `lsappinfo` now says `Foreground`.
3. Click the red button (or Cmd-W). The window goes away, the Dock icon goes
   away, the status item stays. The app must not quit.
4. Repeat 2 and 3 three times; each time the Dock icon must come and go.
5. With the window open, click "Open Studio" again: it must focus, not open a
   second window. Minimise the window, choose "Open Studio": it restores.
6. Cmd-Tab with the window open lists the app; with it closed, does not.
7. Choose "Quit". (In the spike this calls `SystemNavigator.pop`, which may do
   nothing; kill the process if so. That is the known gap, not a mechanism
   failure.)

The automated variant, `open ... --args --autotest` is replaced by running the
binary directly: `.../Contents/MacOS/tray_and_window --autotest`; it logs
`SPIKE ...` lines and cycles show/close at 4, 10, 16, 22 and 28 seconds.

Linux (after F2 changes `my_application.cc`): on a GNOME-with-AppIndicator or
KDE session, launch `marshal-app`. Expect a tray icon and no window and no
taskbar entry; "Open Studio" opens one window with a taskbar entry; the
window's close button hides it and the tray icon remains; "Open Studio" again
re-shows the same window; on Wayland and X11 both. Anything else, in
particular a window at launch or the process exiting on close, means the
changes above are incomplete.

## Spike files

`spikes/tray_and_window/` is a `flutter create --platforms=macos,linux`
project with `tray_manager` ^0.5.3 and `window_manager` 0.7.0 resolved (the
latter only to check that it resolves; it is not used by the code). Throwaway:
nothing there is meant to ship. The Linux runner in it is the unmodified
template.
