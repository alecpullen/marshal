import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/model/inspector_state.dart';
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/thread/review_head.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';
import 'package:marshal_app/src/ui/thread/transcript.dart';

import '../../support/kit.dart';
import '../../support/pulls.dart';
import '../thread/support.dart' show fixtureStore;

/// The review head (F5 decision 13): a ready thread opens with its pull
/// request's checks by name, its reviews, the report and the four actions; a
/// failing one opens with the card that fixes it; and Send back says so when
/// the agent that shipped it has ended.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  /// A thread screen over an empty transcript: the head is what these tests
  /// are about. A test that needs the conversation under the head seeds
  /// [store].
  Widget thread({
    required PullView pull,
    StackStore? store,
    InspectorState? inspector,
    VoidCallback? onSendBack,
    VoidCallback? onMerge,
    VoidCallback? onReviewChanges,
    PrActions? pullActions,
  }) => ThreadScreen(
    thread: threadRef(),
    store:
        store ??
        StackStore.seeded(
          const contract.StackSnapshot(
            sessionID: 's-1',
            rev: 1,
            roots: [],
            nodes: [],
          ),
        ),
    view: ThreadViewState(),
    inspector: inspector,
    now: now,
    pull: pull,
    pullActions: pullActions,
    onSendBack: onSendBack,
    onMerge: onMerge,
  );

  testWidgets('ready thread opens with the review head', (tester) async {
    await pumpKit(tester, thread(pull: pullView()), width: 900, height: 1000);

    expect(find.byType(ReviewHead), findsOneWidget);
    expect(find.text('READY FOR REVIEW'), findsOneWidget);
    expect(find.text('#131'), findsOneWidget);
    // Each check by name, not only a strip of blocks.
    expect(find.text('build'), findsOneWidget);
    expect(find.text('unit'), findsOneWidget);
    expect(find.text('2 reviews passed'), findsOneWidget);
    expect(find.textContaining('head a1b2c3d'), findsOneWidget);
    // The four things a person does with it, at most one primary.
    expect(find.text('Review changes'), findsOneWidget);
    expect(find.text('Merge'), findsOneWidget);
    expect(find.text('Open on forge'), findsOneWidget);
    expect(find.text('Send back'), findsOneWidget);
    expect(
      tester.widget<Btn>(find.widgetWithText(Btn, 'Review changes')).primary,
      isTrue,
    );
  });

  testWidgets('the report is shown when the record carries one', (
    tester,
  ) async {
    await pumpKit(
      tester,
      thread(pull: pullView(report: 'The clock crossed midnight in the test.')),
      width: 900,
      height: 1000,
    );

    expect(find.text('Engineer’s report'), findsOneWidget);
    expect(
      find.text('The clock crossed midnight in the test.'),
      findsOneWidget,
    );

    // Without one the section is not drawn at all.
    await pumpKit(tester, thread(pull: pullView()), width: 900, height: 1000);
    expect(find.text('Engineer’s report'), findsNothing);
  });

  testWidgets('the review head is as wide as the reading column', (
    tester,
  ) async {
    await pumpKit(
      tester,
      thread(pull: pullView(), store: fixtureStore()),
      width: 1440,
      height: 1000,
    );

    // A wide window: the head is the transcript's width, centred over it,
    // rather than spanning the page edge to edge.
    final head = tester.getRect(find.byType(ReviewHead));
    final transcript = tester.getRect(find.byType(Transcript));
    expect(head.width, Layout.reading);
    expect(head.center.dx, moreOrLessEquals(transcript.center.dx));
  });

  testWidgets('review changes expands the inspector on Changes', (
    tester,
  ) async {
    final inspector = InspectorState(
      width: InspectorWidth.collapsed,
      tab: InspectorTab.files,
    );
    addTearDown(inspector.dispose);
    await pumpKit(
      tester,
      thread(pull: pullView(), inspector: inspector),
      width: 900,
      height: 1000,
    );

    await tester.tap(find.text('Review changes'));
    await tester.pump();

    expect(inspector.tab, InspectorTab.changes);
    expect(
      inspector.width,
      InspectorWidth.docked,
      reason: 'a collapsed inspector opens on Changes',
    );
  });

  testWidgets('failing thread opens with the card', (tester) async {
    var fixed = 0;
    await pumpKit(
      tester,
      thread(
        pull: pullView(
          group: PullGroup.failing,
          checks: const [
            PullCheckView('build', PullCheckState.passed),
            PullCheckView('unit', PullCheckState.failed),
          ],
          failureTail: '--- FAIL: TestScriptedRecorderQuietMode (4.21s)',
        ),
        pullActions: PrActions(fix: () => fixed++),
      ),
      width: 900,
      height: 1000,
    );

    expect(find.byType(PrCard), findsOneWidget);
    expect(find.byType(ReviewHead), findsNothing);
    expect(find.text('1 failing: unit'), findsOneWidget);
    expect(
      find.text('--- FAIL: TestScriptedRecorderQuietMode (4.21s)'),
      findsOneWidget,
    );

    await tester.tap(find.text('Fix with a worker'));
    expect(fixed, 1, reason: 'one click, from the head of the thread');
  });

  testWidgets('send back is disabled when the agent has ended', (tester) async {
    await pumpKit(
      tester,
      thread(pull: pullView(), onSendBack: null),
      width: 900,
      height: 1000,
    );

    final sendBack = tester.widget<Btn>(find.widgetWithText(Btn, 'Send back'));
    expect(sendBack.onTap, isNull);
    expect(
      hasTip(
        tester,
        find.widgetWithText(Btn, 'Send back'),
        'The agent that shipped this has ended, so there is nothing to send '
        'it back to',
      ),
      isTrue,
      reason: 'a disabled button says why',
    );
  });

  testWidgets('send back is offered while the agent can hear it', (
    tester,
  ) async {
    var sent = 0;
    await pumpKit(
      tester,
      thread(pull: pullView(), onSendBack: () => sent++),
      width: 900,
      height: 1000,
    );

    await tester.tap(find.text('Send back'));
    expect(sent, 1);
  });
}
