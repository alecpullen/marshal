import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/new_work/new_work_dialog.dart';

import '../../support/kit.dart';

/// A dialog whose routes only record what they were asked.
({NewWorkModel model, List<SpawnRequest> spawns}) _dialog() {
  final spawns = <SpawnRequest>[];
  final model = NewWorkModel(
    defaults: NewWorkDefaults.fromProject(
      const ProjectStatus(
        root: '/home/u/marshal',
        available: true,
        trust: TrustState.trustTrusted,
        isolation: 'available',
      ),
    ),
    recentPrompts: () async =>
        const RecentPrompts(prompts: ['Clean up the fixtures']),
    issues: (project) async => const [
      RepoIssue(
        number: 42,
        title: 'Fix the login redirect',
        body: 'The return path is lost.',
        url: 'https://github.com/owner/name/issues/42',
      ),
    ],
    recipes: () async => const [
      Recipe(
        name: 'fix-a-bug',
        title: 'Fix a bug',
        kind: 'prompt',
        prompt: 'Find the bug and add a test.',
      ),
    ],
    spawn: (request) async {
      spawns.add(request);
      return const SpawnResult(agentID: 'a-9');
    },
    // The sets a real project offers: the two modes the bridge's mode route
    // takes, and the two workspaces an isolating project allows.
    options: {
      'Permission mode': const ['Ask'],
      'Workspace': const ['Project'],
    },
    notes: {
      'Base branch': 'The project default branch; F6 brings the choice',
      'Environment': 'A local spawn; F6 brings the choice',
      'Model': 'The project default; F6 brings the models by role',
      'Host': 'The only host that answers with a project',
    },
  );
  return (model: model, spawns: spawns);
}

/// A dialog whose three lists are all empty, for the empty-state tests.
NewWorkModel _empty({required bool issuesFromRepo}) => NewWorkModel(
  defaults: NewWorkDefaults.fromProject(
    const ProjectStatus(
      root: '/home/u/marshal',
      available: true,
      trust: TrustState.trustTrusted,
      isolation: 'unavailable',
    ),
  ),
  recentPrompts: () async => const RecentPrompts(prompts: []),
  issues: (project) async => const [],
  recipes: () async => const [],
  spawn: (request) async => const SpawnResult(agentID: 'a-9'),
  issuesFromRepo: issuesFromRepo,
);

/// One host's routes for a dialog that may start on several, named by the
/// agent it would start (P8 plan 2, decision 6).
NewWorkRoutes _routesFor(String host) => NewWorkRoutes(
  project: '/home/u/marshal',
  recentPrompts: () async =>
      const RecentPrompts(prompts: ['Clean up the fixtures']),
  issues: (project) async => const [],
  recipes: () async => const [],
  spawn: (request) async => SpawnResult(agentID: 'a-$host'),
  projects: () async => const ['/home/u/marshal'],
);

void main() {
  testWidgets('the prompt comes first, with every chip beneath it', (
    tester,
  ) async {
    final (:model, spawns: _) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    final promptY = tester
        .getTopLeft(find.text('What should it do? (optional)'))
        .dy;
    final chipY = tester.getTopLeft(find.textContaining('Base branch:')).dy;
    expect(promptY, lessThan(chipY));
    expect(find.text('Start'), findsOneWidget);
    expect(find.text('Cancel'), findsOneWidget);

    model.dispose();
  });

  testWidgets('all three kinds are live choices', (tester) async {
    final (:model, spawns: _) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    // F6 decisions 8 and 9: an agent spawns, "Plan first" starts a planning
    // thread, and "Work item" opens the handoff review.
    for (final word in ['Agent', 'Plan first', 'Work item']) {
      expect(
        tester.widget<Btn>(find.widgetWithText(Btn, word)).onTap,
        isNotNull,
        reason: '$word is drawn disabled',
      );
    }

    model.dispose();
  });

  testWidgets('the prompt is optional only for an Agent', (tester) async {
    final (:model, spawns: _) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    expect(find.text('What should it do? (optional)'), findsOneWidget);

    for (final word in ['Plan first', 'Work item']) {
      await tester.tap(find.widgetWithText(Btn, word));
      await tester.pumpAndSettle();
      expect(
        find.text('What should it do?'),
        findsOneWidget,
        reason: '$word requires a prompt',
      );
      expect(find.text('What should it do? (optional)'), findsNothing);
    }

    await tester.tap(find.widgetWithText(Btn, 'Agent'));
    await tester.pumpAndSettle();
    expect(find.text('What should it do? (optional)'), findsOneWidget);

    model.dispose();
  });

  testWidgets('a blank required prompt is the field error and clears', (
    tester,
  ) async {
    final (:model, spawns: _) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    for (final word in ['Plan first', 'Work item']) {
      await tester.tap(find.widgetWithText(Btn, word));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Start'));
      await tester.pumpAndSettle();

      expect(
        tester.widget<Field>(find.byType(Field)).error,
        contains('Write what it should do'),
        reason: '$word puts the refusal on the prompt',
      );
      expect(
        find.textContaining('Write what it should do'),
        findsOneWidget,
        reason: 'the refusal is drawn once, under the prompt',
      );

      await tester.enterText(find.byType(EditableText), 'Review the handoff');
      await tester.pumpAndSettle();
      expect(
        tester.widget<Field>(find.byType(Field)).error,
        isNull,
        reason: 'typing clears the refusal',
      );
      expect(find.textContaining('Write what it should do'), findsNothing);

      await tester.enterText(find.byType(EditableText), '');
      await tester.pumpAndSettle();
      await tester.tap(find.text('Start'));
      await tester.pumpAndSettle();
      expect(find.textContaining('Write what it should do'), findsOneWidget);

      await tester.tap(find.widgetWithText(Btn, 'Agent'));
      await tester.pumpAndSettle();
      expect(
        find.textContaining('Write what it should do'),
        findsNothing,
        reason: 'an agent needs no prompt at all',
      );
    }

    model.dispose();
  });

  testWidgets('typing fills the prompt, and Start posts it', (tester) async {
    final (:model, :spawns) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    await tester.enterText(find.byType(EditableText), 'Fix the login redirect');
    await tester.pumpAndSettle();
    expect(model.prompt, 'Fix the login redirect');

    await tester.tap(find.text('Start'));
    await tester.pumpAndSettle();

    expect(spawns.single.prompt, 'Fix the login redirect');
    expect(spawns.single.project, '/home/u/marshal');

    model.dispose();
  });

  testWidgets('Start with the box blank spawns an agent to talk to', (
    tester,
  ) async {
    final (:model, :spawns) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    // The prompt is optional for an agent: it starts with nothing to do, and
    // its thread takes the first instruction.
    await tester.tap(find.text('Start'));
    await tester.pumpAndSettle();

    expect(spawns.single.project, '/home/u/marshal');
    expect(spawns.single.prompt, isNull);
    expect(model.failure, isNull);

    model.dispose();
  });

  testWidgets('a work item with no text says what it needs', (tester) async {
    final (:model, :spawns) = _dialog();
    String? workItem;
    await pumpKit(
      tester,
      newWorkDialog(model, onWorkItem: (prompt) => workItem = prompt),
      height: 900,
    );

    // A work item's plan is the prompt, so there is nothing to review yet:
    // the sheet says so and opens no review.
    await tester.tap(find.widgetWithText(Btn, 'Work item'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Start'));
    await tester.pumpAndSettle();

    expect(workItem, isNull, reason: 'no empty plan was handed off');
    expect(spawns, isEmpty);
    expect(find.textContaining('Write what it should do'), findsOneWidget);

    model.dispose();
  });

  testWidgets('a chip opens a menu that changes what Start posts', (
    tester,
  ) async {
    final (:model, :spawns) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    await tester.tap(find.textContaining('Permission mode:'));
    await tester.pumpAndSettle();
    // The menu offers the mode this agent does not start with, and it can be
    // chosen: the chip is not a menu of one value.
    expect(find.text('Ask'), findsOneWidget);

    await tester.tap(find.text('Ask'));
    await tester.pumpAndSettle();
    expect(model.choices['Permission mode'], 'Ask');
    expect(find.textContaining('Permission mode: Ask'), findsOneWidget);

    await tester.enterText(find.byType(EditableText), 'Do the work');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Start'));
    await tester.pumpAndSettle();
    expect(spawns.single.mode, 'ask');

    await tester.tap(find.text('Recent prompts'));
    await tester.pumpAndSettle();
    expect(find.text('Clean up the fixtures'), findsOneWidget);

    model.dispose();
  });

  testWidgets('a chip with nothing to choose is a pill naming its phase', (
    tester,
  ) async {
    final (:model, spawns: _) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    // No route lists a project's branches, so the chip is a pill with the
    // reason, never a menu whose only entry is the value already set.
    final pill = tester.widget<Pill>(
      find.widgetWithText(Pill, 'Base branch: The project default'),
    );
    expect(pill.tooltip, contains('F6'));
    expect(
      find.widgetWithText(Btn, 'Base branch: The project default'),
      findsNothing,
    );

    model.dispose();
  });

  testWidgets('an empty issue list says which kind of empty it is', (
    tester,
  ) async {
    // No repo was named, so the list cannot be asked at all: the dialog says
    // that rather than claiming the repo has no open issues.
    final withoutRepo = _empty(issuesFromRepo: false);
    await pumpKit(tester, newWorkDialog(withoutRepo), height: 900);
    await tester.tap(find.text('Issues'));
    await tester.pumpAndSettle();
    expect(find.text('No registered repo for this project'), findsOneWidget);
    expect(find.text('No issues for this project'), findsNothing);
    withoutRepo.dispose();
  });

  testWidgets('an asked, empty issue list says the repo has none', (
    tester,
  ) async {
    final asked = _empty(issuesFromRepo: true);
    await pumpKit(tester, newWorkDialog(asked), height: 900);
    await tester.tap(find.text('Issues'));
    await tester.pumpAndSettle();
    expect(find.text('No issues for this project'), findsOneWidget);
    expect(find.text('No registered repo for this project'), findsNothing);
    asked.dispose();
  });

  testWidgets('the host chip is a menu with two hosts', (tester) async {
    final model = NewWorkModel.forHosts(
      host: 'local',
      routes: {'local': _routesFor('local'), 'studio': _routesFor('studio')},
      project: const ProjectStatus(
        root: '/home/u/marshal',
        available: true,
        trust: TrustState.trustTrusted,
        isolation: 'available',
      ),
    );
    await pumpKit(tester, newWorkDialog(model), height: 900);

    // The chip names the host the dialog starts on, and opens a menu of every
    // host it may start on (spec D10).
    expect(find.textContaining('Host: local'), findsOneWidget);
    await tester.tap(find.textContaining('Host: local'));
    await tester.pumpAndSettle();
    expect(find.text('local'), findsOneWidget);
    expect(find.text('studio'), findsOneWidget);

    await tester.tap(find.text('studio'));
    await tester.pumpAndSettle();

    expect(model.host, 'studio');
    expect(find.textContaining('Host: studio'), findsOneWidget);
    model.dispose();
  });
}
