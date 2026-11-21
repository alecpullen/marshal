import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/attention.dart' as model;
import 'package:marshal_app/src/model/composer_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as asks;
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_composer.dart';

import 'support.dart';

/// A scripted agent turn for a request to plan first: the person's ask, the
/// agent's plan, and nothing else — no tool row, because nothing has been
/// changed yet. The decision the plan waits on is the ask pinned below.
StackStore _planFirstTurn() => StackStore.seeded(
  const StackSnapshot(
    sessionID: 's-1',
    rev: 1,
    roots: ['turn:1'],
    nodes: [
      WireNode(id: 'turn:1', kind: 'turn', children: ['msg:1', 'msg:2']),
      WireNode(
        id: 'msg:1',
        kind: 'message',
        parent: 'turn:1',
        message: WireMessage(
          role: 'user',
          content: 'Plan how to fix the login redirect first.',
        ),
      ),
      WireNode(
        id: 'msg:2',
        kind: 'message',
        parent: 'turn:1',
        message: WireMessage(
          role: 'assistant',
          content:
              'Here is the plan: add the return path to the redirect, cover '
              'it with a test, and change nothing until you approve it.',
        ),
      ),
    ],
  ),
);

/// The ask the plan waits on: the agent may not edit before this is answered.
asks.Ask _approval() => asks.Ask(
  host: 'local',
  id: 'call-1',
  kind: asks.AskKind.approval,
  thread: threadRef(),
  title: 'May I edit the repository?',
  body: 'The plan is ready; nothing is changed until this is approved.',
  actions: [
    const asks.AskAction('Allow editing', primary: true),
    const asks.AskAction('Keep planning'),
  ],
  since: DateTime.utc(2026, 11, 17, 4, 10),
  source: model.AskSource.fleet,
  scope: model.AskScope.everyone,
);

/// A composer that only sends: these tests are about what a scripted turn
/// shows before the person decides.
ComposerModel _composer(ThreadViewState view) => ComposerModel(
  prompt: (text) async {},
  steer: (text) async {},
  cancel: () async {},
  setMode: (mode) async {},
  access: () async =>
      const AccessState(mode: 'ask', auto: false, system: false, revision: 1),
  view: view,
);

void main() {
  testWidgets('a plan is discussed before anything is changed', (tester) async {
    final store = _planFirstTurn();
    final view = ThreadViewState();
    final composer = _composer(view);
    await pumpThread(
      tester,
      store: store,
      view: view,
      asks: [_approval()],
      onAnswer: (ask, action, {reply, answers}) {},
      now: DateTime.utc(2026, 11, 17, 4, 22),
      composer: ThreadComposer(
        model: composer,
        view: view,
        recipient: 'The project agent',
      ),
    );

    // The plan the agent wrote is on screen, and it is the whole turn: the
    // scripted agent ran no tool, so no mutation precedes the decision.
    expect(find.textContaining('Here is the plan'), findsOneWidget);
    expect(find.byType(ToolRow), findsNothing);

    // The decision the plan waits on is within reach, with its own actions.
    expect(find.text('May I edit the repository?'), findsOneWidget);
    expect(find.text('Allow editing'), findsOneWidget);
    expect(find.text('Keep planning'), findsOneWidget);

    // Steering, Stop and the named recipient stay where the person left them:
    // no setup dialog stood between the request and this thread.
    expect(find.byType(ThreadComposer), findsOneWidget);
    expect(find.text('The project agent'), findsOneWidget);
    expect(find.text('Send'), findsOneWidget);

    store.dispose();
    view.dispose();
    composer.dispose();
  });
}
