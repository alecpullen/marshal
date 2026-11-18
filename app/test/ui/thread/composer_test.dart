import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/composer_model.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_composer.dart';

import 'support.dart';

/// A composer whose routes only count what they were asked to do.
ComposerModel _model(
  ThreadViewState view, {
  bool running = false,
  String accessMode = 'act',
  void Function()? onCancel,
  Future<void> Function(String text)? prompt,
}) => ComposerModel(
  prompt: prompt ?? (text) async {},
  steer: (text) async {},
  cancel: () async => onCancel?.call(),
  setMode: (mode) async {},
  access: () async =>
      AccessState(mode: accessMode, auto: false, system: false, revision: 1),
  view: view,
  running: running,
  model: 'qwen3-coder',
);

void main() {
  testWidgets('idle: a reply field with Send and no Stop', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    final composer = _model(view);
    await pumpThread(
      tester,
      store: store,
      view: view,
      composer: ThreadComposer(model: composer, view: view),
    );

    expect(find.text('Reply to the agent…'), findsOneWidget);
    expect(find.text('Send'), findsOneWidget);
    expect(find.text('Stop'), findsNothing);
    expect(find.text('qwen3-coder'), findsOneWidget);

    store.dispose();
    view.dispose();
    composer.dispose();
  });

  testWidgets('running: Steer and Stop, and Stop cancels', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    var cancels = 0;
    final composer = _model(view, running: true, onCancel: () => cancels++);
    await pumpThread(
      tester,
      store: store,
      view: view,
      composer: ThreadComposer(model: composer, view: view),
    );

    expect(find.text('Steer the agent…'), findsOneWidget);
    expect(find.text('Steer'), findsOneWidget);
    expect(find.text('Send'), findsNothing);

    await tester.tap(find.text('Stop'));
    await tester.pumpAndSettle();
    expect(cancels, 1);

    // Cancelling is a request: until the session reports the turn ended, the
    // composer keeps Steer and Stop and says what it is waiting for.
    expect(composer.running, isTrue);
    expect(composer.cancellationRequested, isTrue);
    expect(find.text('Steer'), findsOneWidget);
    expect(find.text('Stop'), findsOneWidget);
    expect(find.text('Send'), findsNothing);
    expect(
      find.text('Cancellation requested — waiting for the session to stop.'),
      findsOneWidget,
    );

    // The session's own report is what confirms the stop.
    composer.setRunning(false);
    await tester.pumpAndSettle();
    expect(composer.cancellationRequested, isFalse);
    expect(
      find.text('Cancellation requested — waiting for the session to stop.'),
      findsNothing,
    );
    expect(find.text('Stop'), findsNothing);
    expect(find.text('Send'), findsOneWidget);

    store.dispose();
    view.dispose();
    composer.dispose();
  });

  testWidgets('a read-only session is one line saying why', (tester) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    final composer = _model(view);
    await composer.refreshAccess();
    // The access tuple the composer reads decides it; ask mode is read-only.
    final readOnly = _model(view, accessMode: 'ask');
    await readOnly.refreshAccess();
    await pumpThread(
      tester,
      store: store,
      view: view,
      composer: ThreadComposer(model: readOnly, view: view),
    );

    expect(find.textContaining('ask mode'), findsOneWidget);
    expect(find.byType(EditableText), findsNothing);
    expect(find.text('Send'), findsNothing);

    store.dispose();
    view.dispose();
    composer.dispose();
    readOnly.dispose();
  });

  testWidgets('the draft survives a rebuild, because it is the thread\'s', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState()..draft = 'half a thought';
    final composer = _model(view);
    await pumpThread(
      tester,
      store: store,
      view: view,
      composer: ThreadComposer(model: composer, view: view),
    );

    expect(find.text('half a thought'), findsOneWidget);

    // Rebuilding the screen keeps the draft: the field reads it from the
    // thread, not from a widget's local state.
    await pumpThread(
      tester,
      store: store,
      view: view,
      composer: ThreadComposer(model: composer, view: view),
    );
    expect(find.text('half a thought'), findsOneWidget);

    store.dispose();
    view.dispose();
    composer.dispose();
  });

  testWidgets('a failed send keeps the text and posts nothing twice', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    var prompts = 0;
    final composer = _model(
      view,
      prompt: (text) async {
        prompts++;
        throw Exception('the bridge went away');
      },
    );
    await pumpThread(
      tester,
      store: store,
      view: view,
      composer: ThreadComposer(model: composer, view: view),
    );

    await tester.enterText(find.byType(EditableText), 'tidy the fixtures');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Send'));
    await tester.pumpAndSettle();

    // One attempt, refused. The composer says why, does not claim a turn
    // started, and the text is still in the field.
    expect(prompts, 1);
    expect(composer.running, isFalse);
    expect(composer.failure, isNotNull);
    expect(find.textContaining('the bridge went away'), findsOneWidget);
    expect(view.draft, 'tidy the fixtures');
    expect(find.text('tidy the fixtures'), findsOneWidget);

    // Nothing resends it: an uncertain request stays unsent until the user
    // sends again.
    await tester.pump(const Duration(seconds: 5));
    await tester.pumpAndSettle();
    expect(prompts, 1);
    expect(view.draft, 'tidy the fixtures');

    store.dispose();
    view.dispose();
    composer.dispose();
  });

  testWidgets('a phone-sized viewport with a keyboard keeps Send reachable', (
    tester,
  ) async {
    final store = fixtureStore();
    final view = ThreadViewState();
    final composer = _model(view);
    await pumpThread(
      tester,
      store: store,
      view: view,
      composer: ThreadComposer(model: composer, view: view),
    );

    // A phone-sized window with an on-screen keyboard open: the keyboard takes
    // the bottom 300 logical pixels, and the composer pads itself by that.
    tester.view.physicalSize = const Size(390, 600);
    tester.view.viewInsets = const FakeViewPadding(bottom: 300);
    await tester.pumpAndSettle();

    expect(tester.takeException(), isNull);
    // Both stand in the part of the window the keyboard leaves visible.
    expect(tester.getRect(find.byType(EditableText)).bottom, lessThan(300));
    expect(tester.getRect(find.text('Send')).bottom, lessThan(300));

    await tester.enterText(find.byType(EditableText), 'on my phone');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Send'));
    await tester.pumpAndSettle();
    // The tap landed: the route took the draft, so it is no longer unsent.
    expect(view.draft, isEmpty);

    store.dispose();
    view.dispose();
    composer.dispose();
  });
}
