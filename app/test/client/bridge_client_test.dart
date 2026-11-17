import 'dart:async';
import 'dart:convert';
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

  test('an automatic-fixing write names the ETag it read', () async {
    // The settings route is a conditional write: the ETag a GET returns is
    // what names the version the PUT was decided against, so a save that read
    // the document before another one wrote it is refused rather than
    // silently overwriting it (web/bridge/projects.go).
    final tag = '"${'a' * 64}"';
    fake
      ..reply(
        'GET',
        '/api/projects/settings',
        FakeReply(
          200,
          headers: {'content-type': 'application/json', 'etag': tag},
          body:
              '{"workspace":"alpha","automations":'
              '{"ciFixer":{"enabled":true},"autoFix":false}}',
        ),
      )
      ..reply(
        'PUT',
        '/api/projects/settings',
        const FakeReply(200, body: '{"workspace":"alpha"}'),
      );

    await client.setProjectAutoFix('/home/u/alpha', true);

    final put = fake.requests.lastWhere((r) => r.method == 'PUT');
    expect(put.headers['if-match'], tag);
    expect(put.uri, contains('root=%2Fhome%2Fu%2Falpha'));
    final body = jsonDecode(put.body) as Map<String, dynamic>;
    expect(body['workspace'], 'alpha', reason: 'the document is sent whole');
    expect((body['automations'] as Map)['ciFixer'], {
      'enabled': true,
    }, reason: 'the other automations survive');
    expect((body['automations'] as Map)['autoFix'], isTrue);
  });

  test('a settings write that loses the race applies its change to the '
      'winner and retries once', () async {
    final read = '"${'1' * 64}"';
    final won = '"${'2' * 64}"';
    fake
      // The document as this save read it...
      ..replyOnce(
        'GET',
        '/api/projects/settings',
        FakeReply(
          200,
          headers: {'content-type': 'application/json', 'etag': read},
          body: '{"automations":{"ciFixer":{"enabled":false}}}',
        ),
      )
      // ...and, when the refusal sends it back to read again, the value that
      // won and the ETag naming it.
      ..replyOnce(
        'GET',
        '/api/projects/settings',
        FakeReply(
          200,
          headers: {'content-type': 'application/json', 'etag': won},
          body:
              '{"workspace":"beta","automations":'
              '{"ciFixer":{"enabled":true}}}',
        ),
      )
      // Another save got there first: 412, changing nothing.
      ..replyOnce(
        'PUT',
        '/api/projects/settings',
        const FakeReply(
          412,
          headers: {'content-type': 'application/json'},
          body: '{"workspace":"beta"}',
        ),
      )
      ..reply(
        'PUT',
        '/api/projects/settings',
        const FakeReply(200, body: '{"workspace":"beta"}'),
      );

    await client.setProjectAutoFix('/home/u/alpha', true);

    final puts = fake.requests.where((r) => r.method == 'PUT').toList();
    expect(puts, hasLength(2), reason: 'it retries once, and no more');
    expect(puts.first.headers['if-match'], read);
    expect(puts.last.headers['if-match'], won);
    final body = jsonDecode(puts.last.body) as Map<String, dynamic>;
    expect(
      body['workspace'],
      'beta',
      reason: 'the change is made to the document that won, not the stale one',
    );
    expect((body['automations'] as Map)['ciFixer'], {
      'enabled': true,
    }, reason: 'the save that won is not overwritten');
    expect((body['automations'] as Map)['autoFix'], isTrue);
  });

  test('a settings read then write names the tag it read', () async {
    // The project page's own loop: projectSettings reads the document and the
    // ETag that names it, and putProjectSettings writes it back naming that
    // tag. A write that omitted If-Match would overwrite a change made after
    // the read, which is what the page promises it will not do
    // (web/bridge/projects.go).
    final read = '"${'d' * 64}"';
    final won = '"${'e' * 64}"';
    fake
      ..reply(
        'GET',
        '/api/projects/settings',
        FakeReply(
          200,
          headers: {'content-type': 'application/json', 'etag': read},
          body:
              '{"workspace":"alpha","intake":{"repoId":"repo-1"},'
              '"automations":{"autoFix":false}}',
        ),
      )
      ..reply(
        'PUT',
        '/api/projects/settings',
        FakeReply(
          200,
          headers: {'content-type': 'application/json', 'etag': won},
          body:
              '{"workspace":"alpha","intake":{"repoId":"repo-1"},'
              '"automations":{"autoFix":true}}',
        ),
      );

    final current = await client.projectSettings('/home/u/alpha');
    expect(current.etag, read);

    final written = await client.putProjectSettings(
      '/home/u/alpha',
      current.settings,
      etag: current.etag,
    );

    final put = fake.requests.lastWhere((r) => r.method == 'PUT');
    expect(
      put.headers['if-match'],
      read,
      reason: 'the write names the tag its own read returned',
    );
    expect(put.uri, contains('root=%2Fhome%2Fu%2Falpha'));
    expect(jsonDecode(put.body), current.settings.toJson());
    expect(
      written.etag,
      won,
      reason: 'the new tag is what the next save is decided against',
    );
  });

  test(
    'a settings write refused twice fails with the bridge’s answer',
    () async {
      final tag = '"${'c' * 64}"';
      fake
        ..reply(
          'GET',
          '/api/projects/settings',
          FakeReply(
            200,
            headers: {'content-type': 'application/json', 'etag': tag},
            body: '{"automations":{}}',
          ),
        )
        ..reply(
          'PUT',
          '/api/projects/settings',
          FakeReply(
            412,
            headers: {'etag': tag, 'content-type': 'application/json'},
            body: '{"automations":{"ciFixer":{"enabled":true}}}',
          ),
        );

      await expectLater(
        client.setProjectAutoFix('/home/u/alpha', true),
        throwsA(
          isA<BridgeFailure>().having(
            (failure) => failure.status,
            'status',
            412,
          ),
        ),
      );
      expect(
        fake.requests.where((r) => r.method == 'PUT'),
        hasLength(2),
        reason: 'one retry is all a lost race gets',
      );
    },
  );
}
