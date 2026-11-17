import 'package:flutter_test/flutter_test.dart';
import 'package:tray_and_window/src/window_state.dart';

void main() {
  test('accessory until a window is shown, regular while shown', () {
    var s = const WindowState();
    expect(s.policy, ActivationPolicy.accessory);
    s = s.open();
    expect(s.policy, ActivationPolicy.regular);
    expect(s.open().policy, ActivationPolicy.regular, reason: 'focus, not toggle');
    s = s.close();
    expect(s.policy, ActivationPolicy.accessory);
    expect(s.quitsOnLastWindowClose, isFalse);
  });
}
