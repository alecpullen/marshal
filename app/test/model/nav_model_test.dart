import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/thread_ref.dart';

import 'support.dart';

/// The sidebar's rules (F2 decision 3), ported from
/// `web/ui/src/lib/nav/model.test.ts`: the fixed places in the design's
/// order, projects in name order, each project's threads split into current
/// and earlier, and the inbox count from the store.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  test('the fixed places are the design\'s order', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local'),
    ], now: now);
    final model = buildNavModel(const Place.overview(), fixture.store);

    expect(
      [for (final place in model.fixed) place.label],
      [
        'Overview',
        'Inbox',
        'Pull requests',
        'Automations',
        'Library',
        'Settings',
      ],
    );
    expect(
      [for (final place in model.fixed) place.kind],
      [
        PlaceKind.overview,
        PlaceKind.inbox,
        PlaceKind.pulls,
        PlaceKind.automations,
        PlaceKind.library,
        PlaceKind.settings,
      ],
    );
    // All six open a place now: F5 brought Pull requests and F7 brought
    // Automations, Library and Settings. The model carries no "planned" flag —
    // a place that cannot be drawn says so on the screen, not in the sidebar.
    expect(navAboveProjects, 3);
  });

  test('the current place is the active row', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local', projects: []),
    ], now: now);

    List<PlaceKind> active(Place place) => [
      for (final row in buildNavModel(place, fixture.store).fixed)
        if (row.active) row.kind,
    ];

    expect(active(const Place.overview()), [PlaceKind.overview]);
    expect(active(const Place(PlaceKind.inbox)), [PlaceKind.inbox]);
    expect(active(const Place(PlaceKind.pulls)), [PlaceKind.pulls]);
    expect(active(const Place(PlaceKind.settings)), [PlaceKind.settings]);
    // A placed thread lights none of the fixed rows.
    expect(
      active(const Place(PlaceKind.thread, host: 'local', id: 'a-1')),
      isEmpty,
    );
  });

  test('projects sort by name then id', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [
          project('/work/zulu/alpha'),
          project('/work/bravo/beta'),
          project('/work/alpha/alpha'),
        ],
      ),
    ], now: now);
    await fixture.refresh();

    final model = buildNavModel(const Place.overview(), fixture.store);
    expect(
      [for (final row in model.projects) '${row.name} ${row.id}'],
      [
        'alpha /work/alpha/alpha',
        'alpha /work/zulu/alpha',
        'beta /work/bravo/beta',
      ],
    );
  });

  test('a project is named by its last part and never blank', () {
    expect(projectName('/home/u/alpha'), 'alpha');
    // Trailing slashes do not leave an empty name behind.
    expect(projectName('/home/u/alpha/'), 'alpha');
    expect(projectName('/home/u/alpha//'), 'alpha');
    // A root with no directory of its own is the filesystem's root, so the
    // row is never blank: not for `/`, and not for an empty one.
    expect(projectName('/'), '/');
    expect(projectName(''), '/');
    expect(projectName('//'), '/');
  });

  test('earlier counts idle agents older than a day', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [
          // Current: active within the day, whatever its state.
          agent(
            id: 'recent',
            lastActive: now.subtract(const Duration(hours: 2)),
          ),
          // Earlier: idle and quiet for more than a day.
          agent(id: 'old', lastActive: now.subtract(const Duration(days: 2))),
          agent(id: 'older', lastActive: now.subtract(const Duration(days: 9))),
          // Current: quiet for days, but failed rather than idle — nothing
          // about an error is "earlier" work.
          agent(
            id: 'failed',
            status: AgentState.agentError,
            lastActive: now.subtract(const Duration(days: 3)),
          ),
          // Current: just inside the day.
          agent(
            id: 'edge',
            lastActive: now.subtract(const Duration(hours: 23)),
          ),
        ],
      ),
    ], now: now);
    await fixture.refresh();

    final row = buildNavModel(
      const Place.overview(),
      fixture.store,
    ).projects.single;
    expect(row.earlier, 2);
    expect(
      [for (final thread in row.current) thread.id],
      ['recent', 'edge', 'failed'],
    );
  });

  test(
    'a project\'s threads are the fleet\'s agents, most recent first',
    () async {
      final fixture = await ModelFixture.start([
        FixtureHost(
          name: 'local',
          projects: [project('/home/u/alpha'), project('/home/u/beta')],
          agents: [
            agent(
              id: 'a-1',
              name: 'Cache the reads',
              status: AgentState.agentRunning,
              lastActive: now.subtract(const Duration(minutes: 4)),
            ),
            agent(
              id: 'a-2',
              status: AgentState.agentAwaitingApproval,
              pending: const PendingRequest(
                kind: PendingKind.pendingApproval,
                id: 'tc-1',
              ),
              lastActive: now.subtract(const Duration(minutes: 9)),
            ),
            agent(id: 'b-1', project: '/home/u/beta'),
          ],
        ),
      ], now: now);
      await fixture.refresh();

      final model = buildNavModel(const Place.overview(), fixture.store);
      final alpha = model.projects.firstWhere(
        (row) => row.id == '/home/u/alpha',
      );
      expect([for (final thread in alpha.current) thread.id], ['a-1', 'a-2']);
      expect(
        [for (final thread in alpha.current) thread.status],
        [ThreadStatus.running, ThreadStatus.needsYou],
      );
      expect(alpha.current.first.title, 'Cache the reads');
      expect(
        [for (final thread in model.projects.last.current) thread.id],
        ['b-1'],
      );
    },
  );

  test('an open thread lights its project row', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha'), project('/home/u/beta')],
        agents: [agent(id: 'a-1')],
      ),
    ], now: now);
    await fixture.refresh();

    final model = buildNavModel(
      const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
      fixture.store,
    );
    expect([for (final row in model.projects) row.active], [true, false]);
  });

  test('inbox count follows the store', () async {
    InboxItem ask(String id, DateTime at) => InboxItem(
      kind: InboxItemKind.inboxItemAgent,
      agentID: id,
      at: at.toIso8601String(),
    );
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        queue: [
          ask('a-1', now.subtract(const Duration(minutes: 20))),
          ask('a-2', now.subtract(const Duration(minutes: 5))),
        ],
      ),
    ], now: now);

    // Before the first read the queue is empty, and the row says nothing.
    expect(
      buildNavModel(const Place.overview(), fixture.store).fixed[1].count,
      0,
    );

    await fixture.refresh();
    expect(
      buildNavModel(const Place.overview(), fixture.store).fixed[1].count,
      2,
    );

    // Nothing else carries the queue's size. Pull requests carries its own
    // count — how many wait for review (F5 decision 14).
    expect(
      [
        for (final row in buildNavModel(
          const Place.overview(),
          fixture.store,
        ).fixed)
          if (row.count != null) row.label,
      ],
      ['Inbox', 'Pull requests'],
    );
  });

  test('two hosts are one tree', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha')],
        agents: [agent(id: 'a-1')],
      ),
      FixtureHost(
        name: 'studio',
        agents: [
          agent(id: 'a-2', project: '/srv/alpha'),
          agent(id: 'a-3', project: '/srv/beta'),
        ],
        projects: [project('/srv/alpha'), project('/srv/beta')],
      ),
    ], now: now);
    await fixture.refresh();

    final model = buildNavModel(const Place.overview(), fixture.store);
    expect(
      [for (final row in model.projects) row.name],
      ['alpha', 'alpha', 'beta'],
    );
    final alpha = model.projects.firstWhere((row) => row.id == '/srv/alpha');
    expect(alpha.current.single.host, 'studio');
  });
}
