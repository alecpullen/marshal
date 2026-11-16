import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/model/planning_model.dart';
import 'package:marshal_app/src/ui/factory/planning_screen.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// A planner whose routes a test can inspect, with one conversation turn and
/// one question waiting on a person.
({PlanningModel model, List<String> prompts, List<Object?> answers}) _planner({
  String question = 'Should the inspector dock or open as a sheet?',
}) {
  final prompts = <String>[];
  final answers = <Object?>[];
  final model = PlanningModel(
    plannerID: 'p-1',
    events: (after) async => [
      PlannerEvent(
        sequence: 1,
        kind: 'turn.text',
        data: const {'text': 'The plan has two tasks.'},
      ),
      if (after < 2)
        PlannerEvent(
          sequence: 2,
          kind: 'turn.question',
          data: {
            'requestId': 'req-1',
            'method': 'session/request_question',
            'params': {
              'questions': [
                {'question': question},
              ],
            },
          },
        ),
    ],
    promptPlanner: (request) async {
      prompts.add(request.text);
      return PlannerTurnReply(
        requestID: request.requestID,
        state: 'dispatched',
      );
    },
    cancelPlanner: () async {},
    answerPlanner: (requestID, response) async => answers.add(response),
    capture: (request) async => PlannerSnapshotReply(
      id: 'snap-1',
      sha256: '9f2c1a55',
      baseCommit: 'base-1',
      artifacts: [
        PlannerSnapshotArtifact(
          kind: 'plan',
          content: '## Task 1\nRead the item.\n',
          sha256: 'a1',
        ),
      ],
      files: const [],
      createdAt: '2026-11-17T04:00:00Z',
      rejected: const [],
    ),
  );
  return (model: model, prompts: prompts, answers: answers);
}

Widget _screen(PlanningModel model, {StateKind? state}) => EmberApp(
  brightness: Brightness.light,
  home: PlanningScreen(
    plan: model,
    title: 'Planning',
    subtitle: 'Permissive · planning in w-1',
    state: state,
    now: DateTime.utc(2026, 11, 17, 4, 12),
    onHandToFactory: () {},
  ),
);

/// Pumps until the model's reads have landed.
Future<void> _settle(WidgetTester tester) async {
  for (var i = 0; i < 4; i++) {
    await tester.pump();
  }
}

void main() {
  testWidgets('prompt posts', (tester) async {
    final planner = _planner();
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    await tester.enterText(find.byType(EditableText), 'Plan the work item');
    await tester.pump();
    await tester.tap(find.text('Send'));
    await tester.pump();
    await _settle(tester);

    expect(planner.prompts, ['Plan the work item']);
    // The person's words are a row of the conversation, not a bubble and not
    // a second copy.
    expect(find.text('Plan the work item'), findsOneWidget);
  });

  testWidgets('a request is an ask', (tester) async {
    final planner = _planner(question: 'Which tab comes first?');
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    final card = tester.widget<AskCard>(find.byType(AskCard));
    expect(card.kind, AskKind.question);
    expect(card.title, 'Question from the planner');
    expect(card.body, 'Which tab comes first?');
    expect(find.text('Which tab comes first?'), findsWidgets);
  });

  testWidgets('snapshot fills the plan', (tester) async {
    final planner = _planner();
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    // Nothing is frozen until a capture is taken.
    expect(find.textContaining('No snapshot yet'), findsOneWidget);

    await tester.runAsync(
      () => planner.model.snapshot(plan: '## Task 1\nRead the item.\n'),
    );
    await _settle(tester);

    expect(find.textContaining('1 task'), findsOneWidget);
    expect(find.textContaining('## Task 1'), findsOneWidget);
  });

  testWidgets('the stale state keeps the conversation and marks it', (
    tester,
  ) async {
    final planner = _planner();
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model, state: StateKind.stale));
    await _settle(tester);

    final shown = tester
        .widgetList<StateMessage>(find.byType(StateMessage))
        .map((message) => message.kind);
    expect(shown, contains(StateKind.stale));
    expect(find.text('The plan has two tasks.'), findsOneWidget);
  });

  testWidgets('plan first opens a planning thread', (tester) async {
    final requests = <PlannerCreateRequest>[];
    final model = NewWorkModel(
      defaults: NewWorkDefaults.fromProject(
        const ProjectStatus(
          root: '/home/u/marshal',
          available: true,
          trust: TrustState.trustTrusted,
          isolation: 'available',
        ),
      ),
      recentPrompts: () async => const RecentPrompts(prompts: []),
      issues: (project) async => const [],
      recipes: () async => const [],
      spawn: (request) async => throw StateError('no agent should start'),
      plan: (request) async {
        requests.add(request);
        return PlannerView.fromJson({
          'id': 'p-9',
          'projectRoot': request.projectRoot,
          'repoId': request.repoID,
          'backendId': 'b-1',
          'workspaceId': 'w-1',
          'occupancy': 'free',
          'requestedMode': 'bypassPermissions',
          'modeConfirmed': true,
          'state': 'active',
          'live': true,
          'eventSequence': 0,
          'capabilities': {
            'prompt': true,
            'cancel': true,
            'permissionRequests': true,
            'sessionLoad': false,
            'workspaceEdits': true,
            'modes': ['bypassPermissions'],
          },
          'sessionLoad': false,
          'resumeAvailable': false,
          'createdAt': '2026-11-17T03:40:00Z',
          'updatedAt': '2026-11-17T04:00:00Z',
        });
      },
      repoId: 'repo-1',
    );
    addTearDown(model.dispose);

    model.prefill('Plan the work item');
    model.selectKind(NewWorkKind.planFirst);

    // The kind is now startable and does not spawn an agent: it starts a
    // planner against the project's repository.
    expect(model.canStart, isTrue);
    await model.start();

    expect(requests, hasLength(1));
    expect(requests.single.projectRoot, '/home/u/marshal');
    expect(requests.single.repoID, 'repo-1');
    expect(model.startedPlanner?.id, 'p-9');
    expect(model.started, isNull);
  });
}
