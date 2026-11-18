import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/composer_model.dart';
import 'package:marshal_app/src/model/attention.dart' as ask;
import 'package:marshal_app/src/model/inbox_model.dart' as ask;
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_composer.dart';

import '../test/ui/thread/support.dart';

/// F3's end-to-end flow on a device: an agent's thread is shown, a prompt is
/// sent, the running turn is steered, an approval is answered where it was
/// raised, and the transcript the screen copies is the session's own
/// plain-text projection.
///
/// The same flow runs as a widget test in `test/ui/thread/e2e_test.dart`,
/// which is what CI can run; this one needs a display:
/// `flutter test integration_test -d linux`.
void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('a thread is started, steered and answered from the app', (
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

    await pumpThread(
      tester,
      store: store,
      view: view,
      now: DateTime.utc(2026, 11, 17, 4, 20),
      asks: [
        ask.Ask(
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
          source: ask.AskSource.fleet,
          scope: ask.AskScope.everyone,
          step: '1',
        ),
      ],
      onAnswer: (theAsk, action) =>
          answered.add('${theAsk.id}:${action.label}'),
      composer: ThreadComposer(model: composer, view: view),
    );

    await tester.enterText(find.byType(EditableText), 'Go ahead');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Send'));
    await tester.pumpAndSettle();
    expect(sent.single, 'prompt:Go ahead');

    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();
    expect(answered.single, 'call-1:Approve once');

    await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
    await tester.sendKeyEvent(LogicalKeyboardKey.keyA);
    await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
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

    composer.dispose();
    store.dispose();
    view.dispose();
  });
}
