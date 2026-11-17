import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/composer_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as ask;
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_composer.dart';

import 'support.dart';

/// The end-to-end flow this phase promises (f3 plan, done-when): a person
/// starts an agent, steers it, answers one approval and lets it finish, and
/// the transcript the screen shows is the session's own plain-text
/// projection.
///
/// It runs here as a widget test; `integration_test/f3_test.dart` drives the
/// same flow on a device.
void main() {
  testWidgets('start, steer, answer, finish: the transcript is the session', (
    tester,
  ) async {
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

    final store = fixtureStore(steps: 2);
    final view = ThreadViewState();
    final sent = <String>[];
    final answered = <String>[];
    ask.Ask? answeredAsk;
    ask.AskAction? answeredAction;
    final composer = ComposerModel(
      prompt: (text) async => sent.add('prompt:$text'),
      steer: (text) async => sent.add('steer:$text'),
      cancel: () async {},
      setMode: (mode) async {},
      access: () async => const AccessState(
        mode: 'act',
        auto: false,
        system: false,
        revision: 1,
      ),
      view: view,
    );
    final askToAnswer = ask.Ask(
      host: 'local',
      id: 'call-1',
      kind: ask.AskKind.approval,
      thread: threadRef(),
      title: 'Approve shell.run?',
      body: 'scripts/test.sh',
      actions: const [
        ask.AskAction('Approve once', primary: true),
        ask.AskAction('Deny'),
      ],
      since: DateTime.utc(2026, 11, 17, 4, 10),
      step: '1',
    );

    await pumpThread(
      tester,
      store: store,
      view: view,
      asks: [askToAnswer],
      now: DateTime.utc(2026, 11, 17, 4, 20),
      onAnswer: (theAsk, action) {
        answered.add('${theAsk.id}:${action.label}');
        answeredAsk = theAsk;
        answeredAction = action;
      },
      composer: ThreadComposer(model: composer, view: view),
    );

    // The agent was started: its transcript is on screen.
    expect(find.text('Step number 1 mapped the fixtures'), findsOneWidget);

    // A prompt is sent; the turn runs, and the next send steers.
    await tester.enterText(
      find.byType(EditableText),
      'Go ahead and remove them',
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Send'));
    await tester.pumpAndSettle();
    expect(sent, ['prompt:Go ahead and remove them']);
    expect(composer.running, isTrue);

    await tester.enterText(find.byType(EditableText), 'Keep the legacy ones');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Steer'));
    await tester.pumpAndSettle();
    expect(sent.last, 'steer:Keep the legacy ones');

    // The approval is answered where it was raised, while the turn is still
    // running and the composer still holds the field.
    expect(composer.running, isTrue);
    expect(find.byType(ThreadComposer), findsOneWidget);
    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();
    expect(answered, ['call-1:Approve once']);

    // The exact request and the exact action: the id an answer is posted to,
    // the step it was pinned at, and the action's own label and primacy.
    expect(
      identical(answeredAsk, askToAnswer),
      isTrue,
      reason: 'the screen answers with the Ask the Inbox holds, not a copy',
    );
    expect(answeredAsk?.id, 'call-1');
    expect(answeredAsk?.kind, ask.AskKind.approval);
    expect(answeredAsk?.step, '1');
    expect(answeredAction?.label, 'Approve once');
    expect(answeredAction?.primary, isTrue);

    // The transcript the screen shows is the session's own projection: every
    // row's plain text, in order, is what selecting all copies.
    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyA);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump();
    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyC);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
    await tester.pump();

    final projection = [
      store.plainText('msg:1'),
      for (var i = 1; i <= 2; i++) ...[
        store.plainText('step:$i'),
        store.plainText('tool:$i'),
      ],
      store.plainText('receipt:1'),
    ].join('\n');
    expect(clipboard, projection);

    // The turn finishing is the store's news, not the composer's guess.
    composer.setRunning(false);
    await tester.pumpAndSettle();
    expect(find.text('Send'), findsOneWidget);

    composer.dispose();
    store.dispose();
    view.dispose();
  });
}
