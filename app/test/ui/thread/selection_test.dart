import 'package:flutter/gestures.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/selection.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/thread/selection_layer.dart';

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

  testWidgets('a highlight after a dropped blank line lands on its line', (
    tester,
  ) async {
    // The store keeps the notice's blank line; the row does not draw it, so
    // its second line sits two characters further along in the store than a
    // run of paragraph lengths would put it.
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['msg:1'],
        nodes: const [
          WireNode(
            id: 'msg:1',
            kind: 'message',
            message: WireMessage(role: 'system', content: 'a\n\nb'),
          ),
        ],
      ),
    );
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);
    await tester.tap(find.text('Details'));
    await tester.pump();

    final layer = tester.state<SelectionLayerState>(
      find.byType(SelectionLayer),
    );
    // The second line's offsets in the store text: what dragging across it
    // selects.
    layer.selection.begin(const TextAnchor('msg:1', 3));
    layer.selection.extend(const TextAnchor('msg:1', 4));
    await tester.pump();

    // The row's own painter is the one keyed on the row the layer registered.
    final row = find.byWidgetPredicate(
      (widget) => widget is CustomPaint && widget.key == layer.keys['msg:1'],
    );
    final canvas = TestRecordingCanvas();
    tester.widget<CustomPaint>(row).painter!.paint(canvas, Size.zero);
    final drawn = [
      for (final call in canvas.invocations)
        if (call.invocation.memberName == #drawRect)
          call.invocation.positionalArguments.first as Rect,
    ];

    // One box, on the second line's glyph: not on the first line, and not on
    // the Details control that follows it.
    final paragraph = tester.renderObject<RenderParagraph>(find.text('b'));
    final glyph = paragraph
        .getBoxesForSelection(
          const TextSelection(baseOffset: 0, extentOffset: 1),
        )
        .single
        .toRect();
    final glyphAt = glyph.shift(paragraph.localToGlobal(Offset.zero));
    final origin = tester
        .renderObject<RenderBox>(row)
        .localToGlobal(Offset.zero);
    expect(drawn, hasLength(1));
    expect(
      drawn.single.shift(origin),
      rectMoreOrLessEquals(glyphAt, epsilon: 0.5),
    );

    store.dispose();
    view.dispose();
  });

  testWidgets('a highlight on a thinking line below its lead lands on it', (
    tester,
  ) async {
    // The row draws a `Thinking` lead line that its store text carries too: a
    // projection without it would put every line below a lead line's length
    // out of place, and a highlight would land on the wrong glyphs.
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: const [
          WireNode(id: 'turn:1', kind: 'turn', children: ['think:1']),
          WireNode(
            id: 'think:1',
            kind: 'thinking',
            parent: 'turn:1',
            thinking: WireThinking(
              text: 'The parser is the first stop.\nThe fixtures matter more.',
            ),
          ),
        ],
      ),
    );
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);
    await tester.tap(find.text('Details'));
    await tester.pump();

    final layer = tester.state<SelectionLayerState>(
      find.byType(SelectionLayer),
    );
    const word = 'The fixtures';
    final at = store.plainText('think:1').indexOf(word);
    expect(at, greaterThan(0), reason: 'the lead line precedes it');
    layer.selection.begin(TextAnchor('think:1', at));
    layer.selection.extend(TextAnchor('think:1', at + word.length));
    await tester.pump();

    final row = find.byWidgetPredicate(
      (widget) => widget is CustomPaint && widget.key == layer.keys['think:1'],
    );
    final canvas = TestRecordingCanvas();
    tester.widget<CustomPaint>(row).painter!.paint(canvas, Size.zero);
    final drawn = [
      for (final call in canvas.invocations)
        if (call.invocation.memberName == #drawRect)
          call.invocation.positionalArguments.first as Rect,
    ];

    // One box, on the second thinking line's first word: the lead line and
    // the line between are not highlighted in its place.
    final paragraph = tester.renderObject<RenderParagraph>(
      find.text('The fixtures matter more.'),
    );
    final glyph = paragraph
        .getBoxesForSelection(
          const TextSelection(baseOffset: 0, extentOffset: 12),
        )
        .single
        .toRect();
    final glyphAt = glyph.shift(paragraph.localToGlobal(Offset.zero));
    final origin = tester
        .renderObject<RenderBox>(row)
        .localToGlobal(Offset.zero);
    expect(drawn, hasLength(1));
    expect(
      drawn.single.shift(origin),
      rectMoreOrLessEquals(glyphAt, epsilon: 0.5),
    );

    store.dispose();
    view.dispose();
  });

  testWidgets('select all copies a thinking row that is still folded', (
    tester,
  ) async {
    _captureClipboard();
    final store = StackStore.seeded(
      StackSnapshot(
        sessionID: 's-1',
        rev: 1,
        roots: const ['turn:1'],
        nodes: const [
          WireNode(id: 'turn:1', kind: 'turn', children: ['think:1']),
          WireNode(
            id: 'think:1',
            kind: 'thinking',
            parent: 'turn:1',
            thinking: WireThinking(
              text:
                  'The parser is the first stop.\n'
                  'The fixtures it reads matter more than the rest.',
            ),
          ),
        ],
      ),
    );
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    // The row is folded and *Details* is not tapped: only its first line is
    // drawn.
    expect(find.text('Thinking'), findsOneWidget);
    expect(find.textContaining('The fixtures it reads matter'), findsNothing);

    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyA);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump();
    await _copy(tester);

    // Copy is model-backed, so folding hides a line on screen and not from
    // the clipboard.
    expect(_clipboard, contains('The parser is the first stop.'));
    expect(_clipboard, contains('The fixtures it reads matter more'));
    expect(_clipboard, startsWith('Thinking'));

    store.dispose();
    view.dispose();
  });
}
