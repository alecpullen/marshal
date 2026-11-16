/// What a browser build does instead of starting a tray: nothing.
///
/// The Studio is the page there — there is no menu-bar item, no window to
/// open and no login item — so the tray's code is not compiled in at all
/// (F2 decision 2; the finding's conditional import).
library;

/// Starts the menu-bar item on a platform that has one. A browser has none.
Future<void> startTray() async {}
