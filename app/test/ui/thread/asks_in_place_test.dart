import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/composer_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_composer.dart';

import 'support.dart';

/// The moment the asks were made, and the clock the screen reads them by.
final _asked = DateTime.utc(2026, 11, 17, 4, 10);
final _now = DateTime.utc(2026, 11, 17, 4, 22);

model.Ask _ask({
  String? step,
  String id = 'call-1',
  String title = 'Approve shell.run?',
  String approve = 'Approve once',
  model.AskKind kind = model.AskKind.approval,
}) => model.Ask(
  host: 'local',
  id: id,
  kind: kind,
  thread: threadRef(),
  title: title,
  body: 'go test ./...',
  actions: [
    model.AskAction(approve, primary: true),
    const model.AskAction('Deny'),
  ],
  since: _asked,
  step: step,
);

/// A store that holds no row yet: an agent that has narrated nothing, which
/// is when a question can still arrive first.
StackStore _emptyStore() => StackStore.seeded(
  const StackSnapshot(sessionID: 's-1', rev: 1, roots: [], nodes: []),
);

/// A composer that sends nothing: these tests are about what the transcript
/// offers, not about the field.
ComposerModel _composer(ThreadViewState view) => ComposerModel(
  prompt: (text) async {},
  steer: (text) async {},
  cancel: () async {},
  setMode: (mode) async {},
  access: () async =>
      const AccessState(mode: 'act', auto: false, system: false, revision: 1),
  view: view,
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

  testWidgets('concurrent asks pin each once, and answer with its own ask', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    final asks = [
      _ask(step: '1', id: 'call-1', title: 'Approve shell.run?'),
      _ask(
        step: '3',
        id: 'call-2',
        title: 'Approve git.push?',
        approve: 'Approve push',
      ),
      _ask(
        id: 'call-3',
        title: 'Approve the release?',
        approve: 'Approve release',
      ),
    ];
    final answeredAsks = <model.Ask>[];
    final answeredActions = <model.AskAction>[];
    await pumpThread(
      tester,
      store: store,
      view: view,
      asks: asks,
      now: _now,
      onAnswer: (ask, action) {
        answeredAsks.add(ask);
        answeredActions.add(action);
      },
    );
    // A window tall enough to hold every row and every card at once, so the
    // pins can be read where they are.
    tester.view.physicalSize = const Size(900, 1400);
    await tester.pumpAndSettle();

    // Three asks, each drawn once: a request is not duplicated, and none is
    // dropped because another one arrived.
    expect(find.byType(AskCard), findsNWidgets(3));
    expect(find.text('Approve shell.run?'), findsOneWidget);
    expect(find.text('Approve git.push?'), findsOneWidget);
    expect(find.text('Approve the release?'), findsOneWidget);

    // Each step's ask sits after its own step, and the step-less ask last.
    final first = tester.getTopLeft(find.text('Approve shell.run?')).dy;
    final second = tester.getTopLeft(find.text('Approve git.push?')).dy;
    final third = tester.getTopLeft(find.text('Approve the release?')).dy;
    expect(
      first,
      greaterThan(
        tester.getTopLeft(find.text('Step number 1 mapped the fixtures')).dy,
      ),
    );
    expect(
      second,
      greaterThan(
        tester.getTopLeft(find.text('Step number 3 mapped the fixtures')).dy,
      ),
    );
    expect(
      third,
      greaterThan(tester.getTopLeft(find.textContaining('4 steps')).dy),
    );
    expect(first, lessThan(second));
    expect(second, lessThan(third));

    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Approve push'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Approve release'));
    await tester.pumpAndSettle();

    // The exact request and the exact action, in the order they were taken.
    expect(answeredAsks, asks);
    expect(
      [for (final ask in answeredAsks) ask.id],
      ['call-1', 'call-2', 'call-3'],
    );
    expect([for (final ask in answeredAsks) ask.step], ['1', '3', null]);
    expect(
      [for (final action in answeredActions) action.label],
      ['Approve once', 'Approve push', 'Approve release'],
    );

    store.dispose();
    view.dispose();
  });

  testWidgets('an empty transcript still offers the ask waiting on it', (
    tester,
  ) async {
    final store = _emptyStore();
    final view = ThreadViewState();
    final answered = <String>[];
    await pumpThread(
      tester,
      store: store,
      view: view,
      // The request names a step this tree does not hold yet: with no row to
      // pin it to, it is still shown rather than waiting for the row.
      asks: [_ask(step: '1')],
      now: _now,
      onAnswer: (ask, action) => answered.add('${ask.id}:${action.label}'),
    );

    expect(find.text('Nothing has happened in this thread yet.'), findsNothing);
    expect(find.byType(AskCard), findsOneWidget);
    expect(find.text('Approve shell.run?'), findsOneWidget);

    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();
    expect(answered, ['call-1:Approve once']);

    store.dispose();
    view.dispose();
  });

  testWidgets('an empty transcript with the composer still offers its ask', (
    tester,
  ) async {
    final store = _emptyStore();
    final view = ThreadViewState();
    final composer = _composer(view);
    final answered = <String>[];
    await pumpThread(
      tester,
      store: store,
      view: view,
      asks: [_ask(step: '1')],
      now: _now,
      onAnswer: (ask, action) => answered.add('${ask.id}:${action.label}'),
      composer: ThreadComposer(model: composer, view: view),
    );

    // The embedded shape: the field is there, and the ask reaches the person
    // above it instead of an empty transcript.
    expect(find.byType(ThreadComposer), findsOneWidget);
    expect(find.text('Nothing has happened in this thread yet.'), findsNothing);
    expect(find.byType(AskCard), findsOneWidget);
    expect(find.text('Approve shell.run?'), findsOneWidget);

    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();
    expect(answered, ['call-1:Approve once']);

    composer.dispose();
    store.dispose();
    view.dispose();
  });

  testWidgets('with no answer path the ask is named and cannot be acted on', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    await pumpThread(
      tester,
      store: store,
      view: view,
      asks: [
        _ask(step: '1'),
        // A recovery has no action this adapter supports: it is drawn as the
        // decision it is, with nothing offered that would fail.
        _ask(
          step: '3',
          id: 'recover-1',
          title: 'The session was lost',
          approve: 'Resume',
          kind: model.AskKind.recovery,
        ),
      ],
      now: _now,
    );

    // Both requests are still named where they are pinned.
    expect(find.text('Approve shell.run?'), findsOneWidget);
    expect(find.text('The session was lost'), findsOneWidget);
    expect(
      tester.widget<AskCard>(find.byType(AskCard).at(1)).kind,
      AskKind.decision,
      reason: 'a recovery is the decision it asks for',
    );

    // Nothing to answer with: every action is drawn disabled, so a tap takes
    // no action rather than reporting one that did not happen.
    for (final label in ['Approve once', 'Deny', 'Resume']) {
      final buttons = find.widgetWithText(Btn, label);
      expect(buttons, findsWidgets, reason: 'the action is still named');
      for (final element in buttons.evaluate()) {
        expect((element.widget as Btn).onTap, isNull);
      }
    }
    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Resume'));
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
    expect(find.byType(AskCard), findsNWidgets(2));

    store.dispose();
    view.dispose();
  });

  testWidgets('an ask whose step the tree does not hold is still pinned', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    final answered = <String>[];
    await pumpThread(
      tester,
      store: store,
      view: view,
      // The request names a step this tree does not hold: it is pinned after
      // the last row rather than dropped.
      asks: [_ask(step: '99', id: 'orphan-1')],
      now: _now,
      onAnswer: (ask, action) => answered.add('${ask.id}:${action.label}'),
    );

    expect(find.byType(AskCard), findsOneWidget);
    final lastY = tester.getTopLeft(find.textContaining('4 steps')).dy;
    final cardY = tester.getTopLeft(find.byType(AskCard)).dy;
    expect(cardY, greaterThan(lastY));

    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();
    expect(answered, ['orphan-1:Approve once']);

    store.dispose();
    view.dispose();
  });

  testWidgets('an ask from after the screen\'s clock is not shown yet', (
    tester,
  ) async {
    final store = _emptyStore();
    final view = ThreadViewState();
    await pumpThread(
      tester,
      store: store,
      view: view,
      asks: [_ask(step: '1')],
      // The screen's clock is before the request was made.
      now: _asked.subtract(const Duration(minutes: 1)),
    );

    // Nothing is waiting as of this clock, so the empty message is the honest
    // state rather than a blank transcript.
    expect(
      find.text('Nothing has happened in this thread yet.'),
      findsOneWidget,
    );
    expect(find.byType(AskCard), findsNothing);

    store.dispose();
    view.dispose();
  });
}
