import 'package:flutter/gestures.dart';
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';

import 'support.dart';

/// What the last Clipboard.setData carried.
String? _clipboard;

void _captureClipboard() {
  TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
      .setMockMethodCallHandler(SystemChannels.platform, (call) async {
        if (call.method == 'Clipboard.setData') {
          _clipboard = (call.arguments as Map)['text'] as String?;
        }
        return null;
      });
  addTearDown(() {
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(SystemChannels.platform, null);
    _clipboard = null;
  });
}

Future<void> _copy(WidgetTester tester) async {
  await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
  await tester.sendKeyEvent(LogicalKeyboardKey.keyC);
  await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
  await tester.pump();
}

Offset _centreOf(WidgetTester tester, String text) =>
    tester.getCenter(find.text(text));

void main() {
  testWidgets('a drag across rows copies them in order', (tester) async {
    _captureClipboard();
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    final from = _centreOf(
      tester,
      'Clean up the test fixtures nobody uses any more.',
    );
    final to = _centreOf(tester, 'Step number 2 mapped the fixtures');
    final gesture = await tester.startGesture(
      from,
      kind: PointerDeviceKind.mouse,
    );
    await tester.pump();
    await gesture.moveTo(to);
    await tester.pump();
    await gesture.up();
    await tester.pump();

    await _copy(tester);

    expect(_clipboard, isNotNull);
    // The drag starts mid-message and ends inside a later step, so the copy
    // is the message's tail, then the rows between, in the order they are
    // drawn.
    expect(_clipboard, startsWith('s nobody uses any more.'));
    expect(_clipboard, contains('mapped the fixtures'), reason: _clipboard);

    store.dispose();
    view.dispose();
  });

  testWidgets('a selection survives rows arriving', (tester) async {
    _captureClipboard();
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    final from = _centreOf(
      tester,
      'Clean up the test fixtures nobody uses any more.',
    );
    final to = _centreOf(tester, 'Step number 1 mapped the fixtures');
    final gesture = await tester.startGesture(
      from,
      kind: PointerDeviceKind.mouse,
    );
    await tester.pump();
    await gesture.moveTo(to);
    await tester.pump();
    await gesture.up();
    await tester.pump();
    await _copy(tester);
    final before = _clipboard;

    // A step arrives while the selection stands; the anchors are model
    // positions, so the copy does not change under it.
    store.onUpdate({
      'kind': 'stack_patch',
      'rev': store.rev + 1,
      'baseRev': store.rev,
      'roots': store.roots,
      'upsert': [
        {
          'id': 'turn:1',
          'kind': 'turn',
          'children': [...store.node('turn:1')!.children!, 'step:9'],
        },
        {
          'id': 'step:9',
          'kind': 'step',
          'parent': 'turn:1',
          'step': {'headline': 'A new step arrived'},
        },
      ],
    });
    await tester.pumpAndSettle();
    await _copy(tester);

    expect(_clipboard, before);

    store.dispose();
    view.dispose();
  });

  testWidgets('a selection spans rows that are not built', (tester) async {
    _captureClipboard();
    // More rows than the window holds, so a row can leave the built window
    // while the drag is still going. The view opens at the top rather than
    // following the live end, so the drag starts on the first row.
    final store = fixtureStore(steps: 40);
    final view = ThreadViewState()..following = false;
    await pumpThread(tester, store: store, view: view);

    const message = 'Clean up the test fixtures nobody uses any more.';
    final from = _centreOf(tester, message);
    final window = tester.getRect(find.byType(ListView));
    final gesture = await tester.startGesture(
      from,
      kind: PointerDeviceKind.mouse,
    );
    await tester.pump();
    // The pointer is held past the list's bottom edge: the layer's edge
    // auto-scroll walks the anchor down the list on its own timer, which is
    // the path nothing else exercises.
    await gesture.moveTo(Offset(from.dx, window.bottom + 8));
    for (var i = 0; i < 40; i++) {
      await tester.pump(const Duration(milliseconds: 16));
    }
    await gesture.up();
    await tester.pump();

    // The row the drag began on is out of the tree now.
    expect(find.text(message), findsNothing, reason: 'the row left the tree');

    await _copy(tester);

    // The selection still spans it, and the rows the scroll brought in,
    // because an anchor is a position in the model rather than in the window.
    // The drag began mid-message, so the copy is the message's tail.
    expect(_clipboard, startsWith('s nobody uses any more.'));
    expect(_clipboard, contains('Step number 1 mapped the fixtures'));
    expect(_clipboard, contains('Step number 5 mapped the fixtures'));

    store.dispose();
    view.dispose();
  });

  testWidgets('a double click selects a word', (tester) async {
    _captureClipboard();
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    final at = _centreOf(
      tester,
      'Clean up the test fixtures nobody uses any more.',
    );
    await tester.tapAt(at);
    await tester.pump(const Duration(milliseconds: 40));
    await tester.tapAt(at);
    await tester.pump();
    await _copy(tester);

    expect(_clipboard, isNotNull);
    expect(_clipboard, isNot(contains(' ')));
    expect(
      'Clean up the test fixtures nobody uses any more.',
      contains(_clipboard!),
    );

    store.dispose();
    view.dispose();
  });

  testWidgets('a triple click selects the line', (tester) async {
    _captureClipboard();
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    final at = _centreOf(
      tester,
      'Clean up the test fixtures nobody uses any more.',
    );
    for (var i = 0; i < 3; i++) {
      await tester.tapAt(at);
      await tester.pump(const Duration(milliseconds: 40));
    }
    await tester.pump();
    await _copy(tester);

    expect(_clipboard, 'Clean up the test fixtures nobody uses any more.');

    store.dispose();
    view.dispose();
  });

  testWidgets('shift click extends the selection', (tester) async {
    _captureClipboard();
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    const message = 'Clean up the test fixtures nobody uses any more.';
    // A plain click at the row's start, then a shift click further along the
    // same row: the copy keeps the first anchor rather than restarting.
    await tester.tapAt(tester.getTopLeft(find.text(message)));
    await tester.pump(const Duration(milliseconds: 500));
    await tester.sendKeyDownEvent(LogicalKeyboardKey.shiftLeft);
    await tester.tapAt(_centreOf(tester, message));
    await tester.sendKeyUpEvent(LogicalKeyboardKey.shiftLeft);
    await tester.pump();
    await _copy(tester);

    expect(_clipboard, isNotNull);
    expect(message, startsWith(_clipboard!));
    expect(_clipboard!.length, greaterThan('Clean'.length));

    store.dispose();
    view.dispose();
  });

  testWidgets('select all copies every row', (tester) async {
    _captureClipboard();
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyA);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump();
    await _copy(tester);

    expect(_clipboard, contains('Clean up the test fixtures'));
    expect(_clipboard, contains('Step number 3 mapped the fixtures'));
    expect(_clipboard, contains('4 steps'));

    store.dispose();
    view.dispose();
  });

  testWidgets('the context menu copies the selection', (tester) async {
    _captureClipboard();
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyA);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump();

    final gesture = await tester.startGesture(
      _centreOf(tester, 'Step number 1 mapped the fixtures'),
      kind: PointerDeviceKind.mouse,
      buttons: kSecondaryButton,
    );
    await gesture.up();
    await tester.pumpAndSettle();

    await tester.tap(find.text('Copy'));
    await tester.pumpAndSettle();

    expect(_clipboard, contains('Step number 1 mapped the fixtures'));

    store.dispose();
    view.dispose();
  });
}
