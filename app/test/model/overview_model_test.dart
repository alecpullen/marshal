import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/client/host.dart';
import 'package:marshal_app/src/model/factory_store.dart';
import 'package:marshal_app/src/model/inbox_model.dart';
import 'package:marshal_app/src/model/overview_model.dart';
import 'package:marshal_app/src/model/source_state.dart';
import 'package:marshal_app/src/model/thread_ref.dart';

import '../support/fake_bridge.dart';
import 'support.dart';

/// The Overview is a projection (F2 decision 8): every section is a count of
/// real items, and an unreachable host contributes what it last said.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  BudgetReport report({double spent = 2, bool blocked = false}) => BudgetReport(
    budgets: const Budgets(
      dailyUSD: 10,
      perAgentUSD: 2,
      onDailyCap: 'pause',
      onAgentCap: 'pause',
    ),
    daily: BudgetDaily(day: '2026-11-17', spentUSD: spent, blocked: blocked),
    agents: const [],
    loaded: true,
  );

  InboxItem ask(String agentID, Duration age) => InboxItem(
    kind: InboxItemKind.inboxItemAgent,
    agentID: agentID,
    at: now.subtract(age).toIso8601String(),
  );

  test('overview counts equal the store\'s', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha'), project('/home/u/beta')],
        agents: [
          agent(
            id: 'a-run',
            status: AgentState.agentRunning,
            lastActive: now.subtract(const Duration(minutes: 3)),
          ),
          agent(
            id: 'a-ask',
            status: AgentState.agentAwaitingApproval,
            pending: const PendingRequest(
              kind: PendingKind.pendingApproval,
              id: 'tc-1',
            ),
            lastActive: now.subtract(const Duration(minutes: 8)),
          ),
          agent(
            id: 'a-ready',
            status: AgentState.agentIdle,
            changedFiles: 2,
            lastActive: now.subtract(const Duration(minutes: 30)),
          ),
          agent(
            id: 'a-failed',
            project: '/home/u/beta',
            status: AgentState.agentError,
            lastActive: now.subtract(const Duration(hours: 2)),
          ),
          agent(id: 'a-idle', project: '/home/u/beta'),
        ],
        queue: [ask('a-ask', const Duration(minutes: 8))],
        budget: report(spent: 2),
      ),
      FixtureHost(
        name: 'studio',
        kind: HostKind.server,
        projects: [project('/srv/alpha')],
        agents: [
          agent(
            id: 's-run',
            project: '/srv/alpha',
            status: AgentState.agentRunning,
            lastActive: now.subtract(const Duration(minutes: 1)),
          ),
        ],
        budget: report(spent: 4, blocked: true),
      ),
    ], now: now);
    await fixture.refresh();
    final inbox = InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(inbox.dispose);

    final overview = buildOverview(fixture.store, inbox);

    // Running now: every thread with a turn in flight, most recent first.
    expect(
      [for (final thread in overview.running) thread.id],
      ['s-run', 'a-run'],
    );
    expect(
      overview.running.every((thread) => thread.status == ThreadStatus.running),
      isTrue,
    );

    // Projects: one row per project, in name order, counts that add up to the
    // fleet's own.
    expect(
      [for (final row in overview.projects) row.name],
      ['alpha', 'alpha', 'beta'],
    );
    expect(
      overview.projects.fold<int>(0, (sum, row) => sum + row.threads),
      fixture.store.agents().length,
    );
    final beta = overview.projects.firstWhere(
      (row) => row.id == '/home/u/beta',
    );
    expect([beta.failed, beta.idle, beta.threads], [1, 1, 2]);
    final alpha = overview.projects.firstWhere(
      (row) => row.id == '/home/u/alpha',
    );
    expect([alpha.running, alpha.needsYou, alpha.ready], [1, 1, 1]);

    // Capacity: one row per host, and its threads add up too.
    expect(
      [for (final row in overview.capacity) row.host],
      ['local', 'studio'],
    );
    expect(
      overview.capacity.fold<int>(0, (sum, row) => sum + row.threads),
      fixture.store.agents().length,
    );
    expect(overview.capacity.first.needsYou, 1);

    // Spend: the hosts' reports together, and the cap nobody guessed at.
    expect(overview.spend?.spentUSD, 6);
    expect(overview.spend?.capUSD, 20);
    expect(overview.spend?.blocked, isTrue);

    // Needs you: the oldest asks, and no more than the design's three.
    expect([for (final waiting in overview.needsYou) waiting.id], ['tc-1']);
  });

  test('needs you is at most three asks', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          for (var i = 0; i < 5; i++)
            agent(
              id: 'a-$i',
              status: AgentState.agentAwaitingApproval,
              pending: PendingRequest(
                kind: PendingKind.pendingApproval,
                id: 'tc-$i',
              ),
              lastActive: now.subtract(Duration(minutes: 10 + i)),
            ),
        ],
        queue: [
          for (var i = 0; i < 5; i++) ask('a-$i', Duration(minutes: 10 + i)),
        ],
      ),
    ], now: now);
    await fixture.refresh();
    final inbox = InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(inbox.dispose);

    final overview = buildOverview(fixture.store, inbox);
    expect(inbox.asks, hasLength(5));
    expect(
      [for (final waiting in overview.needsYou) waiting.id],
      ['tc-4', 'tc-3', 'tc-2'],
    );
    expect(overviewAsks, 3);
  });

  test('an empty fleet has no spend and no rows', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local'),
    ], now: now);
    await fixture.refresh();
    final inbox = InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(inbox.dispose);

    final overview = buildOverview(fixture.store, inbox);
    expect(overview.needsYou, isEmpty);
    expect(overview.running, isEmpty);
    expect(overview.projects, isEmpty);
    expect(overview.spend, isNull);
    // The host is still listed: a host with nothing on it is not nothing.
    expect(overview.capacity.single.threads, 0);
  });

  test('the waiting total counts every ask while the drawn list stays capped', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          for (var i = 0; i < 5; i++)
            agent(
              id: 'a-$i',
              status: AgentState.agentAwaitingApproval,
              pending: PendingRequest(
                kind: PendingKind.pendingApproval,
                id: 'tc-$i',
              ),
              lastActive: now.subtract(Duration(minutes: 10 + i)),
            ),
        ],
        queue: [
          for (var i = 0; i < 5; i++) ask('a-$i', Duration(minutes: 10 + i)),
        ],
      ),
    ], now: now);
    await fixture.refresh();
    final inbox = InboxModel(fixture.store, client: fixture.clientFor);
    addTearDown(inbox.dispose);

    final overview = buildOverview(fixture.store, inbox);

    // The design draws three cards, and the count is what exists: a label that
    // says 3 for a queue of 5 under-reports the queue.
    expect(overview.needsYou, hasLength(3));
    expect(overview.needsYouTotal, 5);
    expect(inbox.asks, hasLength(5));
  });

  test('the model carries one freshness row per host and source', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [agent(id: 'a-1')],
        factoryItems: [factoryItem(id: 'item-1')],
      ),
      FixtureHost(
        name: 'studio',
        kind: HostKind.server,
        projects: [project('/srv/alpha')],
      ),
    ], now: now);
    await fixture.refresh();
    final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
    addTearDown(factory.dispose);
    await factory.refresh();
    final inbox = InboxModel(
      fixture.store,
      client: fixture.clientFor,
      factory: factory,
    );
    addTearDown(inbox.dispose);

    final overview = buildOverview(fixture.store, inbox);

    expect(
      [
        for (final source in overview.sources)
          '${source.host}/${source.kind.name}',
      ],
      ['local/fleet', 'local/factory', 'studio/fleet', 'studio/factory'],
    );
    expect(
      {for (final source in overview.sources) source.kind},
      {SourceKind.fleet, SourceKind.factory},
    );
    expect(overview.sources.first.kind, SourceKind.fleet);
    // The factory answered, so its rows are loaded, not merely present.
    expect(
      overview.sources
          .where((source) => source.kind == SourceKind.factory)
          .every((source) => source.loaded),
      isTrue,
    );
  });

  test(
    'a factory read failure leaves the fleet\'s project counts intact',
    () async {
      final fixture = await ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/alpha')],
          agents: [
            agent(
              id: 'a-1',
              status: AgentState.agentRunning,
              lastActive: now.subtract(const Duration(minutes: 3)),
            ),
          ],
        ),
      ], now: now);
      await fixture.refresh();
      final factory = FactoryStore(fixture.registry, now: fixture.clock.now);
      addTearDown(factory.dispose);
      fixture.bridges['local']!.reply(
        'GET',
        '/api/factory/items',
        const FakeReply(
          500,
          headers: {'content-type': 'application/json'},
          body: '{"error":"offline"}',
        ),
      );
      await factory.refresh('local');
      expect(factory.loaded('local'), isFalse);
      final inbox = InboxModel(
        fixture.store,
        client: fixture.clientFor,
        factory: factory,
      );
      addTearDown(inbox.dispose);

      final overview = buildOverview(fixture.store, inbox);

      // One dead feed is named as itself: it does not empty the fleet's rows.
      expect([for (final row in overview.projects) row.id], ['/home/u/alpha']);
      expect(overview.projects.single.running, 1);
      expect(overview.capacity.single.host, 'local');
      expect(overview.capacity.single.threads, 1);

      final factorySource = overview.sources.singleWhere(
        (source) => source.kind == SourceKind.factory,
      );
      expect(factorySource.failure, 'offline');
      expect(factorySource.healthy, isFalse);
      // The factory never answered at all, so it holds no snapshot to be
      // stale: it is unavailable, and says so.
      expect(factorySource.unavailable, isTrue);
      expect(factorySource.stale, isFalse);
    },
  );
}
