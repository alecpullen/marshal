import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';

import 'support.dart';

/// The transcript's scroll position.
ScrollPosition _position(WidgetTester tester) =>
    tester.state<ScrollableState>(find.byType(Scrollable).first).position;

void main() {
  testWidgets('follows while it is at the end', (tester) async {
    final store = fixtureStore(steps: 40);
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    expect(view.following, isTrue);
    expect(_position(tester).pixels, _position(tester).maxScrollExtent);

    // A new step arrives while the view is at the end: it stays there.
    store.onUpdate({
      'kind': 'stack_patch',
      'rev': store.rev + 1,
      'baseRev': store.rev,
      'roots': store.roots,
      'upsert': [
        {
          'id': 'turn:1',
          'kind': 'turn',
          'children': [...store.node('turn:1')!.children!, 'step:41'],
        },
        {
          'id': 'step:41',
          'kind': 'step',
          'parent': 'turn:1',
          'step': {'headline': 'A new step arrived'},
        },
      ],
    });
    await tester.pumpAndSettle();

    expect(view.following, isTrue);
    expect(_position(tester).pixels, _position(tester).maxScrollExtent);
    expect(find.text('A new step arrived'), findsOneWidget);

    store.dispose();
    view.dispose();
  });

  testWidgets('stops following on scroll up, with one jump control', (
    tester,
  ) async {
    final store = fixtureStore(steps: 40);
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.drag(find.byType(ListView), const Offset(0, 300));
    await tester.pumpAndSettle();

    expect(view.following, isFalse);
    expect(find.textContaining('Jump to live'), findsOneWidget);
    // One control, not one per row.
    expect(find.textContaining('Jump to live'), findsOneWidget);

    store.dispose();
    view.dispose();
  });

  testWidgets('the jump control carries the live headline', (tester) async {
    final store = fixtureStore(steps: 40);
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.drag(find.byType(ListView), const Offset(0, 300));
    await tester.pumpAndSettle();

    // The newest row's first line is what the agent is doing now.
    expect(
      find.textContaining('Step number 40 mapped the fixtures'),
      findsOneWidget,
    );

    store.dispose();
    view.dispose();
  });

  testWidgets('jump to live resumes following', (tester) async {
    final store = fixtureStore(steps: 40);
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.drag(find.byType(ListView), const Offset(0, 300));
    await tester.pumpAndSettle();
    expect(view.following, isFalse);

    await tester.tap(find.textContaining('Jump to live'));
    await tester.pumpAndSettle();

    expect(view.following, isTrue);
    expect(find.textContaining('Jump to live'), findsNothing);
    expect(_position(tester).pixels, _position(tester).maxScrollExtent);

    store.dispose();
    view.dispose();
  });

  testWidgets('returning to the end resumes following', (tester) async {
    final store = fixtureStore(steps: 40);
    final view = ThreadViewState();
    await pumpThread(tester, store: store, view: view);

    await tester.drag(find.byType(ListView), const Offset(0, 300));
    await tester.pumpAndSettle();
    expect(view.following, isFalse);

    await tester.drag(find.byType(ListView), const Offset(0, -600));
    await tester.pumpAndSettle();

    expect(view.following, isTrue);

    store.dispose();
    view.dispose();
  });
}
