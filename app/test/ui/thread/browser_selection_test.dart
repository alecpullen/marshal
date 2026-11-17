@TestOn('browser')
library;

import 'package:flutter/gestures.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';

import 'support.dart';

/// A drag across the transcript selects and copies in a browser (f3 plan,
/// Task 5's `drag selects in a browser`): the pointer events arrive through
/// the browser's own event loop and the clipboard is the platform channel's,
/// which is the half of selection the VM run cannot cover.
///
/// `flutter test` on the VM skips this file; it runs with
///
/// ```
/// flutter test --platform chrome test/ui/thread/browser_selection_test.dart
/// ```
///
/// The environment this phase was built in has no working browser (the Chrome
/// host stalls on CanvasKit), so it is written and has not run — recorded as
/// environment-blocked, not as a failure, in
/// `docs/studio-flutter/f3-agent-thread.md`.
void main() {
  testWidgets('drag selects in a browser', (tester) async {
    String? clipboard;
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(SystemChannels.platform, (call) async {
          if (call.method == 'Clipboard.setData') {
            clipboard = (call.arguments as Map)['text'] as String?;
          }
          return null;
        });
    addTearDown(() {
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(SystemChannels.platform, null);
    });

    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    final from = tester.getCenter(
      find.text('Clean up the test fixtures nobody uses any more.'),
    );
    final to = tester.getCenter(find.text('Step number 2 mapped the fixtures'));
    final gesture = await tester.startGesture(
      from,
      kind: PointerDeviceKind.mouse,
    );
    await tester.pump();
    await gesture.moveTo(to);
    await tester.pump();
    await gesture.up();
    await tester.pump();

    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyC);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump();

    // The drag starts mid-message and ends inside a later step, so the copy
    // is the message's tail and the rows between, in the order they are drawn.
    expect(clipboard, isNotNull);
    expect(clipboard, startsWith('s nobody uses any more.'));
    expect(clipboard, contains('mapped the fixtures'), reason: clipboard);

    store.dispose();
    view.dispose();
  });
}
