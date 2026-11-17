import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/planning_model.dart';

/// A planner whose every route is recorded, so a test asserts what the model
/// posted without a socket.
class _Routes {
  final prompts = <PlannerPromptRequest>[];
  final answers = <({String requestID, Object? response})>[];
  final captures = <PlannerSnapshotRequest>[];
  final reads = <int>[];
  int cancels = 0;

  /// What the journal answers, by the `after` it was asked for.
  List<PlannerEvent> events = const [];

  /// What a capture answers.
  PlannerSnapshotReply capture = _captureReply();

  PlanningModel model({PlannerView? view}) => PlanningModel(
    plannerID: 'p-1',
    events: (after) async {
      reads.add(after);
      return events;
    },
    promptPlanner: (request) async {
      prompts.add(request);
      return PlannerTurnReply(
        requestID: request.requestID,
        state: 'dispatched',
      );
    },
    cancelPlanner: () async => cancels++,
    answerPlanner: (requestID, response) async {
      answers.add((requestID: requestID, response: response));
    },
    capture: (request) async {
      captures.add(request);
      return capture;
    },
    read: view == null ? null : () async => view,
  );
}

PlannerSnapshotReply _captureReply({
  String plan = '## Task 1\nRead the item.\n',
  String spec = 'Ship it.',
  List<PlannerRejection> rejected = const [],
}) => PlannerSnapshotReply(
  id: 'snap-1',
  sha256: '9f2c1a55',
  baseCommit: 'base-1',
  artifacts: [
    PlannerSnapshotArtifact(kind: 'plan', content: plan, sha256: 'a1'),
    PlannerSnapshotArtifact(kind: 'spec', content: spec, sha256: 'a2'),
  ],
  files: const [],
  createdAt: '2026-11-17T04:00:00Z',
  rejected: rejected,
);

/// One journaled event, as the bridge writes it.
PlannerEvent _event(int sequence, String kind, Map<String, Object?> data) =>
    PlannerEvent(sequence: sequence, kind: kind, data: data, truncated: false);

void main() {
  test('prompt posts the words and shows them at once', () async {
    final routes = _Routes();
    final model = routes.model();
    addTearDown(model.dispose);
    await model.refresh();

    await model.prompt('Plan the work item thread.');

    expect(routes.prompts, hasLength(1));
    expect(routes.prompts.single.text, 'Plan the work item thread.');
    expect(routes.prompts.single.requestID, isNotEmpty);
    // The person's own words are a line of the conversation before the bridge
    // has answered anything.
    expect(
      model.messages.any(
        (message) =>
            message.fromUser && message.text == 'Plan the work item thread.',
      ),
      isTrue,
    );
  });

  test('a request is an ask', () async {
    final routes = _Routes();
    routes.events = [
      _event(1, 'turn.question', {
        'requestId': 'req-1',
        'method': 'session/request_question',
        'params': {
          'questions': [
            {'question': 'Which inspector tab comes first?'},
          ],
        },
      }),
    ];
    final model = routes.model();
    addTearDown(model.dispose);

    await model.refresh();

    expect(model.requests, hasLength(1));
    final request = model.requests.single;
    expect(request.requestID, 'req-1');
    expect(request.permission, isFalse);
    expect(request.title, 'Question from the planner');
    expect(request.detail, 'Which inspector tab comes first?');
    expect(request.questions, ['Which inspector tab comes first?']);
  });

  test('a permission request is a permission card', () async {
    final routes = _Routes();
    routes.events = [
      _event(1, 'turn.permission', {
        'requestId': 'req-2',
        'method': 'session/request_permission',
        'params': {'toolName': 'shell.run', 'command': 'go test ./...'},
      }),
    ];
    final model = routes.model();
    addTearDown(model.dispose);
    await model.refresh();

    final request = model.requests.single;
    expect(request.permission, isTrue);
    expect(request.detail, 'tool: shell.run · command: go test ./...');
  });

  test('answering a permission posts the allow the agent reads', () async {
    final routes = _Routes();
    routes.events = [
      _event(1, 'turn.permission', {
        'requestId': 'req-2',
        'params': {'toolName': 'shell.run'},
      }),
    ];
    final model = routes.model();
    addTearDown(model.dispose);
    await model.refresh();

    await model.answer('req-2', 'allow');

    expect(routes.answers, hasLength(1));
    expect(routes.answers.single.requestID, 'req-2');
    expect(routes.answers.single.response, {'approved': true});
    expect(model.requests, isEmpty);
  });

  test('a refused answer keeps the request where it was', () async {
    final routes = _Routes();
    routes.events = [
      _event(1, 'turn.question', {
        'requestId': 'req-3',
        'params': {
          'questions': [
            {'question': 'Which branch?'},
          ],
        },
      }),
    ];
    final model = PlanningModel(
      plannerID: 'p-1',
      events: (after) async => routes.events,
      promptPlanner: (request) async =>
          PlannerTurnReply(requestID: request.requestID, state: 'dispatched'),
      cancelPlanner: () async {},
      answerPlanner: (requestID, response) async =>
          throw StateError('the bridge refused it'),
      capture: (request) async => _captureReply(),
    );
    addTearDown(model.dispose);
    await model.refresh();

    await model.answer('req-3', 'main');

    expect(model.requests, hasLength(1));
    expect(model.requests.single.requestID, 'req-3');
    expect(model.failure, contains('the bridge refused it'));
  });

  test('snapshot fills the plan', () async {
    final routes = _Routes();
    final model = routes.model();
    addTearDown(model.dispose);
    await model.refresh();

    expect(model.latest, isNull);
    final captured = await model.snapshot(
      plan: '## Task 1\nRead the item.\n',
      spec: 'Ship it.',
    );

    expect(captured, isNotNull);
    expect(model.latest, isNotNull);
    expect(model.latest!.id, 'snap-1');
    expect(model.latest!.plan, '## Task 1\nRead the item.\n');
    expect(model.latest!.spec, 'Ship it.');
    expect(model.latest!.summary, contains('1 task'));
    expect(routes.captures.single.plan, '## Task 1\nRead the item.\n');
    expect(routes.captures.single.paths, isEmpty);
  });

  test('a refused capture reports the paths it did not take', () async {
    final routes = _Routes();
    routes.capture = _captureReply(
      rejected: const [
        PlannerRejection(
          path: '../etc/passwd',
          reason: 'outside the workspace',
        ),
      ],
    );
    final model = routes.model();
    addTearDown(model.dispose);

    await model.snapshot(plan: '## Task 1\nx\n');

    expect(model.latest!.rejected, hasLength(1));
    expect(model.latest!.rejected.single.path, '../etc/passwd');
  });

  test('the journal is read from where the last read stopped', () async {
    final routes = _Routes();
    routes.events = [
      _event(1, 'turn.text', {'text': 'Reading the intake path.'}),
    ];
    final model = routes.model();
    addTearDown(model.dispose);
    await model.refresh();

    routes.events = [
      _event(2, 'turn.text', {'text': 'Drafting the plan.'}),
    ];
    await model.refresh();

    expect(routes.reads, [0, 1]);
    expect(model.messages.map((message) => message.text), [
      'Reading the intake path.',
      'Drafting the plan.',
    ]);
  });

  test('a turn in flight comes from the record, and cancel stops it', () async {
    final routes = _Routes();
    // The record is the authority on whether a turn is in flight, so the
    // fixture's view is mutable: cancelling settles the turn the way the
    // bridge's own record would.
    var view = _viewWithTurn('running');
    final model = PlanningModel(
      plannerID: 'p-1',
      events: (after) async => routes.events,
      promptPlanner: (request) async =>
          PlannerTurnReply(requestID: request.requestID, state: 'dispatched'),
      cancelPlanner: () async {
        routes.cancels++;
        view = _viewWithTurn('cancelled');
      },
      answerPlanner: (requestID, response) async {},
      capture: (request) async => _captureReply(),
      read: () async => view,
    );
    addTearDown(model.dispose);
    await model.refresh();

    expect(model.running, isTrue);

    await model.cancel();

    expect(routes.cancels, 1);
    expect(model.running, isFalse);
  });
}

/// A planner record whose one turn is in [state].
PlannerView _viewWithTurn(String state) => PlannerView.fromJson({
  'id': 'p-1',
  'projectRoot': '/home/u/marshal',
  'repoId': 'repo-1',
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
  'turns': [
    {
      'requestId': 'req-1',
      'textHash': 'abc',
      'state': state,
      'at': '2026-11-17T03:50:00Z',
    },
  ],
});
