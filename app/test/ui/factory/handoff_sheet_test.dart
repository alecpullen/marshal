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

  testWidgets('a checks line the parser refuses is kept as typed', (
    tester,
  ) async {
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1', maxRounds: 3),
    );
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    const typed = 'check_run:build:42\nbuild without a kind';
    final checks = find.widgetWithText(Field, HandoffField.checks);
    await tester.enterText(
      find.descendant(of: checks, matching: find.byType(EditableText)),
      typed,
    );
    await tester.pump();

    // The parser's own words sit under the field, as the project editor's do.
    expect(
      find.textContaining('"build without a kind" is neither'),
      findsWidgets,
    );

    // Anything else in the sheet notifying — a rebuild — must not rewrite
    // what was typed with the checks that happened to parse.
    model.set(HandoffField.rounds, 5);
    await tester.pump();

    expect(
      tester
          .widget<EditableText>(
            find.descendant(of: checks, matching: find.byType(EditableText)),
          )
          .controller
          .text,
      typed,
    );
    expect(model.checksText, typed);
  });

  testWidgets('checks text that parses is written exactly as before', (
    tester,
  ) async {
    final model = _handoff(
      defaults: const ProjectDefaults(repoID: 'repo-1', maxRounds: 3),
    );
    addTearDown(model.dispose);
    await tester.pumpWidget(_sheet(model));
    await tester.pump();

    final checks = find.widgetWithText(Field, HandoffField.checks);
    await tester.enterText(
      find.descendant(of: checks, matching: find.byType(EditableText)),
      'check_run:build:42',
    );
    await tester.pump();

    expect(find.textContaining('is neither'), findsNothing);
    expect(model.draftContent.checksMode, 'custom');
    expect(model.draftContent.expectedChecksText, 'check_run:build:42');
    expect(
      model.draft.config.expectedChecks.map(
        (check) => (check.kind, check.name, check.appID),
      ),
      [('check_run', 'build', 42)],
    );
    expect(model.canApprove, isTrue);
  });

  testWidgets(
    'the acknowledgement is the sheet\u2019s, and clears stated checks',
    (tester) async {
      final model = _handoff(
        defaults: const ProjectDefaults(repoID: 'repo-1', maxRounds: 3),
      );
      addTearDown(model.dispose);
      // Tall enough that the checks row's controls are where a hand can reach
      // them, as they are in a real sheet.
      tester.view.physicalSize = const Size(1000, 1400);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.reset);
      await tester.pumpWidget(_sheet(model));
      await tester.pump();

      // The one control that can state the acknowledgement, in plain words.
      await tester.tap(find.text('Acknowledge no check is required'));
      await tester.pump();

      expect(model.noRequiredCIAcknowledged, isTrue);
      expect(model.draftContent.checksMode, 'none');
      expect(model.draft.config.noRequiredCIAcknowledged, isTrue);
      expect(model.draft.config.expectedChecks, isEmpty);
      expect(model.draftContent.expectedChecksText, '');
      // The sheet says what the acknowledgement means, and the stated-checks
      // field is out of play while it stands: no control claims both.
      expect(
        find.text(
          'This work needs no CI check to pass, so stated checks are unused.',
        ),
        findsOneWidget,
      );
      expect(
        tester
            .widget<Field>(find.widgetWithText(Field, HandoffField.checks))
            .enabled,
        isFalse,
      );

      await tester.tap(find.text('Withdraw the acknowledgement'));
      await tester.pump();

      expect(model.noRequiredCIAcknowledged, isFalse);
      expect(model.draftContent.checksMode, 'inherit');
      expect(
        tester
            .widget<Field>(find.widgetWithText(Field, HandoffField.checks))
            .enabled,
        isTrue,
      );
    },
  );
}
