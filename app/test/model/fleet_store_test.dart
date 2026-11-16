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
}
