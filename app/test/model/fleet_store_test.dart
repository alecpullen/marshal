import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/host.dart';

import '../support/fake_bridge.dart';
import 'support.dart';

/// The store is the one writer of fleet state (F2 decision 6): it holds what
/// each host's snapshot said, applies the stream's deltas to it, and keeps a
/// host's rows when it stops answering.
void main() {
  BudgetReport report({double spent = 1.5}) => BudgetReport(
    budgets: const Budgets(
      dailyUSD: 10,
      perAgentUSD: 2,
      onDailyCap: 'pause',
      onAgentCap: 'pause',
    ),
    daily: BudgetDaily(day: '2026-11-17', spentUSD: spent, blocked: false),
    agents: const [],
    loaded: true,
  );

  /// One session row as a test writes one.
  SessionRow sessionRow(String id) => SessionRow(
    sessionID: id,
    title: 'Fix the login bug',
    updatedAt: 1763300000000,
    messageCount: 3,
    open: true,
    viewers: 1,
    busy: false,
  );

  /// One project's sessions as the index carries them.
  ProjectSessions group(String root, List<SessionRow> rows, {String? error}) =>
      ProjectSessions(root: root, sessions: rows, error: error);

  Future<ModelFixture> fixtureWith({
    List<AgentStatus>? agents,
    List<ProjectStatus>? projects,
    List<InboxItem>? queue,
    BudgetReport? budget,
  }) => ModelFixture.start([
    FixtureHost(
      name: 'local',
      agents:
          agents ??
          [agent(id: 'a-1', status: AgentState.agentRunning, activity: 'Idle')],
      projects: projects ?? [project('/home/u/alpha')],
      queue:
          queue ??
          [
            InboxItem(
              kind: InboxItemKind.inboxItemAgent,
              agentID: 'a-1',
              at: '2026-11-17T04:00:00Z',
            ),
          ],
      budget: budget ?? report(),
    ),
  ]);

  test('snapshot populates', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();

    expect([for (final a in fixture.store.agents()) a.id], ['a-1']);
    expect(
      [for (final p in fixture.store.projects()) p.root],
      ['/home/u/alpha'],
    );
    expect(fixture.store.inbox(), hasLength(1));
    expect(fixture.store.inbox().single.agentID, 'a-1');
    expect(fixture.store.budgets('local')?.daily.spentUSD, 1.5);
    expect(fixture.store.failure('local'), isNull);
    // One read per route: the rows and the queue describe the same moment.
    expect(
      [
        for (final request in fixture.requests('local'))
          if (request.uri.startsWith('/api/')) request.uri,
      ],
      containsAll([
        '/api/agents',
        '/api/projects',
        '/api/inbox',
        '/api/budgets',
      ]),
    );
  });

  test('an activity delta says what the agent is doing', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();

    fixture.delta('local', {
      'kind': 'activity',
      'sessionId': 'a-1',
      'activity': 'Running the tests',
    });
    await eventually(
      () => fixture.store.agents().single.activity == 'Running the tests',
    );
    expect(fixture.store.agents().single.status, AgentState.agentRunning);
  });

  test('a telemetry delta carries the context and the changed files', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();

    fixture.delta('local', {
      'kind': 'telemetry',
      'sessionId': 'a-1',
      'contextPct': 42,
      'changedFiles': 3,
    });
    await eventually(() => fixture.store.agents().single.contextPct == 42);

    final agent = fixture.store.agents().single;
    expect(agent.changedFiles, 3);
  });

  test('a mode delta carries the committed access tuple', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();

    fixture.delta('local', {
      'kind': 'mode',
      'sessionId': 'a-1',
      'access': {
        'mode': 'act',
        'auto': true,
        'system_access': false,
        'access_revision': 4,
      },
    });
    await eventually(() => fixture.store.agents().single.access != null);

    final access = fixture.store.agents().single.access!;
    expect(access.mode, 'act');
    expect(access.auto, isTrue);
    expect(access.system, isFalse);
    expect(access.revision, 4);
  });

  test('a hold delta says whether a terminal holds the agent', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();

    fixture.delta('local', {
      'kind': 'hold',
      'sessionId': 'a-1',
      'held': true,
      'by': 'tui',
    });
    await eventually(() => fixture.store.agents().single.held == true);
  });

  test(
    'a pending delta parks the agent and reads the request itself',
    () async {
      final fixture = await fixtureWith();
      await fixture.refresh();

      // The delta names the kind only; the request behind it is the
      // snapshot's to give, so the bridge's agents route now reports it.
      fixture.serve('local', '/api/agents', [
        agent(
          id: 'a-1',
          status: AgentState.agentAwaitingApproval,
          pending: const PendingRequest(
            kind: PendingKind.pendingApproval,
            id: 'tc-1',
            params: {'command': 'git push'},
          ),
        ).toJson(),
      ]);
      final reads = fixture.requests('local').length;
      fixture.delta('local', {
        'kind': 'pending',
        'sessionId': 'a-1',
        'pendingKind': 'approval',
      });

      await eventually(() => fixture.store.agents().single.pending != null);
      final parked = fixture.store.agents().single;
      expect(parked.status, AgentState.agentAwaitingApproval);
      expect(parked.pending?.id, 'tc-1');
      expect(
        fixture.requests('local').length,
        greaterThan(reads),
        reason: 'the request itself is read from the snapshot',
      );
    },
  );

  test('an agent that parks during a re-read still gets its request', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          agent(id: 'a-1', status: AgentState.agentRunning),
          agent(id: 'a-2', status: AgentState.agentRunning),
        ],
      ),
    ]);
    await fixture.refresh();

    PendingRequest parked(String id) => PendingRequest(
      kind: PendingKind.pendingApproval,
      id: id,
      params: {'command': 'git push'},
    );

    // The first re-read is held at the bridge, so the second agent parks
    // while it is still in flight.
    fixture.serve('local', '/api/agents', [
      agent(
        id: 'a-1',
        status: AgentState.agentAwaitingApproval,
        pending: parked('tc-1'),
      ).toJson(),
      agent(id: 'a-2', status: AgentState.agentRunning).toJson(),
    ], delay: const Duration(milliseconds: 150));

    fixture.delta('local', {
      'kind': 'pending',
      'sessionId': 'a-1',
      'pendingKind': 'approval',
    });
    await Future<void>.delayed(const Duration(milliseconds: 30));

    // The bridge's next answer carries both requests.
    fixture.serve('local', '/api/agents', [
      agent(
        id: 'a-1',
        status: AgentState.agentAwaitingApproval,
        pending: parked('tc-1'),
      ).toJson(),
      agent(
        id: 'a-2',
        status: AgentState.agentAwaitingApproval,
        pending: parked('tc-2'),
      ).toJson(),
    ]);
    fixture.delta('local', {
      'kind': 'pending',
      'sessionId': 'a-2',
      'pendingKind': 'approval',
    });

    await eventually(
      () =>
          fixture.store.agents().any((a) => a.id == 'a-2' && a.pending != null),
      reason: 'the second request is coalesced into a follow-up read, not lost',
    );
    expect(
      fixture.store.agents().firstWhere((a) => a.id == 'a-2').pending?.id,
      'tc-2',
    );
  });

  test('a gate delta is accepted and set aside', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();

    fixture.delta('local', {
      'kind': 'gate',
      'sessionId': 'a-1',
      'gate': {'at': '2026-11-17T04:10:00Z'},
    });
    await settle();
    // The gate has no place in a row yet; the row is untouched, never
    // guessed at.
    expect(fixture.store.agents().single.status, AgentState.agentRunning);
  });

  test('a run delta is accepted and set aside', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();

    fixture.delta('local', {
      'kind': 'run',
      'sessionId': 'a-1',
      'at': 1763300000000,
      'run': {'step': 2},
    });
    await settle();
    expect(fixture.store.agents().single.status, AgentState.agentRunning);
  });

  test('a budget delta reads the report again', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();
    expect(fixture.store.budgets('local')?.daily.spentUSD, 1.5);

    fixture.serve('local', '/api/budgets', report(spent: 4.25).toJson());
    fixture.delta('local', {
      'kind': 'budget',
      'sessionId': 'a-1',
      'scope': 'daily',
      'spentUsd': 4.25,
      'capUsd': 10.0,
      'action': 'warn',
    });

    await eventually(
      () => fixture.store.budgets('local')?.daily.spentUSD == 4.25,
      reason: 'the report is read again, not invented from the delta',
    );
  });

  test('a slow budget read does not put older spend back', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();
    expect(fixture.store.budgets('local')?.daily.spentUSD, 1.5);

    // The delta's read is held at the bridge, and answers with the report as
    // it was before the spend moved.
    fixture.serve(
      'local',
      '/api/budgets',
      report(spent: 1.5).toJson(),
      delay: const Duration(milliseconds: 120),
    );
    fixture.delta('local', {
      'kind': 'budget',
      'sessionId': 'a-1',
      'scope': 'daily',
      'spentUsd': 7.0,
      'capUsd': 10.0,
      'action': 'warn',
    });
    await Future<void>.delayed(const Duration(milliseconds: 30));

    // A snapshot meanwhile reads the spend as it is now.
    fixture.serve('local', '/api/budgets', report(spent: 7.0).toJson());
    await fixture.store.refresh('local');
    expect(fixture.store.budgets('local')?.daily.spentUSD, 7.0);

    // The held answer lands after it, superseded, and is discarded.
    await Future<void>.delayed(const Duration(milliseconds: 200));
    expect(
      fixture.store.budgets('local')?.daily.spentUSD,
      7.0,
      reason: 'an answer read before a newer snapshot is not the current spend',
    );
  });

  test('an inbox delta replaces the queue', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();
    expect(fixture.store.inbox(), hasLength(1));

    fixture.delta('local', {
      'kind': 'inbox',
      'inbox': {
        'groups': {
          'needsYou': ['a-1'],
          'running': [],
          'ready': [],
          'earlier': [],
        },
        'queue': [
          {'kind': 'agent', 'agentId': 'a-1', 'at': '2026-11-17T04:00:00Z'},
          {'kind': 'agent', 'agentId': 'a-2', 'at': '2026-11-17T04:05:00Z'},
        ],
      },
    });

    await eventually(() => fixture.store.inbox().length == 2);
    expect(
      [for (final item in fixture.store.inbox()) item.agentID],
      ['a-1', 'a-2'],
    );
  });

  test('an inbox delta reads the agent it names', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();
    expect([for (final a in fixture.store.agents()) a.id], ['a-1']);

    // The stream is already live, so no live transition will read the rows:
    // this test is about what the inbox delta itself does.
    fixture.frames['local']!.write(': hb\n\n');
    await eventually(() => fixture.store.reach('local') == HostReach.live);

    // The bridge names an agent this snapshot did not have: it spawned one
    // mid-session, so the inbox is newer than the rows are.
    fixture.serve('local', '/api/agents', [
      agent(id: 'a-1', status: AgentState.agentRunning).toJson(),
      agent(
        id: 'z-1',
        status: AgentState.agentAwaitingApproval,
        pending: const PendingRequest(
          kind: PendingKind.pendingApproval,
          id: 'tc-9',
          params: {'command': 'git push'},
        ),
      ).toJson(),
    ]);
    fixture.delta('local', {
      'kind': 'inbox',
      'inbox': {
        'groups': {
          'needsYou': ['z-1'],
          'running': ['a-1'],
          'ready': [],
          'earlier': [],
        },
        'queue': [
          {'kind': 'agent', 'agentId': 'z-1', 'at': '2026-11-17T04:10:00Z'},
        ],
      },
    });

    // No reconnect, no gap, no manual refresh: the delta is what brings the
    // row its ask needs.
    await eventually(
      () => fixture.store.agents().any((agent) => agent.id == 'z-1'),
      reason: 'the agent an inbox delta names is read again',
    );
    expect(
      fixture.store
          .agents()
          .where((agent) => agent.id == 'z-1')
          .single
          .pending
          ?.id,
      'tc-9',
    );
  });

  test('removed project drops its agents', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha'), project('/home/u/beta')],
        agents: [
          agent(id: 'a-1', project: '/home/u/alpha'),
          agent(id: 'b-1', project: '/home/u/beta'),
        ],
      ),
    ]);
    await fixture.refresh();
    expect(fixture.store.agents(), hasLength(2));

    fixture.delta('local', {
      'kind': 'project_removed',
      'project': '/home/u/alpha',
    });

    await eventually(() => fixture.store.projects().length == 1);
    expect(
      [for (final p in fixture.store.projects()) p.root],
      ['/home/u/beta'],
    );
    expect([for (final a in fixture.store.agents()) a.id], ['b-1']);
  });

  test('unknown delta is ignored', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();
    final before = fixture.store.agents().single;

    fixture.delta('local', {
      'kind': 'a_delta_from_a_later_bridge',
      'sessionId': 'a-1',
      'something': true,
    });
    // A frame that is not JSON at all is ignored the same way.
    fixture.frames['local']!.write('data: not json\n\n');
    // So is a delta for an agent this host does not have.
    fixture.delta('local', {
      'kind': 'activity',
      'sessionId': 'gone',
      'activity': 'x',
    });
    await settle();

    expect(fixture.store.agents(), hasLength(1));
    expect(fixture.store.agents().single.activity, before.activity);
    expect(fixture.store.failure('local'), isNull);
  });

  test('gap refetches', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();
    final reads = fixture
        .requests('local')
        .where((request) => request.uri == '/api/agents')
        .length;

    fixture.overflow('local');

    await eventually(
      () =>
          fixture
              .requests('local')
              .where((request) => request.uri == '/api/agents')
              .length >
          reads,
      reason:
          'a replay overflow cannot be resumed, so the snapshot is read again',
    );
  });

  test('stale host keeps its rows', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();
    expect(fixture.store.agents(), hasLength(1));

    // The host was live, then said nothing for the whole stale window.
    fixture.frames['local']!.write(': hb\n\n');
    await eventually(
      () => fixture.store.reach('local') == HostReach.live,
      reason: 'a heartbeat is contact',
    );
    fixture.clock.advance(const Duration(seconds: 10));
    await eventually(
      () => fixture.store.reach('local') == HostReach.stale,
      reason: 'silence for the stale window',
    );

    expect(
      fixture.store.agents(),
      hasLength(1),
      reason: 'a stale host keeps its rows',
    );
    expect(fixture.store.inbox(), hasLength(1));
    expect(fixture.store.lastContact('local'), isNotNull);

    // Then the connection went: unreachable, and still nothing is dropped.
    // The fake bridge ends the body rather than cutting the socket mid-frame
    // (`cut()` never reaches the client), which the stream reads the same
    // way: the connection is gone.
    await fixture.frames['local']!.end();
    await eventually(
      () => fixture.store.reach('local') == HostReach.unreachable,
      reason: 'the connection died',
    );
    expect(fixture.store.agents(), hasLength(1));
    expect(fixture.store.projects(), hasLength(1));
  });

  test('a host that comes back is live again and reads its rows', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();

    // Live, then gone: the body ends and the connection is not there.
    fixture.frames['local']!.write(': hb\n\n');
    await eventually(() => fixture.store.reach('local') == HostReach.live);
    await fixture.frames['local']!.end();
    await eventually(
      () => fixture.store.reach('local') == HostReach.unreachable,
      reason: 'the connection died',
    );

    // The bridge comes back, and its snapshot carries what changed meanwhile.
    fixture.serve('local', '/api/agents', [
      agent(
        id: 'a-1',
        status: AgentState.agentRunning,
        activity: 'Back again',
      ).toJson(),
    ]);
    fixture.frames['local']!
      ..reopen()
      ..write(': hb\n\n');

    // The client retries with backoff; the fixture's clock moves it on.
    fixture.clock.advance(const Duration(seconds: 2));

    await eventually(
      () => fixture.store.reach('local') == HostReach.live,
      reason: 'the stream reconnected',
    );
    await eventually(
      () => fixture.store.agents().single.activity == 'Back again',
      reason: 'a host that came back has its rows read again',
    );
  });

  test('a failed read keeps the rows and says what failed', () async {
    final fixture = await fixtureWith();
    await fixture.refresh();
    expect(fixture.store.agents(), hasLength(1));

    fixture.bridges['local']!.reply(
      'GET',
      '/api/agents',
      const FakeReply(500, body: '{"error":"the daemon stopped"}'),
    );
    await fixture.store.refresh('local');

    expect(fixture.store.failure('local'), 'the daemon stopped');
    expect(fixture.store.agents(), hasLength(1));
  });

  test('the snapshot reads the session index', () async {
    final fixture = await fixtureWith();
    fixture.serve(
      'local',
      '/api/sessions/all',
      SessionIndex(
        projects: [
          group('/home/u/alpha', [sessionRow('s-1')]),
          group('/home/u/beta', [sessionRow('t-1')]),
        ],
      ).toJson(),
    );
    await fixture.refresh();

    expect(
      [for (final s in fixture.store.sessions()) s.row.sessionID],
      ['s-1', 't-1'],
      reason: 'the index’s groups are flattened with their host and root',
    );
    final hosted = fixture.store.session('local', 's-1');
    expect(hosted?.host, 'local');
    expect(hosted?.root, '/home/u/alpha');
    expect(
      [
        for (final request in fixture.requests('local'))
          if (request.uri.startsWith('/api/')) request.uri,
      ],
      contains('/api/sessions/all'),
      reason: 'the snapshot reads the index beside the budgets',
    );
  });

  test('an older bridge’s 404 leaves no sessions and fails nothing', () async {
    // fixtureWith's bridge serves no index route, which is what a server
    // profile of an older build looks like: the 404 is that build's answer,
    // not a failure of the snapshot beside it.
    final fixture = await fixtureWith();
    await fixture.refresh();

    expect(fixture.store.sessions(), isEmpty);
    expect(fixture.store.failure('local'), isNull);
    expect(fixture.store.agents(), hasLength(1));
    expect(fixture.store.budgets('local')?.daily.spentUSD, 1.5);
  });

  test('a sessions delta re-reads its root only', () async {
    final fixture = await fixtureWith();
    fixture.serve(
      'local',
      '/api/sessions/all',
      SessionIndex(
        projects: [
          group('/home/u/alpha', [sessionRow('s-1')]),
          group('/home/u/beta', [sessionRow('t-1')]),
        ],
      ).toJson(),
    );
    await fixture.refresh();
    expect(fixture.store.sessions(), hasLength(2));

    // The stream goes live on its first frame, and going live costs a
    // snapshot: write the heartbeat and let that snapshot's index read land
    // before the delta, so this test is about what the delta itself reads,
    // not about what going live re-reads.
    int indexReads() => fixture
        .requests('local')
        .where((request) => request.uri.startsWith('/api/sessions/all'))
        .length;
    final beforeLive = indexReads();
    fixture.frames['local']!.write(': hb\n\n');
    await eventually(() => fixture.store.reach('local') == HostReach.live);
    await eventually(() => indexReads() > beforeLive);
    await settle();

    // The delta names one project and the bridge answers that root only:
    // the read replaces alpha's group and leaves beta's as it was.
    fixture.serve(
      'local',
      '/api/sessions/all',
      SessionIndex(
        projects: [
          group('/home/u/alpha', [sessionRow('s-2')]),
        ],
      ).toJson(),
    );
    fixture.delta('local', {'kind': 'sessions', 'project': '/home/u/alpha'});

    await eventually(() => fixture.store.session('local', 's-2') != null);
    expect(
      [for (final s in fixture.store.sessions()) s.row.sessionID],
      containsAll(['s-2', 't-1']),
      reason: 'the rooted read answers for its root, not for every group',
    );
    expect(
      fixture.requests('local').last.uri,
      contains('root=%2Fhome%2Fu%2Falpha'),
      reason: 'the re-read names its root',
    );
  });

  test('a burst of sessions deltas costs at most two reads', () async {
    final fixture = await fixtureWith();
    fixture.serve(
      'local',
      '/api/sessions/all',
      SessionIndex(
        projects: [
          group('/home/u/alpha', [sessionRow('s-1')]),
        ],
      ).toJson(),
    );
    await fixture.refresh();

    // Going live costs one more snapshot: write the heartbeat and let that
    // snapshot's index read land before the burst, so the burst's own reads
    // are what this test counts, not what going live re-read.
    int indexReads() => fixture
        .requests('local')
        .where((request) => request.uri.startsWith('/api/sessions/all'))
        .length;
    final beforeLive = indexReads();
    fixture.frames['local']!.write(': hb\n\n');
    await eventually(() => fixture.store.reach('local') == HostReach.live);
    await eventually(() => indexReads() > beforeLive);
    await settle();
    final reads = indexReads();

    // The first delta's read is held at the bridge; the deltas that arrive
    // meanwhile are coalesced into one follow-up read.
    fixture.serve(
      'local',
      '/api/sessions/all',
      SessionIndex(
        projects: [
          group('/home/u/alpha', [sessionRow('s-2')]),
        ],
      ).toJson(),
      delay: const Duration(milliseconds: 150),
    );
    for (var i = 0; i < 5; i++) {
      fixture.delta('local', {'kind': 'sessions', 'project': '/home/u/alpha'});
    }

    await eventually(
      () => indexReads() > reads + 1,
      reason: 'the coalesced follow-up read runs',
    );
    expect(
      indexReads(),
      reads + 2,
      reason: 'one read in flight and one follow-up, not one per delta',
    );
  });

  test('a root missing from the answer is removed', () async {
    final fixture = await fixtureWith();
    fixture.serve(
      'local',
      '/api/sessions/all',
      SessionIndex(
        projects: [
          group('/home/u/alpha', [sessionRow('s-1')]),
          group('/home/u/beta', [sessionRow('t-1')]),
        ],
      ).toJson(),
    );
    await fixture.refresh();
    expect(
      [for (final s in fixture.store.sessions()) s.root],
      ['/home/u/alpha', '/home/u/beta'],
    );

    // The rooted read answers with no group for alpha at all: with the
    // project's sessions gone, its group is gone from the store too.
    fixture.serve(
      'local',
      '/api/sessions/all',
      SessionIndex(
        projects: [
          group('/home/u/beta', [sessionRow('t-1')]),
        ],
      ).toJson(),
    );
    await fixture.store.refreshSessions('local', root: '/home/u/alpha');

    expect(
      [for (final s in fixture.store.sessions()) s.root],
      ['/home/u/beta'],
    );
  });

  test('a failed re-read keeps the rows', () async {
    final fixture = await fixtureWith();
    fixture.serve(
      'local',
      '/api/sessions/all',
      SessionIndex(
        projects: [
          group('/home/u/alpha', [sessionRow('s-1')]),
          group('/home/u/beta', [sessionRow('t-1')]),
        ],
      ).toJson(),
    );
    await fixture.refresh();
    expect(fixture.store.sessions(), hasLength(2));

    fixture.bridges['local']!.reply(
      'GET',
      '/api/sessions/all',
      const FakeReply(500, body: '{"error":"the child is down"}'),
    );
    await fixture.store.refreshSessions('local', root: '/home/u/alpha');

    expect(
      [for (final s in fixture.store.sessions()) s.row.sessionID],
      ['s-1', 't-1'],
      reason: 'a failed re-read is not a reason to empty the groups',
    );
    expect(fixture.store.failure('local'), isNull);
  });

  test('a root’s error is reported', () async {
    final fixture = await fixtureWith();
    fixture.serve(
      'local',
      '/api/sessions/all',
      SessionIndex(
        projects: [
          group('/home/u/alpha', [
            sessionRow('s-1'),
          ], error: 'the child is down'),
        ],
      ).toJson(),
    );
    await fixture.refresh();

    expect(
      fixture.store.sessionsError('local', '/home/u/alpha'),
      'the child is down',
    );
    expect(fixture.store.sessionsError('local', '/home/u/beta'), isNull);
  });
}
