import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

/// What the app asks of its window (findings/tray-and-window.md): show it,
/// hide it, and — only when the person chose Quit — quit.
///
/// It is an interface so the tray's rules can be tested without a window, and
/// so the two runners can answer it however their platform allows.
abstract class WindowHost {
  /// Opens the window, or focuses it when it is already open.
  Future<void> show();

  /// Hides the window and leaves the app running.
  Future<void> hide();

  /// Ends the process.
  Future<void> quit();
}

/// The real window: the `marshal/window` method channel, answered by the
/// macOS Runner's `MainFlutterWindow` and by the Linux runner's application.
///
/// No window plugin is used: the Runner already owns the window, and the
/// finding's forty lines on each side keep it that way.
class ChannelWindowHost implements WindowHost {
  const ChannelWindowHost();

  /// The channel both runners answer.
  static const channel = MethodChannel('marshal/window');

  @override
  Future<void> show() => _invoke('show');

  @override
  Future<void> hide() => _invoke('hide');

  @override
  Future<void> quit() => _invoke('quit');

  /// A host without the channel — the browser, a widget test — has no window
  /// for the app to act on, and asking is not an error worth showing anyone.
  Future<void> _invoke(String method) async {
    try {
      await channel.invokeMethod<void>(method);
    } on MissingPluginException {
      return;
    } on PlatformException {
      return;
    }
  }
}

/// The app's one window, as the app sees it.
///
/// Closing the window hides it: the app is a menu-bar item, and only Quit
/// quits (finding: macOS step 3, Linux step 3). So [closed] — what the red
/// button, Cmd-W and the Linux close button mean — hides, and there is no
/// close that quits.
class WindowController extends ChangeNotifier {
  WindowController(this._host);

  final WindowHost _host;
  bool _open = false;

  /// Whether a person has the window in front of them.
  bool get open => _open;

  /// Opens the window, or focuses it when it is already open: one call for
  /// both, so the tray item does not need to know which it is.
  Future<void> show() async {
    await _host.show();
    _set(open: true);
  }

  /// Hides the window and leaves the app running.
  Future<void> hide() async {
    await _host.hide();
    _set(open: false);
  }

  /// The window was closed. The app hides, and does not quit.
  Future<void> closed() => hide();

  /// Quits, and only from the menu's Quit.
  Future<void> quit() => _host.quit();

  void _set({required bool open}) {
    if (_open == open) return;
    _open = open;
    notifyListeners();
  }
}
