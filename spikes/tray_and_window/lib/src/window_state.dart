/// WindowState is the pure rule: the Dock icon is present exactly while a
/// Studio window is shown. The Runner applies it; this class is what the
/// Dart side reasons with and what the unit test pins.
enum ActivationPolicy { accessory, regular }

class WindowState {
  const WindowState({this.windowShown = false});

  final bool windowShown;

  ActivationPolicy get policy =>
      windowShown ? ActivationPolicy.regular : ActivationPolicy.accessory;

  /// A tray "Open Studio" opens when hidden and focuses when shown; either
  /// way the result is shown.
  WindowState open() => const WindowState(windowShown: true);

  /// Closing the window (close button, Cmd-W) hides it. Never quits.
  WindowState close() => const WindowState(windowShown: false);

  /// The app quits only through the tray's Quit, never from a window event.
  bool get quitsOnLastWindowClose => false;
}
