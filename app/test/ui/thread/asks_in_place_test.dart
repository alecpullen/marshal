import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import 'support.dart';

/// The moment the asks were made, and the clock the screen reads them by.
final _asked = DateTime.utc(2026, 11, 17, 4, 10);
final _now = DateTime.utc(2026, 11, 17, 4, 22);

model.Ask _ask({String? step}) => model.Ask(
  host: 'local',
  id: 'call-1',
  kind: model.AskKind.approval,
  thread: threadRef(),
  title: 'Approve shell.run?',
  body: 'go test ./...',
  actions: const [
    model.AskAction('Approve once', primary: true),
    model.AskAction('Deny'),
  ],
  since: _asked,
  step: step,
);

void main() {
  testWidgets('an ask is pinned in the thread, at the step that raised it', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(
      tester,
      store: store,
      view: view,
      asks: [_ask(step: '1')],
      now: _now,
    );

    expect(find.text('Approve shell.run?'), findsOneWidget);
    expect(find.byType(AskCard), findsOneWidget);
    // The card sits after its step's row, not at the top of the transcript.
    final stepY = tester
        .getTopLeft(find.text('Step number 1 mapped the fixtures'))
        .dy;
    final cardY = tester.getTopLeft(find.byType(AskCard)).dy;
    expect(cardY, greaterThan(stepY));

    store.dispose();
    view.dispose();
  });

  testWidgets('answering in the thread goes through the one answer', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    model.Ask? answered;
    model.AskAction? withAction;
    await pumpThread(
      tester,
      store: store,
      view: view,
      asks: [_ask(step: '1')],
      now: _now,
      onAnswer: (ask, action) {
        answered = ask;
        withAction = action;
      },
    );

    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();

    // The same Ask the Inbox holds, and the action the card offers: the
    // InboxModel's answer is the one path for both places.
    expect(answered?.id, 'call-1');
    expect(answered?.step, '1');
    expect(withAction?.label, 'Approve once');

    store.dispose();
    view.dispose();
  });

  testWidgets('an ask with no step sits after the last row', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(
      tester,
      store: store,
      view: view,
      asks: [_ask()],
      now: _now,
    );

    final lastY = tester.getTopLeft(find.textContaining('4 steps')).dy;
    final cardY = tester.getTopLeft(find.byType(AskCard)).dy;
    expect(cardY, greaterThan(lastY));

    store.dispose();
    view.dispose();
  });
}
