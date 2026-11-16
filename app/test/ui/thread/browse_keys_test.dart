import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';

import 'support.dart';

void main() {
  testWidgets('j moves down a row, and from nothing it starts at the first', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyEvent(LogicalKeyboardKey.keyJ);
    await tester.pump();
    expect(view.cursor, 'msg:1');

    await tester.sendKeyEvent(LogicalKeyboardKey.keyJ);
    await tester.pump();
    expect(view.cursor, 'step:1');

    store.dispose();
    view.dispose();
  });

  testWidgets('k moves up a row, and from nothing it starts at the last', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyEvent(LogicalKeyboardKey.keyK);
    await tester.pump();
    expect(view.cursor, 'receipt:1');

    store.dispose();
    view.dispose();
  });

  testWidgets('J moves ten rows down', (tester) async {
    final store = fixtureStore(steps: 20);
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyEvent(LogicalKeyboardKey.keyJ);
    await tester.pump();
    expect(view.cursor, 'msg:1');

    await tester.sendKeyDownEvent(LogicalKeyboardKey.shiftLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyJ);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.shiftLeft);
    await tester.pump();

    expect(view.cursor, isNot('msg:1'));

    store.dispose();
    view.dispose();
  });

  testWidgets('K moves ten rows up', (tester) async {
    final store = fixtureStore(steps: 20);
    final view = ThreadViewState()..cursor = 'step:8';
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyDownEvent(LogicalKeyboardKey.shiftLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyK);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.shiftLeft);
    await tester.pump();

    expect(view.cursor, isNot('step:8'));

    store.dispose();
    view.dispose();
  });

  testWidgets('g goes to the first row and G to the last', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState()..cursor = 'step:2';
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyEvent(LogicalKeyboardKey.keyG);
    await tester.pump();
    expect(view.cursor, 'msg:1');

    await tester.sendKeyDownEvent(LogicalKeyboardKey.shiftLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyG);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.shiftLeft);
    await tester.pump();
    expect(view.cursor, 'receipt:1');

    store.dispose();
    view.dispose();
  });

  testWidgets('Enter cycles the density', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    expect(view.density, Density.comfortable);
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pump();
    expect(view.density, Density.full);
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pump();
    expect(view.density, Density.outline);

    store.dispose();
    view.dispose();
  });

  testWidgets('z folds the cursor row, and again unfolds it', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState()..cursor = 'step:1';
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyEvent(LogicalKeyboardKey.keyZ);
    await tester.pump();
    expect(view.folded('step:1'), isTrue);

    await tester.sendKeyEvent(LogicalKeyboardKey.keyZ);
    await tester.pump();
    expect(view.folded('step:1'), isFalse);

    store.dispose();
    view.dispose();
  });

  testWidgets('z with no cursor folds nothing', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyEvent(LogicalKeyboardKey.keyZ);
    await tester.pump();
    expect(view.folded('step:1'), isFalse);

    store.dispose();
    view.dispose();
  });

  testWidgets('y copies the cursor row', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState()..cursor = 'step:1';
    String? copied;
    await pumpThread(
      tester,
      store: store,
      view: view,
      onCopyRow: (id) => copied = id,
    );

    await tester.sendKeyEvent(LogicalKeyboardKey.keyY);
    await tester.pump();

    expect(copied, 'step:1');

    store.dispose();
    view.dispose();
  });

  testWidgets('Esc leaves browse mode, keeping the cursor where it is', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState()..cursor = 'step:2';
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pump();
    expect(view.cursor, isNull);

    store.dispose();
    view.dispose();
  });

  testWidgets('i is reserved for the inspector and does nothing', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState()..cursor = 'step:2';
    await pumpThread(tester, store: store, view: view);

    await tester.sendKeyEvent(LogicalKeyboardKey.keyI);
    await tester.pump();

    expect(view.cursor, 'step:2');
    expect(view.density, Density.comfortable);
    expect(tester.takeException(), isNull);

    store.dispose();
    view.dispose();
  });

  testWidgets('⌘N opens the new-work dialog', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    var opened = 0;
    await pumpThread(
      tester,
      store: store,
      view: view,
      onNewWork: () => opened++,
    );

    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyN);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump();

    expect(opened, 1);

    store.dispose();
    view.dispose();
  });

  test('every browse key is in the one map, with words for its tooltip', () {
    for (final key in const [
      'Esc',
      'j',
      'k',
      'J',
      'K',
      'g',
      'G',
      'Enter',
      'z',
      'y',
      'i',
      '⌘N',
    ]) {
      expect(
        threadKeys.any((entry) => entry.key == key),
        isTrue,
        reason: '$key is a browse key',
      );
    }
    for (final entry in threadKeys) {
      expect(entry.description, isNotEmpty);
      expect(threadKeyTip(entry.key), contains(entry.description));
    }
  });
}
