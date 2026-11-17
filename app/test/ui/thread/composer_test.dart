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
}) => ComposerModel(
  prompt: (text) async {},
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
}
