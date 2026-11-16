import 'package:flutter/services.dart';
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
      NewWorkIssue('42', 'Fix the login redirect'),
    ],
    recipes: () async => const [
      NewWorkRecipe('Fix a bug', 'Find the bug and add a test.'),
    ],
    spawn: (request) async {
      spawns.add(request);
      return const SpawnResult(agentID: 'a-9');
    },
  );
  return (model: model, spawns: spawns);
}

void main() {
  testWidgets('the prompt comes first, with every chip beneath it', (
    tester,
  ) async {
    final (:model, spawns: _) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    final promptY = tester.getTopLeft(find.text('What should it do?')).dy;
    final chipY = tester.getTopLeft(find.textContaining('Base branch:')).dy;
    expect(promptY, lessThan(chipY));
    expect(find.text('Start'), findsOneWidget);
    expect(find.text('Cancel'), findsOneWidget);

    model.dispose();
  });

  testWidgets('planned kinds are drawn disabled, naming F6', (tester) async {
    final (:model, spawns: _) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    final plan = tester.widget<Btn>(find.widgetWithText(Btn, 'Plan first'));
    final item = tester.widget<Btn>(find.widgetWithText(Btn, 'Work item'));
    expect(plan.onTap, isNull);
    expect(item.onTap, isNull);
    expect(hasTip(tester, find.text('Plan first'), 'Planned for F6'), isTrue);
    expect(hasTip(tester, find.text('Work item'), 'Planned for F6'), isTrue);

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

  testWidgets('a chip opens its menu, and the lists open theirs', (
    tester,
  ) async {
    final (:model, spawns: _) = _dialog();
    await pumpKit(tester, newWorkDialog(model), height: 900);

    await tester.tap(find.textContaining('Base branch:'));
    await tester.pumpAndSettle();
    expect(find.text('main'), findsOneWidget);
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();

    await tester.tap(find.text('Recent prompts'));
    await tester.pumpAndSettle();
    expect(find.text('Clean up the fixtures'), findsOneWidget);

    model.dispose();
  });

  testWidgets('an empty list says so instead of offering nothing', (
    tester,
  ) async {
    final model = NewWorkModel(
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
    );
    await pumpKit(tester, newWorkDialog(model), height: 900);

    await tester.tap(find.text('Issues'));
    await tester.pumpAndSettle();
    expect(find.text('No issues for this project'), findsOneWidget);

    model.dispose();
  });
}
