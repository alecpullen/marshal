import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/bridge_client.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/event_stream.dart';
import 'package:marshal_app/src/client/transport.dart';

import '../support/fake_bridge.dart';
import '../support/sockets.dart';

/// [BridgeClient] against the in-process fake bridge: one test per route, so
/// a route's path, method and decoded type are pinned without a daemon.
void main() {
  late FakeBridge fake;
  late Transport transport;
  late BridgeClient client;

  setUp(() async {
    // flutter_test's binding answers every HttpClient with 400; the fake
    // bridge is on this machine, so the real client is wanted back.
    HttpOverrides.global = null;
    final box = await socketBridge();
    fake = box.bridge;
    transport = box.transport;
    client = BridgeClient(transport);
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

  test('hosts reads /api/hosts', () async {
    fake.reply(
      'GET',
      '/api/hosts',
      const FakeReply(
        200,
        body:
            '{"hosts":[{"name":"laptop","ssh":"me@laptop","state":"live",'
            '"socket":"/tmp/laptop.sock","since":1763300000000},'
            '{"name":"build","ssh":"me@build","state":"down",'
            '"socket":"/tmp/build.sock","lastError":"connection refused",'
            '"since":1763300060000}]}',
      ),
    );

    final reported = await client.hosts();

    expect(reported.hosts, hasLength(2));
    expect(reported.hosts.first.name, 'laptop');
    expect(reported.hosts.first.ssh, 'me@laptop');
    expect(reported.hosts.first.state, HostState.hostLive);
    expect(reported.hosts.first.socket, '/tmp/laptop.sock');
    expect(reported.hosts.first.lastError, isNull);
    expect(reported.hosts.first.since, 1763300000000);
    // A host whose tunnel is down is still listed: its last error is why, and
    // the row stays so the person can see it rather than lose the host.
    expect(reported.hosts.last.name, 'build');
    expect(reported.hosts.last.ssh, 'me@build');
    expect(reported.hosts.last.state, HostState.hostDown);
    expect(reported.hosts.last.socket, '/tmp/build.sock');
    expect(reported.hosts.last.lastError, 'connection refused');
    expect(fake.requests.single.method, 'GET');
    expect(fake.requests.single.uri, '/api/hosts');
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

  test('preview reads the URL the route answers', () async {
    fake.reply(
      'POST',
      '/api/agents/a-1/preview/5173',
      const FakeReply(
        200,
        body: '{"url":"http://127.0.0.1:5173/preview/t-1/"}',
      ),
    );

    final url = await client.preview('a-1', 5173);

    expect(url, 'http://127.0.0.1:5173/preview/t-1/');
    expect(fake.requests.single.method, 'POST');
    expect(fake.requests.single.uri, '/api/agents/a-1/preview/5173');
  });

  test('a preview names the client’s preview origin', () async {
    // A link the route answers with has to open on this machine, so a client
    // that reaches the host's preview listener through a forwarded loopback
    // port says which one it is, and the bridge builds the URL from that.
    final remote = BridgeClient(
      transport,
      previewOrigin: 'http://localhost:54400',
    );
    fake.reply(
      'POST',
      '/api/agents/a-1/preview/5173',
      const FakeReply(
        200,
        body: '{"url":"http://localhost:54400/preview/t-1/"}',
      ),
    );

    expect(
      await remote.preview('a-1', 5173),
      'http://localhost:54400/preview/t-1/',
    );
    expect(
      fake.requests.single.headers[previewOriginHeader.toLowerCase()],
      'http://localhost:54400',
      reason: 'the port the host forwards is the origin the link is built on',
    );
  });

  test('a preview without an origin sends no header', () async {
    // The local daemon's previews open on this machine already, so a client
    // with no forwarded port names nothing and the bridge keeps its own answer.
    fake.reply(
      'POST',
      '/api/agents/a-1/preview/5173',
      const FakeReply(200, body: '{"url":"http://127.0.0.1:5173/x/"}'),
    );

    await client.preview('a-1', 5173);

    expect(
      fake.requests.single.headers,
      isNot(contains(previewOriginHeader.toLowerCase())),
      reason: 'a client with no origin names none',
    );
  });

  test('a non-preview call sends no preview origin header', () async {
    // The origin is the preview route's business alone (web/bridge/preview.go
    // reads it from that request): a client that has one must not let it ride
    // on every call, or each route would read a header only previews answer.
    final remote = BridgeClient(
      transport,
      previewOrigin: 'http://localhost:54400',
    );
    fake.reply(
      'GET',
      '/api/daemon',
      const FakeReply(
        200,
        body:
            '{"version":"0.9.1","profile":"local","pid":42,'
            '"startedAt":1763300000000,"socket":"/tmp/daemon.sock",'
            '"projects":[],"pinned":[],"children":[]}',
      ),
    );

    await remote.daemon();

    expect(fake.requests.single.uri, '/api/daemon');
    expect(
      fake.requests.single.headers,
      isNot(contains(previewOriginHeader.toLowerCase())),
      reason: 'only a preview names the origin it is reached through',
    );
  });

  test('a preview answer that names no url yields no URL', () async {
    // The route answers {"url": …} and nothing else (web/bridge/preview.go):
    // a body that names some other key is not a URL this client can use, so
    // it must not be read as one.
    fake.reply(
      'POST',
      '/api/agents/a-1/preview/5173',
      const FakeReply(200, body: '{"previewUrl":"http://stale.example/"}'),
    );

    expect(await client.preview('a-1', 5173), '');
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
    // The fake keys a stream by path, so the query is asserted on the recorded
    // request below: the fleet stream is the query-selected view of this route,
    // and without `stream=fleet` the bridge reads it as a session's stream and
    // refuses it.
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
    expect(fake.requests.last.uri, '/api/events?stream=fleet');
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

  // The capture-bound handoff route: a review posts the draft the person
  // approved, and the bridge rewrites its plan and specification from the
  // frozen snapshot. Pinning the path, the method and the decoded answer here
  // is what keeps that route from drifting behind the model's claims.
  test('a snapshot handoff posts the capture and reads the item', () async {
    const request = FactoryCreate(
      requestID: 'req-1',
      title: 'Work',
      plan: '## Task 1\nFrozen plan.\n',
      spec: 'Frozen specification.',
      config: FactoryConfig(
        repoID: 'repo-1',
        projectRoot: '/home/u/alpha',
        hostID: 'local',
        targetBranch: '',
        workspace: '',
        native: true,
        implementerRouting: null,
        reviewerRouting: null,
        reviewerInstructions: '',
        implementerAuto: true,
        expectedChecks: [],
        noRequiredCIAcknowledged: true,
        maxRounds: 3,
        ciPollSeconds: 30,
        ciWaitSeconds: 900,
      ),
      editSnapshot: EditSnapshot(
        baseCommit: 'c0ffee1',
        files: [],
        sha256: 'a1b2c3d4',
      ),
    );
    // The item the bridge answers carries the draft it stored.
    fake.reply(
      'POST',
      '/api/planners/planner-7/snapshots/snap-1/handoff',
      FakeReply(
        201,
        body: jsonEncode({
          'item': {
            'id': 'item-1',
            'title': 'Work',
            'state': 'draft',
            'stage': 'plan',
            'repoId': 'repo-1',
            'projectRoot': '/home/u/alpha',
            'hostId': 'local',
            'branch': 'marshal/factory/item-1',
            'baseSha': 'base-1',
            'version': 1,
            'draft': request.toJson(),
            'approvals': <Object?>[],
            'grants': <Object?>[],
            'rounds': <Object?>[],
            'workers': <Object?>[],
            'prs': <Object?>[],
            'effects': <Object?>[],
            'history': <Object?>[],
            'createdAt': '2026-11-17T04:00:00Z',
            'approvedAt': '',
            'updatedAt': '2026-11-17T04:00:00Z',
          },
        }),
      ),
    );

    final item = await client.handoffPlannerSnapshot(
      'planner-7',
      'snap-1',
      request,
    );

    expect(item.id, 'item-1');
    expect(item.state, 'draft');
    expect(fake.requests.single.method, 'POST');
    expect(
      fake.requests.single.uri,
      '/api/planners/planner-7/snapshots/snap-1/handoff',
    );
    // The draft the review built travels, capture included.
    final body = jsonDecode(fake.requests.single.body) as Map<String, dynamic>;
    expect(body['requestId'], 'req-1');
    expect(body['plan'], '## Task 1\nFrozen plan.\n');
    final edit = body['editSnapshot'] as Map<String, dynamic>;
    expect(edit['baseCommit'], 'c0ffee1');
    expect(edit['sha256'], 'a1b2c3d4');
    expect(edit['files'], isEmpty);
  });

  test('sessionIndex reads the index', () async {
    fake.reply(
      'GET',
      '/api/sessions/all',
      const FakeReply(
        200,
        body:
            '{"projects":[{"root":"/home/u/src/marshal",'
            '"sessions":[{"sessionId":"s-1","title":"Fix it",'
            '"updatedAt":1763300000000,"messageCount":3,'
            '"open":true,"viewers":1,"busy":false}]}]}',
      ),
    );

    final index = await client.sessionIndex();

    final project = index.projects.single;
    expect(project.root, '/home/u/src/marshal');
    final row = project.sessions.single;
    expect(row.sessionID, 's-1');
    expect(row.title, 'Fix it');
    expect(row.messageCount, 3);
    expect(row.open, isTrue);
    expect(row.viewers, 1);
    expect(row.busy, isFalse);
    expect(fake.requests.single.method, 'GET');
    expect(fake.requests.single.uri, '/api/sessions/all');
  });

  test('sessionIndex narrows to a root', () async {
    fake.reply(
      'GET',
      '/api/sessions/all',
      const FakeReply(200, body: '{"projects":[]}'),
    );

    final index = await client.sessionIndex(root: '/home/u/alpha');

    expect(index.projects, isEmpty);
    expect(
      fake.requests.single.uri,
      '/api/sessions/all?root=%2Fhome%2Fu%2Falpha',
    );
  });

  test('openSession posts load', () async {
    fake.reply('POST', '/api/sessions/s-1/load', const FakeReply(200));

    await client.openSession('s-1');

    final request = fake.requests.single;
    expect(request.method, 'POST');
    expect(request.uri, '/api/sessions/s-1/load');
    expect(request.body, isEmpty);
  });
}
