import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/inspect_model.dart';
import 'package:marshal_app/src/model/preview_model.dart';

import '../support/fake_bridge.dart';
import '../support/sockets.dart';

FakeReply _json(Object body, [int status = 200]) => FakeReply(
  status,
  headers: const {'content-type': 'application/json'},
  body: jsonEncode(body),
);

void main() {
  late FakeBridge bridge;
  late BridgeClient client;

  setUp(() async {
    HttpOverrides.global = null;
    final box = await socketBridge();
    bridge = box.bridge;
    client = BridgeClient(box.transport);
  });

  tearDown(() => bridge.close());

  test('lists declared ports, and opens one through the bridge', () async {
    bridge
      ..reply(
        'POST',
        '/api/agents/a-1/preview/5173',
        _json({'url': '/preview/a-1/5173/?t=tok'}),
      )
      ..reply(
        'POST',
        '/api/agents/a-1/preview/8080',
        _json({'error': 'port is not declared for preview'}, 403),
      );
    final model = PreviewModel(client, agentId: 'a-1', ports: const [5173]);

    expect(model.ports, [5173]);
    expect(model.urlFor(5173), isNull);

    final url = await model.open(5173);
    expect(url, '/preview/a-1/5173/?t=tok');
    expect(model.urlFor(5173), '/preview/a-1/5173/?t=tok');

    // A second open is free: the URL is kept.
    await model.open(5173);
    expect(
      bridge.requests.where((r) => r.uri == '/api/agents/a-1/preview/5173'),
      hasLength(1),
    );

    // A port the workspace does not declare is refused, and says why.
    model.setPorts(const [5173, 8080]);
    expect(await model.open(8080), isNull);
    expect(model.errorFor(8080), isNotNull);
  });

  test('inspect shows the node’s facts', () async {
    bridge.reply(
      'GET',
      '/api/sessions/s-1/nodes/n1',
      _json(
        NodeDetailResponse(
          node: const WireNode(
            id: 'n1',
            kind: 'tool',
            tool: WireTool(
              name: 'file.edit',
              display: 'Edit file',
              calls: [
                WireCall(
                  callID: 'call_8f21c0',
                  approval: 'auto',
                  risk: 'low',
                  durationMs: 300,
                ),
              ],
            ),
          ),
          detail: const NodeDetail(
            step: StepDetail(owner: 'agent', why: 'Drop the dead entries'),
            calls: [
              CallDetail(
                callID: 'call_8f21c0',
                toolName: 'file.edit',
                output: 'Applied 1 edit.',
                args: '{"path":"index.json"}',
              ),
            ],
            thinking: [
              ThoughtDetail(text: 'The fold test loads the other one.'),
            ],
          ),
        ).toJson(),
      ),
    );
    final model = InspectModel(client, sessionId: 's-1');

    await model.select('n1');

    expect(model.nodeId, 'n1');
    expect(model.loading, isFalse);
    final facts = model.facts;
    expect(facts.title, 'Edit file');
    expect(facts.actor, 'agent');
    expect(facts.why, 'Drop the dead entries');
    expect(facts.took, '300 ms');
    expect(facts.approval, 'auto · low');
    expect(facts.callId, 'call_8f21c0');
    expect(facts.output, 'Applied 1 edit.');
    expect(facts.arguments, '{"path":"index.json"}');
    expect(facts.thinking.single, 'The fold test loads the other one.');
  });

  group('a session the bridge no longer tracks', () {
    // The Studio showed "bridge: unknown session" in Inspect beside a live
    // transcript: the node route answers only for a session the bridge tracks,
    // and the transcript had been read while it still did.
    const node = NodeDetailResponse(
      node: WireNode(
        id: 'n1',
        kind: 'step',
        step: WireStep(headline: 'Run the bridge suite'),
      ),
      detail: NodeDetail(),
    );
    final unknown = _json({'error': 'bridge: unknown session'}, 404);

    test('is opened, and the node is read again', () async {
      bridge
        ..replyOnce('GET', '/api/sessions/s-1/nodes/n1', unknown)
        ..reply('GET', '/api/sessions/s-1/nodes/n1', _json(node.toJson()))
        ..reply('POST', '/api/sessions/s-1/load', _json({'sessionId': 's-1'}));
      final model = InspectModel(
        client,
        sessionId: 's-1',
        opener: () => client.openSession('s-1'),
      );

      await model.select('n1');

      expect(model.error, isNull);
      expect(model.facts.title, 'Run the bridge suite');
      expect(
        [for (final r in bridge.requests) '${r.method} ${r.uri}'],
        [
          'GET /api/sessions/s-1/nodes/n1',
          'POST /api/sessions/s-1/load',
          'GET /api/sessions/s-1/nodes/n1',
        ],
      );
    });

    test('an agent has no opener, so the refusal is the answer', () async {
      bridge.reply('GET', '/api/sessions/a-1/nodes/n1', unknown);
      final model = InspectModel(client, sessionId: 'a-1');

      await model.select('n1');

      expect(model.error, 'bridge: unknown session');
      expect(bridge.requests, hasLength(1));
    });

    test('a session that cannot be opened says why, once', () async {
      bridge
        ..reply('GET', '/api/sessions/s-1/nodes/n1', unknown)
        ..reply('POST', '/api/sessions/s-1/load', unknown);
      final model = InspectModel(
        client,
        sessionId: 's-1',
        opener: () => client.openSession('s-1'),
      );

      await model.select('n1');

      expect(model.error, 'bridge: unknown session');
      expect(bridge.requests, hasLength(2), reason: 'no second read');
    });
  });

  test('inspect appends a call’s workspace lifecycle facts', () async {
    NodeDetailResponse answer(SubagentWorkspace? workspace) =>
        NodeDetailResponse(
          node: const WireNode(
            id: 'n1',
            kind: 'tool',
            tool: WireTool(
              name: 'agent.integrate',
              display: 'Integrate task-1',
            ),
          ),
          detail: NodeDetail(
            calls: [
              CallDetail(
                callID: 'call_1',
                toolName: 'agent.integrate',
                output: 'task_key: task-1',
                workspace: workspace,
              ),
            ],
          ),
        );

    Future<InspectFacts> factsFor(SubagentWorkspace? workspace) async {
      bridge.reply(
        'GET',
        '/api/sessions/s-1/nodes/n1',
        _json(answer(workspace).toJson()),
      );
      final model = InspectModel(client, sessionId: 's-1');
      await model.select('n1');
      return model.facts;
    }

    // A recorded integration reports its candidate and its own states.
    final known = await factsFor(
      const SubagentWorkspace(
        taskKey: 'task-1',
        mode: 'isolated',
        root: '/worktrees/task-1',
        candidateSHA: '2222222',
        targetBranch: 'main',
        execution: 'done',
        integration: 'integrated',
        cleanup: 'integrated_cleanup_pending',
        reason: 'merged; the worktree is still present',
      ),
    );
    for (final want in [
      'workspace: isolated · /worktrees/task-1',
      'task: task-1',
      'candidate: 2222222',
      'target: main',
      'execution: done',
      'integration: integrated',
      'cleanup: integrated, cleanup pending',
      'reason: merged; the worktree is still present',
    ]) {
      expect(known.output, contains(want));
    }
    // The receipt's own output is still there, above the evidence.
    expect(known.output, startsWith('task_key: task-1'));

    // A conflict is not a success, and an absent candidate says so.
    final conflicted = await factsFor(
      const SubagentWorkspace(
        taskKey: 'task-1',
        integration: 'conflict',
        cleanup: 'retained',
      ),
    );
    expect(conflicted.output, contains('integration: conflict'));
    expect(conflicted.output, contains('cleanup: retained'));
    expect(conflicted.output, contains('candidate: none recorded'));

    // A candidate nobody integrated is never shown as integrated, and an
    // interrupted record's unknown state is not invented.
    final unintegrated = await factsFor(
      const SubagentWorkspace(taskKey: 'task-1', candidateSHA: 'abc1234'),
    );
    expect(unintegrated.output, contains('candidate: abc1234'));
    expect(unintegrated.output, contains('integration: not recorded'));
    expect(unintegrated.output, isNot(contains('integration: integrated')));

    // A pending integration is stated as pending, not as done.
    final pending = await factsFor(
      const SubagentWorkspace(taskKey: 'task-1', integration: 'intent'),
    );
    expect(pending.output, contains('integration: pending'));

    // A legacy call with no payload keeps exactly the output it had.
    final legacy = await factsFor(null);
    expect(legacy.output, 'task_key: task-1');
  });

  test('the model request loads on demand', () async {
    bridge.reply(
      'GET',
      '/api/sessions/s-1/last-request',
      _json(
        LastRequest(
          request: ModelRequest(
            attemptID: 7,
            model: 'claude-sonnet-4',
            messages: const [
              RequestMessage(role: 'user', content: 'Clean up the fixtures.'),
            ],
            tools: const [],
            options: const RequestOptions(streaming: true),
            outcome: const RequestOutcome(status: 'ok'),
          ),
        ).toJson(),
      ),
    );
    final model = InspectModel(client, sessionId: 's-1');

    // Nothing is read until the tab is opened.
    expect(model.modelRequest, isNull);
    expect(bridge.requests, isEmpty);

    await model.loadRequest();

    expect(model.modelRequest, contains('claude-sonnet-4'));
    expect(model.modelRequest, contains('Clean up the fixtures.'));
    expect(
      bridge.requests.where((r) => r.uri == '/api/sessions/s-1/last-request'),
      hasLength(1),
    );
  });

  test(
    'a request in flight notifies nobody once the thread has closed',
    () async {
      final held = _HeldTransport();
      final model = InspectModel(BridgeClient(held), sessionId: 's-1');

      final reading = model.loadRequest();
      // The thread place closes while the bridge has not answered: the request
      // still lands, and its answer must not reach a disposed model.
      model.dispose();
      held.release();

      await reading;
    },
  );
}

/// A transport whose answer waits for the test: a read can be in flight while
/// the model it feeds is disposed, which is what a thread closing mid-read
/// does.
class _HeldTransport implements Transport {
  Completer<Response>? _read;
  bool _released = false;

  @override
  Future<Response> send(Request request) {
    if (_released) {
      return Future.value(
        const Response(status: 404, body: '{"error":"no canned route"}'),
      );
    }
    final read = Completer<Response>();
    _read = read;
    return read.future;
  }

  /// Answers the read that is in flight, as the bridge does after the thread
  /// that asked it has closed.
  void release() {
    _released = true;
    _read?.complete(
      const Response(status: 404, body: '{"error":"no canned route"}'),
    );
    _read = null;
  }

  @override
  Stream<String> lines(Request request) => const Stream.empty();
}
