import 'model.dart';

/// TrayHost is the shell the app draws through. Keeping it an interface is
/// what lets the menu model be tested without a tray, and lets the eventual
/// windowed app replace the renderer.
abstract class TrayHost {
  Future<void> show(TrayMenu menu);

  /// notify reports a message the user may need to know — a daemon command
  /// that failed — without a window to put it in.
  Future<void> notify(String message);
}
