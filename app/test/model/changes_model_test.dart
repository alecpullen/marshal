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
      ..reply('GET', '/api/sessions/s-1/step-diffs', _json(_steps().toJson()));
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

  test('with nothing to follow the first changed file is shown', () {
    final model = ChangesModel.seeded(files: _diff().files);
    expect(model.editedFile, isNull);
    expect(model.activeFile, 'internal/acp/stack.go');
  });
}

FakeReply _json(Object body) => FakeReply(
  200,
  headers: const {'content-type': 'application/json'},
  body: jsonEncode(body),
);
