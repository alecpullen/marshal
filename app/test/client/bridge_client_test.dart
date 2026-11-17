import 'dart:async';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/event_stream.dart';
import 'package:marshal_app/src/client/transport.dart';

import '../support/fake_bridge.dart';

/// [BridgeClient] against the in-process fake bridge: one test per route, so
/// a route's path, method and decoded type are pinned without a daemon.
void main() {
  late FakeBridge fake;
  late BridgeClient client;

  setUp(() async {
    // flutter_test's binding answers every HttpClient with 400; the fake
    // bridge is on this machine, so the real client is wanted back.
    HttpOverrides.global = null;
    fake = await FakeBridge.onLoopback();
    addTearDown(fake.close);
    client = BridgeClient(
      serverTransport(fake.base, token: () async => 'unused'),
    );
  });

  test('daemon reads the daemon document', () async {
    fake.reply(
      'GET',
      '/api/daemon',
      const FakeReply(
        200,
        body:
            '{"version":"0.9.1","profile":"local","pid":42,'
            '"startedAt":1763300000000,"socket":"/tmp/daemon.sock",'
            '"projects":["/home/u/src/marshal"],"pinned":[],"children":[]}',
      ),
    );

    final info = await client.daemon();

    expect(info.version, '0.9.1');
    expect(info.socket, '/tmp/daemon.sock');
    expect(fake.requests.single.method, 'GET');
    expect(fake.requests.single.uri, '/api/daemon');
  });

  test('agents decodes every agent', () async {
    fake.reply(
      'GET',
      '/api/agents',
      const FakeReply(
        200,
        body:
            '[{"id":"a-1","project":"/home/u/src/marshal",'
            '"status":"awaiting-approval",'
            '"updatedAt":"2026-11-17T04:00:00Z"}]',
      ),
    );

    final agents = await client.agents();

    expect(agents, hasLength(1));
    expect(agents.single.id, 'a-1');
    expect(agents.single.status, AgentState.agentAwaitingApproval);
    expect(fake.requests.single.uri, '/api/agents');
  });

  test('projects decodes every project', () async {
    fake.reply(
      'GET',
      '/api/projects',
      const FakeReply(
        200,
        body:
            '[{"root":"/home/u/src/marshal","available":true,'
            '"trust":"trusted","isolation":"available"}]',
      ),
    );

    final projects = await client.projects();

    expect(projects.single.root, '/home/u/src/marshal');
    expect(projects.single.trust, TrustState.trustTrusted);
    expect(fake.requests.single.uri, '/api/projects');
  });

  test('inbox decodes the queue and its groups', () async {
    fake.reply(
      'GET',
      '/api/inbox',
      const FakeReply(
        200,
        body:
            '{"groups":{"needsYou":["a-1"],"running":[],"ready":[],'
            '"earlier":[]},"queue":[]}',
      ),
    );

    final inbox = await client.inbox();

    expect(inbox.groups.needsYou, ['a-1']);
    expect(inbox.queue, isEmpty);
    expect(fake.requests.single.uri, '/api/inbox');
  });

  test('pending lists the submissions waiting on a decision', () async {
    fake.reply(
      'GET',
      '/api/pending',
      const FakeReply(
        200,
        body:
            '[{"auto":true,"clientId":"c-1",'
            '"createdAt":"2026-11-17T04:40:00Z",'
            '"expiresAt":"2026-11-17T05:40:00Z","id":"s-1","mode":"act",'
            '"origin":"tui","plan":"docs/plan.md","prompt":"fix",'
            '"ref":"refs/heads/main","repoId":"r-1","title":"Fix it"}]',
      ),
    );

    final pending = await client.pending();

    expect(pending.single.id, 's-1');
    expect(pending.single.title, 'Fix it');
    expect(fake.requests.single.uri, '/api/pending');
  });

  test('answerPermission posts the decision to its tool call', () async {
    fake.reply('POST', '/api/permissions/tc-1', const FakeReply(200));

    await client.answerPermission(
      'tc-1',
      Decision(approved: true, edited: 'rm -rf ./build'),
    );

    final request = fake.requests.single;
    expect(request.method, 'POST');
    expect(request.uri, '/api/permissions/tc-1');
    expect(request.body, '{"approved":true,"edited":"rm -rf ./build"}');
  });

  test('answerQuestion posts the answers to its question', () async {
    fake.reply('POST', '/api/questions/q-1', const FakeReply(200));

    await client.answerQuestion(
      'q-1',
      Answers(
        answers: [Answer(question: 'Which branch?', answer: 'main')],
      ),
    );

    final request = fake.requests.single;
    expect(request.method, 'POST');
    expect(request.uri, '/api/questions/q-1');
    expect(
      request.body,
      '{"answers":[{"question":"Which branch?","answer":"main"}]}',
    );
  });

  test('approveIntake reads the agent it started', () async {
    fake.reply(
      'POST',
      '/api/pending/s-1/approve',
      const FakeReply(200, body: '{"agentId":"a-1","status":"spawning"}'),
    );

    final result = await client.approveIntake('s-1');

    expect(result.agentID, 'a-1');
    expect(result.status, 'spawning');
    expect(fake.requests.single.method, 'POST');
    expect(fake.requests.single.uri, '/api/pending/s-1/approve');
  });

  test('denyIntake posts the denial', () async {
    fake.reply(
      'POST',
      '/api/pending/s-1/deny',
      const FakeReply(200, body: '{"status":"denied"}'),
    );

    await client.denyIntake('s-1');

    expect(fake.requests.single.method, 'POST');
    expect(fake.requests.single.uri, '/api/pending/s-1/deny');
  });

  test('budgets decodes the report', () async {
    fake.reply(
      'GET',
      '/api/budgets',
      const FakeReply(
        200,
        body:
            '{"budgets":{"dailyUsd":10.0,"perAgentUsd":2.0,'
            '"onDailyCap":"pause","onAgentCap":"pause"},'
            '"daily":{"day":"2026-11-17","spentUsd":1.5,"blocked":false},'
            '"agents":[],"loaded":true}',
      ),
    );

    final report = await client.budgets();

    expect(report.loaded, isTrue);
    expect(report.daily.day, '2026-11-17');
    expect(report.budgets.dailyUSD, 10.0);
    expect(fake.requests.single.uri, '/api/budgets');
  });

  test('recentPrompts decodes the prompts', () async {
    fake.reply(
      'GET',
      '/api/prompts/recent',
      const FakeReply(200, body: '{"prompts":["fix the login bug"]}'),
    );

    final recent = await client.recentPrompts();

    expect(recent.prompts, ['fix the login bug']);
    expect(fake.requests.single.uri, '/api/prompts/recent');
  });

  test('recipes decodes the bridge store', () async {
    fake.reply(
      'GET',
      '/api/recipes',
      const FakeReply(
        200,
        body:
            '[{"name":"fix-a-bug","title":"Fix a bug","kind":"prompt",'
            '"prompt":"Find the bug and add a test.","builtin":true}]',
      ),
    );

    final recipes = await client.recipes();

    expect(recipes.single.name, 'fix-a-bug');
    expect(recipes.single.title, 'Fix a bug');
    expect(recipes.single.prompt, contains('add a test'));
    expect(recipes.single.builtin, isTrue);
    expect(fake.requests.single.uri, '/api/recipes');
  });

  test('issues asks the repo route, escaping the id', () async {
    fake.reply(
      'GET',
      '/api/repos/owner%2Fname/issues',
      const FakeReply(
        200,
        body:
            '[{"Number":42,"Title":"Fix the login redirect",'
            '"Body":"The return path is lost.",'
            '"URL":"https://example.test/issues/42","Labels":["bug"]}]',
      ),
    );

    final issues = await client.issues('owner/name');

    expect(issues.single.number, 42);
    expect(issues.single.title, 'Fix the login redirect');
    expect(issues.single.labels, ['bug']);
    expect(fake.requests.single.uri, '/api/repos/owner%2Fname/issues');
  });

  test('fleetEvents streams the bridge events', () async {
    final frames = fake.stream('GET', '/api/events');
    frames.write('id: 9\n');
    frames.write('data: {"kind":"activity"}\n');
    frames.write('\n');

    final stream = client.fleetEvents();
    addTearDown(stream.stop);
    final first = Completer<StreamEvent>();
    stream.events.listen((event) {
      if (!first.isCompleted) first.complete(event);
    });
    stream.start();

    final event = await first.future.timeout(const Duration(seconds: 5));
    expect(event.id, 9);
    expect(event.data, '{"kind":"activity"}');
    expect(fake.requests.last.uri, '/api/events');
  });
}
