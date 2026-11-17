import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/handoff_model.dart';
import 'package:marshal_app/src/ui/factory/handoff_sheet.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// A handoff for a project whose defaults name no repository: the sheet opens
/// blocked, which is what a person fixes first.
HandoffModel _handoff({
  List<FactoryCreate>? posts,
  ProjectDefaults defaults = const ProjectDefaults(maxRounds: 3),
  String plan = '## Task 1\nRead the item.\n## Task 2\nApprove it.\n',
}) => HandoffModel(
  defaults: defaults,
  projectRoot: '/home/u/marshal',
  title: 'Work item thread',
  plan: plan,
  spec: 'Ship the work item thread.',
  repositories: const ['repo-1', 'repo-2'],
  create: (draft) async {
    posts?.add(draft);
    return FactoryItem.fromJson({
      'id': 'item-9',
      'title': draft.title,
      'state': 'draft',
      'stage': 'plan',
      'repoId': draft.config.repoID,
      'projectRoot': draft.config.projectRoot,
      'hostId': draft.config.hostID,
      'branch': 'factory/item-9',
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
  },
);

Widget _sheet(HandoffModel model) => EmberApp(
  brightness: Brightness.light,
  home: Builder(builder: (context) => handoffSheet(context, model)),
);

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
    // The consequence states what approving authorises.
    expect(find.textContaining('repo-1'), findsWidgets);
  });

  testWidgets('approve is blocked with a reason', (tester) async {
    final model = _handoff();
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    final approve = tester.widget<Btn>(
      find.widgetWithText(Btn, 'Approve and start'),
    );
    expect(approve.onTap, isNull);
    expect(find.textContaining('repository'), findsWidgets);
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
    final rounds = find.widgetWithText(Field, HandoffField.rounds);
    await tester.enterText(
      find.descendant(of: rounds, matching: find.byType(EditableText)),
      '7',
    );
    await tester.pump();

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
    final repo = find.widgetWithText(Field, HandoffField.repository);
    await tester.enterText(
      find.descendant(of: repo, matching: find.byType(EditableText)),
      'repo-1',
    );
    await tester.pump();

    final approve = tester.widget<Btn>(
      find.widgetWithText(Btn, 'Approve and start'),
    );
    expect(approve.onTap, isNotNull);

    await tester.runAsync(() => model.approve());
    await tester.pump();

    expect(posts, hasLength(1));
    expect(model.created?.id, 'item-9');
  });
}
