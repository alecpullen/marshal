import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/run_model.dart';
import 'package:marshal_app/src/ui/factory/run_screen.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../../support/kit.dart';

/// `RunScreen`'s gate (design-system.md §5, §9): the question a run stopped on
/// is answered through the app's seam, and a run opened without one says so
/// instead of offering a tap that would post nothing.
void main() {
  final now = DateTime.utc(2026, 11, 17, 4, 12);

  /// A run that stopped on a question at task 2.
  Map<String, dynamic> stopped() => {
    'active': true,
    'planName': 'F6 plan 2',
    'branch': 'sdd/run-1',
    'totalTasks': 2,
    'doneTasks': 1,
    'currentTask': 2,
    'phase': 'verify',
    'fixRound': 0,
    'maxFixRounds': 3,
    'tokensUsed': 0,
    'tokensMax': 200000,
    'finished': false,
    'succeeded': false,
    'startedAt': 1763351900000,
    'tasks': [
      {
        'n': 1,
        'title': 'Read the item',
        'dependsOn': <int>[],
        'status': 'done',
        'startedAt': 1763351900000,
        'endedAt': 1763351990000,
        'commit': {'base': 'base-1', 'head': 'abcdef1234567'},
        'fixRounds': 0,
        'stages': [
          {'name': 'implement', 'state': 'done'},
        ],
      },
      {
        'n': 2,
        'title': 'Approve it',
        'dependsOn': [1],
        'status': 'active',
        'startedAt': 1763352000000,
        'fixRounds': 0,
        'stages': [
          {'name': 'implement', 'state': 'active'},
        ],
      },
    ],
    'gate': {
      'taskN': 2,
      'question': 'Should the fixtures be regenerated in the same commit?',
    },
  };

  RunModel run({Future<void> Function(String answer)? answer}) =>
      RunModel.fromDigest('a-run', stopped(), answer: answer);

  Future<void> pump(WidgetTester tester, RunModel model) async {
    tester.view.physicalSize = const Size(1440, 2400);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: RunScreen(model: model, now: now),
      ),
    );
    await tester.pump();
  }

  /// The gate's one action, as the card draws it.
  AskAction answerOf(WidgetTester tester) =>
      tester.widget<AskCard>(find.byType(AskCard)).actions.single;

  testWidgets('a run with the answer seam offers the one action', (
    tester,
  ) async {
    final model = run(answer: (words) async {});
    addTearDown(model.dispose);
    await pump(tester, model);

    expect(model.hasGate, isTrue);
    expect(model.canAnswer, isTrue);
    final action = answerOf(tester);
    expect(action.label, 'Answer');
    expect(action.onTap, isNotNull);
    expect(action.note, isNull, reason: 'there is nothing to explain');
  });

  testWidgets('one repair round is singular in its cell', (tester) async {
    final digest = stopped();
    digest['tasks'] = [
      {
        'n': 1,
        'title': 'Read the item',
        'dependsOn': <int>[],
        'status': 'active',
        'startedAt': 1763351900000,
        'fixRounds': 1,
        'stages': [
          {'name': 'implement', 'state': 'done'},
          {'name': 'review', 'state': 'failed'},
        ],
      },
    ];
    final model = RunModel.fromDigest('a-run', digest);
    addTearDown(model.dispose);
    await pump(tester, model);

    // One round is "1 round": the cell's note counts rounds, not a plural.
    expect(find.text('1 round'), findsOneWidget);
    expect(find.text('1 rounds'), findsNothing);
  });

  testWidgets('a run without the seam says why it cannot be answered', (
    tester,
  ) async {
    final model = run();
    addTearDown(model.dispose);
    await pump(tester, model);

    // The run is still drawn, and still says what the question is.
    expect(model.hasGate, isTrue);
    expect(model.canAnswer, isFalse);
    expect(
      find.text('Should the fixtures be regenerated in the same commit?'),
      findsOneWidget,
    );

    // Its one action is disabled, and the reason is the action's tooltip (§9)
    // rather than a tap that would open a sheet and post nothing.
    final action = answerOf(tester);
    expect(action.onTap, isNull);
    expect(action.note, isNotNull);
    expect(hasTip(tester, find.text('Answer'), action.note!), isTrue);
    expect(
      tester.widget<Btn>(find.widgetWithText(Btn, 'Answer')).onTap,
      isNull,
    );

    await tester.tap(find.text('Answer'), warnIfMissed: false);
    await tester.pump();
    expect(
      find.text('Answer the run'),
      findsNothing,
      reason: 'no sheet opens for an answer that could not be posted',
    );
  });
}
