import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/handoff_model.dart';
import 'package:marshal_app/src/model/planning_model.dart';
import 'package:marshal_app/src/ui/factory/handoff_sheet.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// The factory item a route answers with, built from the draft it was handed.
FactoryItem _item(String id, FactoryCreate draft) => FactoryItem.fromJson({
  'id': id,
  'title': draft.title,
  'state': 'draft',
  'stage': 'plan',
  'repoId': draft.config.repoID,
  'projectRoot': draft.config.projectRoot,
  'hostId': draft.config.hostID,
  'branch': 'factory/$id',
  'baseSha': 'base-1',
  'version': 1,
  'draft': {
    'requestId': draft.requestID,
    'title': draft.title,
    'plan': draft.plan,
    'spec': draft.spec,
    'config': draft.config.toJson(),
  },
  'approvals': <Object?>[],
  'grants': <Object?>[],
  'rounds': <Object?>[],
  'workers': <Object?>[],
  'prs': <Object?>[],
  'effects': <Object?>[],
  'history': <Object?>[],
  'createdAt': '2026-11-17T04:00:00Z',
  'approvedAt': '',
  'updatedAt': '2026-11-17T04:00:00Z',
});

/// The capture a planning thread froze: two files, one refused path.
const _snapshot = PlannerSnapshot(
  id: 'snap-1',
  sha256: 'a1b2c3d4e5f6',
  baseCommit: 'c0ffee1',
  plan: '## Task 1\nFrozen plan.\n',
  spec: 'Frozen specification.',
  files: [
    PlannerSnapshotFile(
      path: 'lib/a.dart',
      operation: 'modify',
      baseSHA256: 'b1',
      contentSHA256: 'c1',
      executable: false,
      diff: '--- a/lib/a.dart\n+++ b/lib/a.dart\n',
    ),
    PlannerSnapshotFile(
      path: 'lib/new.dart',
      operation: 'create',
      contentSHA256: 'c2',
      executable: false,
      diff: '--- /dev/null\n+++ b/lib/new.dart\n',
    ),
  ],
  rejected: [
    PlannerRejection(path: '../etc/passwd', reason: 'outside the project'),
  ],
);

/// A capture that froze the documents only: nothing was selected.
const _documentsOnly = PlannerSnapshot(
  id: 'snap-2',
  sha256: 'ffee0099',
  baseCommit: 'd00d1e',
  plan: '## Task 1\nFrozen plan.\n',
  spec: 'Frozen specification.',
);

/// A handoff for a project whose defaults name no repository: the sheet opens
/// blocked, which is what a person fixes first.
HandoffModel _handoff({
  List<FactoryCreate>? posts,
  ProjectDefaults defaults = const ProjectDefaults(maxRounds: 3),
  String plan = '## Task 1\nRead the item.\n## Task 2\nApprove it.\n',
  Future<FactoryItem> Function(FactoryCreate draft)? create,
  PlannerSnapshot? snapshot,
  String? plannerID,
  Future<FactoryItem> Function(
    String plannerID,
    String snapshotID,
    FactoryCreate request,
  )?
  handoffSnapshot,
}) => HandoffModel(
  defaults: defaults,
  projectRoot: '/home/u/marshal',
  title: 'Work item thread',
  plan: plan,
  spec: 'Ship the work item thread.',
  repositories: const ['repo-1', 'repo-2'],
  reviewedSnapshot: snapshot,
  plannerID: plannerID,
  handoffSnapshot: handoffSnapshot,
  create: (draft) async {
    posts?.add(draft);
    return create == null ? _item('item-9', draft) : create(draft);
  },
);

Widget _sheet(HandoffModel model) => EmberApp(
  brightness: Brightness.light,
  home: Builder(builder: (context) => handoffSheet(context, model)),
);

/// The sheet's one primary action.
Finder get _action => find.widgetWithText(Btn, 'Create the factory draft');

/// Types [value] into the field named [field].
Future<void> _enter(WidgetTester tester, String field, String value) async {
  final target = find.widgetWithText(Field, field);
  await tester.enterText(
    find.descendant(of: target, matching: find.byType(EditableText)),
    value,
  );
  await tester.pump();
}

void main() {
  testWidgets('every field is drawn in order, marked inherited', (
    tester,
  ) async {
    final model = _handoff(
      defaults: const ProjectDefaults(
        repoID: 'repo-1',
        implementerModel: 'local-fast',
        maxRounds: 4,
      ),
    );
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    // The fields appear in the order the plan states them.
    final names = [
      for (final field in HandoffField.all)
        if (field != HandoffField.scope) field,
    ];
    var last = -1.0;
    for (final name in names) {
      final found = tester.getTopLeft(find.text(name).first).dy;
      expect(
        found,
        greaterThanOrEqualTo(last),
        reason: '$name is drawn out of order',
      );
      last = found;
    }
    // Nothing has been overridden, so no Reset is offered.
    expect(find.text('Inherited'), findsWidgets);
    expect(find.text('Reset'), findsNothing);
    // The consequence states what the action does, and the repository it
    // lands in.
    expect(find.textContaining('repo-1'), findsWidgets);
  });

  testWidgets('the action names the create and says work has not started', (
    tester,
  ) async {
    final model = _handoff(defaults: const ProjectDefaults(repoID: 'repo-1'));
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    // Creating a factory draft is not starting work, and the label says so.
    expect(_action, findsOneWidget);
    expect(find.text('Approve and start'), findsNothing);
    // "Label" is the kit's uppercase section label (§3).
    expect(find.text('WHAT CREATING THE DRAFT DOES'), findsOneWidget);
    expect(find.textContaining('This creates a factory draft'), findsOneWidget);
    expect(
      find.textContaining(
        'Work starts when the item is approved in the factory',
      ),
      findsOneWidget,
    );
  });

  testWidgets('approve is blocked with a reason', (tester) async {
    final model = _handoff();
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    final approve = tester.widget<Btn>(_action);
    expect(approve.onTap, isNull);
    expect(find.textContaining('repository'), findsWidgets);
    // The tooltip is the same blocker, so the reason is on the control too.
    final tip = tester.widget<Tip>(
      find.ancestor(of: _action, matching: find.byType(Tip)),
    );
    expect(tip.message, 'Choose the repository the work is on.');
  });

  testWidgets('an override is marked and a reset returns to inherited', (
    tester,
  ) async {
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1', maxRounds: 3),
    );
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    // The round allowance is typed into, which marks it overridden.
    await _enter(tester, HandoffField.rounds, '7');

    expect(model.sourceOf(HandoffField.rounds), FieldSource.overridden);
    expect(find.text('Overridden'), findsOneWidget);

    // Reset is the sheet's own control; it puts the project's value back, and
    // the field follows it.
    expect(find.text('Reset'), findsOneWidget);
    model.reset(HandoffField.rounds);
    await tester.pump();

    expect(model.sourceOf(HandoffField.rounds), FieldSource.inherited);
    expect(model.draft.config.maxRounds, 3);
    expect(find.text('Reset'), findsNothing);
  });

  testWidgets('approve creates the item once the blocker is gone', (
    tester,
  ) async {
    final posts = <FactoryCreate>[];
    final model = _handoff(posts: posts);
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    // The repository is the one thing the project did not state.
    await _enter(tester, HandoffField.repository, 'repo-1');

    final approve = tester.widget<Btn>(_action);
    expect(approve.onTap, isNotNull);

    // The action the person presses, not the model behind it: the button's own
    // wiring is what posts the draft.
    await tester.tap(_action);
    await tester.pump();
    await tester.pump();

    expect(posts, hasLength(1));
    expect(model.created?.id, 'item-9');
  });

  testWidgets('the frozen capture is the evidence the sheet shows', (
    tester,
  ) async {
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      snapshot: _snapshot,
      plannerID: 'planner-7',
      handoffSnapshot: (plannerID, snapshotID, request) async =>
          _item('item-frozen', request),
    );
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    // The capture's own identity.
    expect(find.text('THE FROZEN CAPTURE'), findsOneWidget);
    expect(find.textContaining('Snapshot snap-1'), findsOneWidget);
    expect(find.textContaining('digest a1b2c3d4'), findsOneWidget);
    expect(find.textContaining('Base commit c0ffee1'), findsOneWidget);
    // Every captured file with its operation, and its diff in the kit's own
    // block rather than a hand-drawn one. A path is set in mono, so it is its
    // own text rather than a combined sentence.
    expect(find.text('lib/a.dart'), findsOneWidget);
    expect(find.text('Modified'), findsOneWidget);
    expect(find.text('lib/new.dart'), findsOneWidget);
    expect(find.text('Created'), findsOneWidget);
    // The plan and the specification the capture froze are drawn, alongside the
    // two file diffs: the text that travels is shown, not only described.
    expect(find.byType(OutputBlock), findsNWidgets(4));
    expect(find.textContaining('Frozen plan.'), findsOneWidget);
    expect(find.textContaining('Frozen specification.'), findsOneWidget);
    expect(find.textContaining('+++ b/lib/a.dart'), findsOneWidget);
    // Every path the capture refused, with the reason.
    expect(find.text('NOT CAPTURED'), findsOneWidget);
    expect(find.text('../etc/passwd'), findsOneWidget);
    expect(find.textContaining('outside the project'), findsOneWidget);
    // The consequence names the capture and what a change would need.
    expect(find.textContaining('frozen capture snap-1'), findsOneWidget);
    expect(find.textContaining('fresh capture'), findsWidgets);
  });

  testWidgets('a documents-only capture selects no file', (tester) async {
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      snapshot: _documentsOnly,
      plannerID: 'planner-7',
      handoffSnapshot: (plannerID, snapshotID, request) async =>
          _item('item-frozen', request),
    );
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    expect(find.textContaining('No files selected'), findsOneWidget);
    // Nothing is selected on the person's behalf: the draft binds an empty
    // selection, because the capture froze the documents only.
    expect(model.draft.editSnapshot, isNotNull);
    expect(model.draft.editSnapshot!.files, isEmpty);
    // The documents are drawn, and no file diff is.
    expect(find.textContaining('Frozen plan.'), findsOneWidget);
    expect(find.textContaining('+++ b/'), findsNothing);
  });

  testWidgets('an unbound review says the plan text is what is sent', (
    tester,
  ) async {
    final model = _handoff(defaults: const ProjectDefaults(repoID: 'repo-1'));
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    expect(find.text('WHAT WILL BE SENT'), findsOneWidget);
    expect(
      find.textContaining('No capture is bound to this review'),
      findsOneWidget,
    );
    expect(find.text('THE FROZEN CAPTURE'), findsNothing);
  });

  testWidgets('a failed create leaves the sheet open with the failure', (
    tester,
  ) async {
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      create: (draft) async =>
          throw StateError('the bridge refused the create'),
    );
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    await tester.tap(_action);
    await tester.pump();
    await tester.pump();

    // The sheet is still there, saying what failed: nothing retries by itself.
    expect(_action, findsOneWidget);
    expect(
      find.textContaining('the bridge refused the create'),
      findsOneWidget,
    );
    expect(model.created, isNull);
  });

  // The sheet grew a tall evidence block. It is not a registered screen, so
  // overflow_test.dart never pumps it: these two sizes are its only proof that
  // the capture review fits a phone and a desktop.
  testWidgets('the capture review fits a phone and a desktop', (tester) async {
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1'),
      snapshot: _snapshot,
      plannerID: 'planner-7',
      handoffSnapshot: (plannerID, snapshotID, request) async =>
          _item('item-frozen', request),
    );
    addTearDown(model.dispose);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);

    tester.view.physicalSize = const Size(390, 780);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();
    expect(tester.takeException(), isNull, reason: 'a phone overflows');

    tester.view.physicalSize = const Size(1440, 900);
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull, reason: 'a desktop overflows');
  });
}
