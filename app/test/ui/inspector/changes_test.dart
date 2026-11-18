import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
// The contract's CheckState is a CI check's wire state; the kit's is a check
// drawn in the interface. This test reads the kit's.
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart' hide CheckState;
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/changes_model.dart';
import 'package:marshal_app/src/model/inspector_state.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/inspector/changes_tab.dart';
import 'package:marshal_app/src/ui/inspector/inspector.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';

import '../thread/support.dart';

const _stackGo =
    'diff --git a/internal/acp/stack.go b/internal/acp/stack.go\n'
    '--- a/internal/acp/stack.go\n'
    '+++ b/internal/acp/stack.go\n'
    '@@ -1,2 +1,2 @@\n'
    ' one\n'
    '-two\n'
    '+three\n';

/// The thread screen with the Changes tab in its inspector, which is how a
/// person reaches it (f4-inspector.md decision 2).
Future<void> pumpChanges(
  WidgetTester tester,
  ChangesModel model, {
  InspectorState? inspector,
  String? shipped,
}) async {
  tester.view.physicalSize = const Size(1440, 900);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  tester.platformDispatcher.accessibilityFeaturesTestValue =
      const FakeAccessibilityFeatures(disableAnimations: true);
  addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);
  await tester.pumpWidget(
    EmberApp(
      brightness: Brightness.light,
      home: ThreadScreen(
        thread: threadRef(),
        store: StackStore.seeded(transcriptFixture()),
        view: ThreadViewState(),
        inspector: inspector ?? InspectorState(),
        inspectorBody: (tab) => tab == InspectorTab.changes
            ? ChangesTab(model: model, shipped: shipped)
            : Text('body ${tab.name}'),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('gate shows each check', (tester) async {
    final model = ChangesModel.seeded(
      gate: const GateResult(
        ok: false,
        skipped: false,
        failedCommand: 'go test ./...',
      ),
      gateRanAt: DateTime.utc(2026, 11, 17, 4, 8),
      files: const [
        DiffFile(path: 'internal/acp/stack.go', added: 1, removed: 1),
      ],
      diff: _stackGo,
    );
    await pumpChanges(tester, model);

    expect(find.text('Build and test failed'), findsOneWidget);
    expect(find.text('go test ./...'), findsOneWidget);
    // The state is never colour alone: its icon is the failed one.
    expect(
      find.descendant(
        of: find.byType(GateList),
        matching: find.byWidgetPredicate(
          (w) => w is Icon && w.icon == CheckState.failed.icon,
        ),
      ),
      findsOneWidget,
    );

    // A gate that passed reads as passed.
    await pumpChanges(
      tester,
      ChangesModel.seeded(gate: const GateResult(ok: true, skipped: false)),
    );
    expect(find.text('Build and test'), findsOneWidget);
    expect(find.text('passed'), findsOneWidget);

    // And one that has not run says so, rather than showing nothing.
    await pumpChanges(tester, ChangesModel.seeded());
    // The gate section and the footer's one-line summary both say it.
    expect(find.text('The gate has not run'), findsWidgets);
  });

  testWidgets('by step groups hunks by the step that changed them', (
    tester,
  ) async {
    final model = ChangesModel.seeded(
      gate: const GateResult(ok: true, skipped: false),
      files: const [
        DiffFile(path: 'internal/acp/stack.go', added: 1, removed: 1),
        DiffFile(path: 'internal/acp/new.go', added: 7, removed: 0),
      ],
      diff: _stackGo,
      steps: const [
        StepDiff(
          stepNode: 'step:1',
          turnNode: 'turn:1',
          headline: 'Removed the dead fixtures',
          files: ['internal/acp/stack.go'],
          diff: '@@ -1,2 +1,2 @@\n one\n-two\n+three',
        ),
        StepDiff(
          stepNode: 'step:2',
          turnNode: 'turn:1',
          headline: 'Added the new file',
          files: ['internal/acp/new.go'],
          diff: '@@ -0,0 +1,1 @@\n+new',
        ),
      ],
    );
    await pumpChanges(tester, model);

    // Whole, every file is in one list and no step is named.
    expect(find.text('Removed the dead fixtures'), findsNothing);
    expect(find.text('internal/acp/new.go'), findsOneWidget);

    await tester.tap(find.widgetWithText(Btn, 'All changes'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('By step'));
    await tester.pumpAndSettle();

    expect(model.byStep, isTrue);
    expect(find.text('Removed the dead fixtures'), findsOneWidget);
    expect(find.text('Added the new file'), findsOneWidget);
    expect(find.text('internal/acp/stack.go'), findsWidgets);
  });

  testWidgets('viewed marks persist across tabs', (tester) async {
    final model = ChangesModel.seeded(
      gate: const GateResult(ok: true, skipped: false),
      files: const [
        DiffFile(path: 'internal/acp/stack.go', added: 1, removed: 1),
      ],
      diff: _stackGo,
    );
    await pumpChanges(tester, model);
    expect(find.text('+1 −1'), findsOneWidget);

    await tester.tap(find.widgetWithText(Btn, 'Mark viewed'));
    await tester.pumpAndSettle();
    expect(model.isViewed('internal/acp/stack.go'), isTrue);
    expect(find.text('✓ +1 −1'), findsOneWidget);

    // Away to another tab and back: the mark is the model's, so it stays.
    await tester.tap(find.text('Files'));
    await tester.pumpAndSettle();
    expect(find.text('body files'), findsOneWidget);
    await tester.tap(find.text('Changes'));
    await tester.pumpAndSettle();
    expect(find.text('✓ +1 −1'), findsOneWidget);
    expect(find.widgetWithText(Btn, 'Viewed'), findsOneWidget);
  });

  testWidgets('the selected file is the one whose diff is shown', (
    tester,
  ) async {
    final model = ChangesModel.seeded(
      gate: const GateResult(ok: true, skipped: false),
      files: const [
        DiffFile(path: 'internal/acp/stack.go', added: 1, removed: 1),
        DiffFile(path: 'internal/acp/new.go', added: 7, removed: 0),
      ],
      diff: _stackGo,
    );
    await pumpChanges(tester, model);
    expect(model.activeFile, 'internal/acp/stack.go');
    expect(find.text('two'), findsOneWidget);

    await tester.tap(find.text('internal/acp/new.go'));
    await tester.pumpAndSettle();
    expect(model.activeFile, 'internal/acp/new.go');
    // That file has no hunk in this diff, so the diff says so rather than
    // showing another file's lines.
    expect(find.text('No changes to show'), findsOneWidget);
    expect(find.text('two'), findsNothing);
  });

  testWidgets('the footer says what the change shipped as', (tester) async {
    final model = ChangesModel.seeded(
      gate: const GateResult(ok: true, skipped: false),
      files: const [
        DiffFile(path: 'internal/acp/stack.go', added: 1, removed: 1),
      ],
      diff: _stackGo,
    );

    // Once it has shipped, the footer names the pull request and what its
    // checks say (F5 decision 14).
    await pumpChanges(
      tester,
      model,
      shipped: 'Pull request #131 · 1 failing: unit',
    );
    expect(find.text('Pull request #131 · 1 failing: unit'), findsOneWidget);

    // Before a ship there is nothing to say, and nothing is drawn.
    await pumpChanges(tester, model);
    expect(find.textContaining('Pull request'), findsNothing);
  });

  testWidgets('a failed read says so in words, inset', (tester) async {
    // A bridge whose answer is not the shape the client read it as: the read
    // fails with a TypeError, whose own text names a Dart type.
    final model = ChangesModel(BridgeClient(_Unreadable()), agentId: 'a-1');
    await model.refresh();
    expect(model.status, ChangesStatus.failed);

    await pumpChanges(tester, model);

    // §7: what failed, in plain words — never the exception's own text.
    expect(
      find.text(
        'The bridge sent an answer this Studio cannot read. The bridge and '
        'the Studio may be different versions.',
      ),
      findsOneWidget,
    );
    expect(find.textContaining('TypeError'), findsNothing);

    // And the message sits on the tab's own inset, as the ready content does
    // (f4-inspector.md decision 5): its mark starts a Space.s16 in.
    final tab = tester.getTopLeft(find.byType(ChangesTab)).dx;
    final mark = tester
        .getTopLeft(
          find.descendant(
            of: find.byType(TabMessage),
            matching: find.byIcon(Glyphs.exclamationmark_circle),
          ),
        )
        .dx;
    expect(mark - tab, Space.s16);
  });
}

/// A bridge whose every answer is the wrong shape: each request raises the
/// failure a decode does, so the tab shows what a version mismatch looks like.
class _Unreadable implements Transport {
  @override
  Future<Response> send(Request request) async => throw _decodeFailure();

  @override
  Stream<String> lines(Request request) =>
      throw StateError('the Changes tab opens no stream');
}

/// The failure a decode raises: a value that is not the shape the contract
/// read it as (a null where the contract's `List` was expected).
Object _decodeFailure() {
  try {
    final Object? value = null;
    return value as List<Object?>;
  } on TypeError catch (error) {
    return error;
  }
}
