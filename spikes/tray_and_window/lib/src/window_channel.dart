import 'package:flutter/services.dart';

import 'window_state.dart';

/// WindowChannel is the whole native surface: show, hide, and a callback when
/// the user closes the window with the red button.
class WindowChannel {
  WindowChannel({void Function(WindowState)? onChanged}) {
    _channel.setMethodCallHandler((call) async {
      if (call.method == 'windowChanged') {
        state = WindowState(windowShown: call.arguments as bool);
        onChanged?.call(state);
      }
    });
  }

  static const _channel = MethodChannel('marshal/window');
  WindowState state = const WindowState();

  Future<void> show() => _channel.invokeMethod('show');
  Future<void> hide() => _channel.invokeMethod('hide');
  Future<String?> describe() => _channel.invokeMethod<String>('state');
}
