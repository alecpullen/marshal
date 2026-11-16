import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/changes_model.dart';
import 'package:marshal_app/src/model/stack_store.dart';

import '../support/fake_bridge.dart';

/// A change set as the bridge answers it: two files, one of them new.
DiffResult _diff() => const DiffResult(
  files: [
    DiffFile(path: 'internal/acp/stack.go', added: 12, removed: 3),
    DiffFile(path: 'internal/acp/new.go', added: 7, removed: 0),
  ],
  diff:
      'diff --git a/internal/acp/stack.go b/internal/acp/stack.go\n'
      '--- a/internal/acp/stack.go\n'
      '+++ b/internal/acp/stack.go\n'
      '@@ -1,2 +1,2 @@\n'
      ' one\n'
      '-two\n'
      '+three\n'
      'diff --git a/internal/acp/new.go b/internal/acp/new.go\n'
      '--- /dev/null\n'
      '+++ b/internal/acp/new.go\n'
      '@@ -0,0 +1,1 @@\n'
      '+new\n',
);

GateRecord _record({bool ok = false}) => GateRecord(
  at: '2026-11-17T04:08:00Z',
  result: GateResult(
    ok: ok,
    skipped: false,
    failedCommand: ok ? null : 'go test ./...',
    output: 'FAIL marshal/internal/app',
  ),
);

StepDiffs _steps() => const StepDiffs(
  steps: [
    StepDiff(
      stepNode: 'step:1',
      turnNode: 'turn:1',
      headline: 'Removed the dead fixtures',
      files: ['internal/acp/stack.go'],
      diff: '@@ -1,2 +1,2 @@\n one\n-two\n+three',
    ),
    StepDiff(
      stepNode: 'step:2',
      turnNode: 'turn:1',
      headline: 'Added the new file',
      files: ['internal/acp/new.go'],
      diff: '@@ -0,0 +1,1 @@\n+new',
    ),
  ],
);

/// A transcript whose live step is editing [target].
StackStore _live(String target) => StackStore.seeded(
  StackSnapshot(
    sessionID: 's-1',
    rev: 1,
    roots: const ['turn:1'],
    nodes: [
      const WireNode(id: 'turn:1', kind: 'turn', children: ['step:1']),
      const WireNode(
        id: 'step:1',
        kind: 'step',
        parent: 'turn:1',
        live: true,
        children: ['tool:1'],
        step: WireStep(headline: 'Editing the stack'),
      ),
      WireNode(
        id: 'tool:1',
        kind: 'tool',
        parent: 'turn:1',
        tool: WireTool(
          name: 'file.edit',
          display: 'Edit file',
          running: WireRunning(target: target),
        ),
      ),
    ],
  ),
);

void main() {
  late FakeBridge bridge;
  late BridgeClient client;

  setUp(() async {
    // flutter_test's binding answers every HttpClient with 400; the fake
    // bridge is on this machine, so the real client is wanted back.
    HttpOverrides.global = null;
    bridge = await FakeBridge.onLoopback();
    client = BridgeClient(
      serverTransport(bridge.base, token: () async => 'unused'),
    );
    bridge
      ..reply('GET', '/api/agents/a-1/diff', _json(_diff().toJson()))
      ..reply('GET', '/api/agents/a-1/gate', _json(_record().toJson()))
      ..reply(
        'GET',
        '/api/agents/a-1/commit-draft',
        _json(const CommitDraft(message: 'acp: split the stack').toJson()),
      )
      ..reply('GET', '/api/sessions/s-1/step-diffs', _json(_steps().toJson()))
      ..reply(
        'GET',
        '/api/agents/a-1/review/comments',
        _json({'comments': <Object>[]}),
      );
  });

  tearDown(() => bridge.close());

  test('refresh reads the gate, the files, the draft and the steps', () async {
    final model = ChangesModel(client, agentId: 'a-1', sessionId: 's-1');
    await model.refresh();

    expect(model.status, ChangesStatus.ready);
    expect(model.files.map((f) => f.path), [
      'internal/acp/stack.go',
      'internal/acp/new.go',
    ]);
    expect(model.gate?.ok, isFalse);
    expect(model.gate?.failedCommand, 'go test ./...');
    expect(model.gateRanAt, DateTime.utc(2026, 11, 17, 4, 8));
    expect(model.draft?.message, 'acp: split the stack');
    expect(model.steps.length, 2);
  });

  test('a failed read says so, and keeps nothing from the last one', () async {
    final model = ChangesModel(client, agentId: 'gone');
    await model.refresh();
    expect(model.status, ChangesStatus.failed);
    expect(model.failureMessage, isNotNull);
  });

  test('run gate posts verify and keeps the record it answers', () async {
    bridge.reply(
      'POST',
      '/api/agents/a-1/verify',
      _json(_record(ok: true).toJson()),
    );
    final model = ChangesModel(client, agentId: 'a-1');
    await model.refresh();
    expect(model.gate?.ok, isFalse);

    await model.runGate();

    final post = bridge.requests.where(
      (r) => r.method == 'POST' && r.uri == '/api/agents/a-1/verify',
    );
    expect(post, hasLength(1));
    expect(model.gate?.ok, isTrue);
    expect(model.runningGate, isFalse);
  });

  test('by step groups hunks by the step that changed them', () {
    final model = ChangesModel.seeded(
      files: _diff().files,
      diff: _diff().diff ?? '',
      steps: _steps().steps,
    );
    // Whole, the list is one group of both files.
    expect(model.groups.single.label, isEmpty);
    expect(model.groups.single.files, [
      'internal/acp/stack.go',
      'internal/acp/new.go',
    ]);

    model.setByStep(true);

    // Grouped, each group is a step's headline and its own hunk.
    expect(model.groups.map((g) => g.label), [
      'Removed the dead fixtures',
      'Added the new file',
    ]);
    expect(model.groups.first.files, ['internal/acp/stack.go']);
    expect(model.groups.first.diff, contains('-two'));
    expect(model.groups.last.diff, contains('+new'));
  });

  test('the diff splits per file', () {
    final byFile = splitDiffByFile(_diff().diff ?? '');
    expect(byFile.keys, ['internal/acp/stack.go', 'internal/acp/new.go']);
    expect(byFile['internal/acp/stack.go'], contains('+three'));
    expect(byFile['internal/acp/stack.go'], isNot(contains('+new')));
    expect(byFile['internal/acp/new.go'], contains('+new'));
  });

  test('viewed marks are held by the model, not by a tab', () {
    final model = ChangesModel.seeded(files: _diff().files);
    expect(model.isViewed('internal/acp/stack.go'), isFalse);
    model.toggleViewed('internal/acp/stack.go');
    expect(model.viewed, {'internal/acp/stack.go'});
    model.toggleViewed('internal/acp/stack.go');
    expect(model.viewed, isEmpty);
  });

  test('with nothing selected the file the agent is editing is shown', () {
    final model = ChangesModel.seeded(
      files: _diff().files,
      diff: _diff().diff ?? '',
      live: _live('internal/acp/new.go:12'),
    );
    expect(model.editedFile, 'internal/acp/new.go');
    expect(model.activeFile, 'internal/acp/new.go');

    // A person's choice wins, and stays after the agent moves on.
    model.select('internal/acp/stack.go');
    expect(model.activeFile, 'internal/acp/stack.go');

    // Following again: the edited file is back.
    model.select(null);
    expect(model.activeFile, 'internal/acp/new.go');
  });

  test('comment posts as steering', () async {
    bridge.reply(
      'POST',
      '/api/agents/a-1/review/comments',
      _json(_comment().toJson()),
    );
    final model = ChangesModel(client, agentId: 'a-1');
    await model.comment(
      'internal/acp/stack.go',
      7,
      'Trim this list to what the tests load',
      side: 'old',
      quote: '-two',
    );

    final post = bridge.requests.singleWhere(
      (r) => r.method == 'POST' && r.uri == '/api/agents/a-1/review/comments',
    );
    final body = jsonDecode(post.body) as Map<String, dynamic>;
    expect(body['path'], 'internal/acp/stack.go');
    expect(body['line'], 7);
    expect(body['side'], 'old');
    expect(body['quote'], '-two');
    expect(body['body'], 'Trim this list to what the tests load');
    // The bridge is what steers the agent with it; the model keeps the
    // comment the bridge stored.
    expect(model.comments.single.id, 'rc-1');
  });

  test('resolve resolves', () async {
    bridge.reply(
      'POST',
      '/api/agents/a-1/review/comments/rc-1/resolve',
      _json(const {'status': 'resolved'}),
    );
    final model = ChangesModel(client, agentId: 'a-1')..comments = [_comment()];
    expect(model.commentThreads.single.resolved, isFalse);

    await model.resolve('rc-1');

    expect(
      bridge.requests.where(
        (r) =>
            r.method == 'POST' &&
            r.uri == '/api/agents/a-1/review/comments/rc-1/resolve',
      ),
      hasLength(1),
    );
    expect(model.commentThreads.single.resolved, isTrue);
  });

  test('ship posts the message and whether to open a pull request', () async {
    bridge.reply(
      'POST',
      '/api/agents/a-1/exit',
      _json(
        const ExitResult(
          destination: 'push',
          branch: 'f4-inspector',
          prUrl: 'https://example.com/marshal/pull/7',
        ).toJson(),
      ),
    );
    final model = ChangesModel(client, agentId: 'a-1');

    final result = await model.ship(message: 'f4: the inspector', openPr: true);

    final post = bridge.requests.singleWhere(
      (r) => r.method == 'POST' && r.uri == '/api/agents/a-1/exit',
    );
    final body = jsonDecode(post.body) as Map<String, dynamic>;
    expect(body['commitMessage'], 'f4: the inspector');
    // A pull request is the default: the flag is false, which the bridge
    // reads as "open one", and no override is sent.
    expect(body['noPr'], isFalse);
    expect(body.containsKey('override'), isFalse);
    expect(result.outcome, ShipOutcome.pullRequest);
    expect(result.url, 'https://example.com/marshal/pull/7');

    await model.ship(message: 'f4: the inspector', openPr: false);
    final second =
        jsonDecode(bridge.requests.last.body) as Map<String, dynamic>;
    expect(second['noPr'], isTrue);
  });

  test('an override sends the reason it was given', () async {
    bridge.reply(
      'POST',
      '/api/agents/a-1/exit',
      _json(const ExitResult(destination: 'push', branch: 'b').toJson()),
    );
    final model = ChangesModel(client, agentId: 'a-1');
    await model.ship(
      message: 'm',
      openPr: true,
      overrideReason: 'the flaky test is unrelated',
    );
    final body = jsonDecode(bridge.requests.last.body) as Map<String, dynamic>;
    final override = body['override'] as Map<String, dynamic>;
    expect(override['reason'], 'the flaky test is unrelated');
  });

  test('each ship outcome is read from what the bridge answers', () async {
    Future<ShipResult> ship(ExitResult result) {
      bridge.reply('POST', '/api/agents/a-1/exit', _json(result.toJson()));
      return ChangesModel(
        client,
        agentId: 'a-1',
      ).ship(message: 'm', openPr: true);
    }

    expect(
      (await ship(
        const ExitResult(
          destination: 'push',
          branch: 'b',
          prUrl: 'https://x/pull/1',
        ),
      )).outcome,
      ShipOutcome.pullRequest,
    );
    expect(
      (await ship(const ExitResult(destination: 'push', branch: 'b'))).outcome,
      ShipOutcome.pushed,
    );
    final refused = await ship(
      const ExitResult(
        destination: 'push',
        blocked: true,
        verify: GateResult(
          ok: false,
          skipped: false,
          failedCommand: 'go test ./...',
        ),
      ),
    );
    expect(refused.outcome, ShipOutcome.gateRefused);
    expect(refused.message, contains('go test ./...'));
    expect(
      (await ship(const ExitResult(destination: 'patch'))).outcome,
      ShipOutcome.patch,
    );

    // A forge that refuses is an error from the bridge: nothing was pushed.
    bridge.reply(
      'POST',
      '/api/agents/a-1/exit',
      const FakeReply(502, body: '{"error":"forge: no credential"}'),
    );
    final forge = await ChangesModel(
      client,
      agentId: 'a-1',
    ).ship(message: 'm', openPr: true);
    expect(forge.outcome, ShipOutcome.forgeRefused);
    expect(forge.message, contains('no credential'));
  });

  test('discard posts, then empties the change set', () async {
    bridge.reply(
      'POST',
      '/api/agents/a-1/discard',
      _json(const ActionStatus(status: 'discarded').toJson()),
    );
    final model = ChangesModel(client, agentId: 'a-1')
      ..files = _diff().files
      ..diff = _diff().diff ?? ''
      ..comments = [_comment()];
    model.toggleViewed('internal/acp/stack.go');

    await model.discard();

    expect(
      bridge.requests.where(
        (r) => r.method == 'POST' && r.uri == '/api/agents/a-1/discard',
      ),
      hasLength(1),
    );
    expect(model.files, isEmpty);
    expect(model.diff, isEmpty);
    expect(model.comments, isEmpty);
    expect(model.viewed, isEmpty);
  });

  test('refresh loads the comments on the change set', () async {
    bridge.reply(
      'GET',
      '/api/agents/a-1/review/comments',
      _json({
        'comments': [_comment().toJson()],
      }),
    );
    final model = ChangesModel(client, agentId: 'a-1');
    await model.refresh();
    expect(model.commentThreads.single.id, 'rc-1');
    expect(model.commentThreads.single.sent, isTrue);
  });

  test('with nothing to follow the first changed file is shown', () {
    final model = ChangesModel.seeded(files: _diff().files);
    expect(model.editedFile, isNull);
    expect(model.activeFile, 'internal/acp/stack.go');
  });
}

/// One review comment as the bridge stores it.
ReviewComment _comment({String id = 'rc-1', String? resolvedAt}) =>
    ReviewComment(
      id: id,
      agentID: 'a-1',
      path: 'internal/acp/stack.go',
      line: 7,
      side: 'old',
      quote: '-two',
      body: 'Trim this list',
      createdAt: '2026-11-17T04:10:00Z',
      sentAt: '2026-11-17T04:10:01Z',
      resolvedAt: resolvedAt,
      ownerID: 'u-1',
    );

FakeReply _json(Object body) => FakeReply(
  200,
  headers: const {'content-type': 'application/json'},
  body: jsonEncode(body),
);
