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
    expect(request.questions, ['Which inspector tab comes first?']);
  });

  test('a planner’s question is its card’s title', () async {
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
      _event(2, 'turn.question', {
        'requestId': 'req-2',
        'method': 'session/request_question',
        'params': {'message': 'Which branch should the plan start from?'},
      }),
      _event(3, 'turn.permission', {
        'requestId': 'req-3',
        'method': 'session/request_permission',
        'params': {'toolName': 'shell.run', 'command': 'go test ./...'},
      }),
    ];
    final model = routes.model();
    addTearDown(model.dispose);

    await model.refresh();

    final [asked, messaged, permission] = model.requests;
    // The question is the card's title; its body is empty, so the question is
    // not written twice.
    expect(asked.title, 'Which inspector tab comes first?');
    expect(asked.detail, isEmpty);
    // A payload that asked no question but carried its own line is titled with
    // that line too.
    expect(messaged.title, 'Which branch should the plan start from?');
    expect(messaged.detail, isEmpty);
    // A permission is the other way round: the wording is the title and the
    // command is the body.
    expect(permission.title, 'Permission requested');
    expect(permission.detail, 'tool: shell.run · command: go test ./...');
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

  test('the connection comes from the record, never from a guess', () async {
    // No record read at all: unknown, which is not the same as connected.
    final unknown = _Routes().model();
    addTearDown(unknown.dispose);
    await unknown.refresh();

    expect(unknown.connection, PlannerConnection.unknown);
    expect(unknown.connection.label, 'Planner state unknown');

    final live = _Routes().model(view: _view());
    addTearDown(live.dispose);
    await live.refresh();

    expect(live.connection, PlannerConnection.live);
    expect(live.view?.live, isTrue);

    // A record that says the planner ended is ended, whatever its last turn
    // says: nothing is running in a session that is over.
    final ended = _Routes().model(
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
    addTearDown(ended.dispose);
    await ended.refresh();

    expect(ended.connection, PlannerConnection.ended);
    expect(ended.running, isFalse);
  });

  test('a requested mode is not stated as the mode in force', () async {
    final model = _Routes().model(
      view: _view(
        requestedMode: 'default',
        modeConfirmed: false,
        confirmedMode: null,
      ),
    );
    addTearDown(model.dispose);
    await model.refresh();

    expect(model.requestedMode, 'Restricted');
    expect(model.confirmedMode, isNull);
    expect(model.modeConfirmed, isFalse);
    // The words a screen draws: the request, and that it is not in force.
    expect(model.modeWords, 'Restricted requested · not confirmed');
  });

  test('a confirmed mode is the mode in force', () async {
    // The bridge may confirm the request without naming a mode; that confirms
    // the mode that was requested.
    final byRequest = _Routes().model(
      view: _view(requestedMode: 'default', modeConfirmed: true),
    );
    addTearDown(byRequest.dispose);
    await byRequest.refresh();

    expect(byRequest.confirmedMode, 'Restricted');
    expect(byRequest.modeWords, 'Restricted');

    // A confirmation that names its own mode is that mode, not the request.
    final byName = _Routes().model(
      view: _view(
        requestedMode: 'default',
        confirmedMode: 'bypassPermissions',
        modeConfirmed: true,
      ),
    );
    addTearDown(byName.dispose);
    await byName.refresh();

    expect(byName.requestedMode, 'Restricted');
    expect(byName.confirmedMode, 'Permissive');
    expect(byName.modeWords, 'Permissive');
  });

  test(
    'the workspace comes from the record, and says when it has none',
    () async {
      final named = _Routes().model(view: _view(workspaceID: 'w-9'));
      addTearDown(named.dispose);
      await named.refresh();

      expect(named.workspace, 'w-9');
      expect(named.workspaceWords, 'Workspace w-9');

      final none = _Routes().model(view: _view(workspaceID: ''));
      addTearDown(none.dispose);
      await none.refresh();

      expect(none.workspace, isNull);
      expect(none.workspaceWords, 'No workspace');

      final unknown = _Routes().model();
      addTearDown(unknown.dispose);
      await unknown.refresh();

      expect(unknown.workspaceWords, 'Workspace unknown');
      expect(unknown.modeWords, 'Mode unknown');
    },
  );

  test('the waiting count is the requests the planner is parked on', () async {
    final routes = _Routes();
    routes.events = [
      _event(1, 'turn.permission', {
        'requestId': 'req-2',
        'params': {'toolName': 'shell.run'},
      }),
      _event(2, 'turn.question', {
        'requestId': 'req-3',
        'params': {
          'questions': [
            {'question': 'Which branch?'},
          ],
        },
      }),
    ];
    final model = routes.model();
    addTearDown(model.dispose);
    await model.refresh();

    expect(model.waiting, 2);

    await model.answer('req-2', 'allow');

    expect(model.waiting, 1);
  });
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

/// A planner record whose one turn is in [state].
PlannerView _viewWithTurn(String state) => _view(
  turns: [
    {
      'requestId': 'req-1',
      'textHash': 'abc',
      'state': state,
      'at': '2026-11-17T03:50:00Z',
    },
  ],
);
