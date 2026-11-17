import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/handoff_model.dart';
import 'package:marshal_app/src/model/new_work_model.dart';
import 'package:marshal_app/src/model/planning_model.dart';
import 'package:marshal_app/src/ui/factory/handoff_sheet.dart';
import 'package:marshal_app/src/ui/factory/planning_screen.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// A planner whose routes a test can inspect, with one conversation turn and
/// one question waiting on a person.
///
/// [view] is the record the bridge answers a read with. No view is no read
/// route at all, which is the screen's unknown state rather than a guessed
/// one.
({PlanningModel model, List<String> prompts, List<Object?> answers}) _planner({
  String question = 'Should the inspector dock or open as a sheet?',
  PlannerView? view,
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
    read: view == null ? null : () async => view,
  );
  return (model: model, prompts: prompts, answers: answers);
}

/// A planner record as the bridge writes one: live and active in `w-1` under
/// a confirmed permissive mode, unless the arguments say otherwise.
PlannerView _view({
  String requestedMode = 'bypassPermissions',
  String? confirmedMode,
  bool modeConfirmed = true,
  String workspaceID = 'w-1',
  String state = 'active',
  bool live = true,
  String? endedAt,
  List<Map<String, Object?>> turns = const [],
}) => PlannerView.fromJson({
  'id': 'p-1',
  'projectRoot': '/home/u/marshal',
  'repoId': 'repo-1',
  'backendId': 'b-1',
  'workspaceId': workspaceID,
  'occupancy': 'free',
  'requestedMode': requestedMode,
  'confirmedMode': confirmedMode,
  'modeConfirmed': modeConfirmed,
  'state': state,
  'live': live,
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
  'endedAt': endedAt,
  'turns': turns,
});

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

/// Every pill label the screen draws, in order: the strip and the composer's
/// own chips.
List<String> _pills(WidgetTester tester) => [
  for (final pill in tester.widgetList<Pill>(find.byType(Pill))) pill.label,
];

/// A phone-width surface: below `Layout.narrow`, so the page and the sheet
/// take their narrow layout.
void _phone(WidgetTester tester) {
  tester.view.physicalSize = const Size(390, 780);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
}

/// A desktop surface: above `Layout.narrow`, so the page and the sheet take
/// their wide layout.
void _desktop(WidgetTester tester) {
  tester.view.physicalSize = const Size(1280, 900);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
}

/// The screen with a review standing in for the handoff sheet: a kit sheet
/// over the conversation that closes without creating anything. The real
/// sheet's fields, evidence and approval are the handoff tests' subject;
/// this one is about what the planner keeps while it is open.
Widget _screenWithReview(PlanningModel model) => EmberApp(
  brightness: Brightness.light,
  home: Builder(
    builder: (context) => PlanningScreen(
      plan: model,
      title: 'Planning',
      subtitle: 'Permissive · planning in w-1',
      now: DateTime.utc(2026, 11, 17, 4, 12),
      onHandToFactory: () => _review(context),
    ),
  ),
);

Future<String?> _review(BuildContext context) async {
  // The real review, not a stand-in: the round trip this test proves is the
  // one the screen actually opens.
  final model = HandoffModel(
    defaults: const ProjectDefaults(repoID: 'repo-1'),
    projectRoot: '/home/u/marshal',
    title: 'Planning',
    plan: '## Task 1\nRead the item.\n',
    spec: 'Ship it.',
    create: (draft) async => throw UnimplementedError(),
  );
  final item = await showHandoffSheet(context, model);
  model.dispose();
  return item;
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

  testWidgets('the strip states the confirmed connection, mode and workspace', (
    tester,
  ) async {
    _desktop(tester);
    final planner = _planner(view: _view());
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    final pills = _pills(tester);
    expect(pills, contains('Planner live'));
    expect(pills, contains('Permissive'));
    expect(pills, contains('Workspace w-1'));
    // The caller's own sentence is not drawn: it cannot tell a requested mode
    // from one in force, and the strip states the facts the record holds.
    expect(find.text('Permissive · planning in w-1'), findsNothing);
  });

  testWidgets('a requested mode is stated as requested, not in force', (
    tester,
  ) async {
    _phone(tester);
    final planner = _planner(
      view: _view(
        requestedMode: 'default',
        confirmedMode: null,
        modeConfirmed: false,
      ),
    );
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    final pills = _pills(tester);
    expect(pills, contains('Restricted requested · not confirmed'));
    expect(pills, isNot(contains('Restricted')));
    // The composer's chip tells the same story at the field the person sends
    // from, rather than drawing the request as the mode in force.
    expect(
      tester.widget<Composer>(find.byType(Composer)).mode,
      'Restricted requested · not confirmed',
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('the strip and the conversation fit the phone layout', (
    tester,
  ) async {
    _phone(tester);
    final planner = _planner(view: _view());
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    expect(tester.takeException(), isNull);
    expect(_pills(tester), contains('Planner live'));
    expect(find.text('The plan has two tasks.'), findsOneWidget);
    expect(find.byType(Composer), findsOneWidget);
  });

  testWidgets('a pending request is counted beside the conversation', (
    tester,
  ) async {
    final planner = _planner(question: 'Which tab comes first?');
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    // The card the person answers and the count the strip states are both on
    // the screen the composer is on, and the card is above the field.
    expect(find.byType(AskCard), findsOneWidget);
    expect(_pills(tester), contains('1 request waiting on you'));
    expect(
      tester.getTopLeft(find.byType(AskCard)).dy,
      lessThan(tester.getTopLeft(find.byType(Composer)).dy),
    );
  });

  testWidgets('an ended planner is not drawn as running', (tester) async {
    final planner = _planner(
      view: _view(
        state: 'ended',
        live: false,
        endedAt: '2026-11-17T04:05:00Z',
        turns: [
          {
            'requestId': 'req-1',
            'textHash': 'abc',
            'state': 'running',
            'at': '2026-11-17T03:50:00Z',
          },
        ],
      ),
    );
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    expect(_pills(tester), contains('Planner ended'));
    // The record's last turn says "running"; the session says otherwise, so
    // the field offers to send rather than to steer or stop.
    expect(find.text('Steer'), findsNothing);
    expect(find.text('Stop'), findsNothing);
    expect(find.text('Send'), findsOneWidget);
  });

  testWidgets('a planner whose record has not been read claims nothing', (
    tester,
  ) async {
    // No read route: the model never has a record, which is unknown, not
    // connected.
    final planner = _planner();
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    final pills = _pills(tester);
    expect(pills, contains('Planner state unknown'));
    expect(pills, contains('Mode unknown'));
    expect(pills, contains('Workspace unknown'));
    expect(find.text('Send'), findsOneWidget);
    expect(find.text('Steer'), findsNothing);
    expect(find.text('Stop'), findsNothing);
  });

  testWidgets('the plan says what handing off does and that it comes back', (
    tester,
  ) async {
    final planner = _planner();
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screen(planner.model));
    await _settle(tester);

    expect(
      find.textContaining('Handing off freezes the plan and the spec'),
      findsOneWidget,
    );
    expect(
      find.textContaining('Closing the review brings you back here'),
      findsOneWidget,
    );

    await tester.runAsync(
      () => planner.model.snapshot(plan: '## Task 1\nRead the item.\n'),
    );
    await _settle(tester);

    // A captured plan is the one the review opens with, and the return is
    // still the same conversation.
    expect(find.textContaining('capture digest 9f2c1a55'), findsOneWidget);
    expect(
      find.textContaining('Closing the review brings you back here'),
      findsOneWidget,
    );
  });

  testWidgets('the draft survives the handoff review and back', (tester) async {
    final planner = _planner(view: _view());
    addTearDown(planner.model.dispose);
    await tester.pumpWidget(_screenWithReview(planner.model));
    await _settle(tester);

    await tester.enterText(find.byType(EditableText), 'Draft a second task');
    await tester.pump();
    expect(find.text('Draft a second task'), findsOneWidget);

    await tester.tap(find.text('Hand to the factory'));
    await tester.pumpAndSettle();
    expect(find.text('Create the factory draft'), findsOneWidget);

    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();

    expect(find.text('Create the factory draft'), findsNothing);
    // Back on the same conversation, with the words still in the field: the
    // review does not take the draft with it.
    expect(find.text('Draft a second task'), findsOneWidget);
    expect(planner.prompts, isEmpty);
    expect(_pills(tester), contains('Planner live'));
  });
}
