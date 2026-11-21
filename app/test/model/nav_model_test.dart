import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/contract.g.dart';
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/thread_ref.dart';

import 'support.dart';

/// The sidebar's rules (F2 decision 3), ported from
/// `web/ui/src/lib/nav/model.test.ts`: the fixed places in the design's
/// order, projects in name order, each project's threads split into current
/// and earlier, and the Needs you count from the store.
void main() {
  final now = DateTime.utc(2026, 11, 17, 12);

  /// One session row as a host's index reports one.
  Map<String, Object?> session({
    String id = 'sess_0123456789abcdef',
    String? title,
    DateTime? lastActive,
    bool open = true,
    bool busy = false,
    int viewers = 0,
  }) => {
    'sessionId': id,
    'title': ?title,
    'updatedAt': (lastActive ?? now).millisecondsSinceEpoch,
    'open': open,
    'viewers': viewers,
    'busy': busy,
  };

  /// Serves [host]'s session index: each project root and its rows.
  void serveSessions(
    ModelFixture fixture,
    String host,
    Map<String, List<Map<String, Object?>>> byRoot,
  ) => fixture.serve(host, '/api/sessions/all', {
    'projects': [
      for (final entry in byRoot.entries)
        {'root': entry.key, 'sessions': entry.value},
    ],
  });

  test('the fixed places are the design\'s order', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local'),
    ], now: now);
    final model = buildNavModel(const Place.overview(), fixture.store);

    expect(
      [for (final place in model.fixed) place.label],
      [
        'Overview',
        'Needs you',
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
    // The earlier threads are the ones left, in the same order: most recently
    // active first.
    expect(
      [for (final thread in row.earlierThreads) thread.id],
      ['old', 'older'],
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

  test('an open thread lights only its own row', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha'), project('/home/u/beta')],
        agents: [agent(id: 'a-1')],
      ),
    ], now: now);
    await fixture.refresh();

    // The thread is open, not its project: the thread's row is where the
    // person is, so no project row is filled.
    final model = buildNavModel(
      const Place(PlaceKind.thread, host: 'local', id: 'a-1'),
      fixture.store,
    );
    expect([for (final row in model.projects) row.active], [false, false]);
  });

  test('the project page lights its project row', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(
        name: 'local',
        projects: [project('/home/u/alpha'), project('/home/u/beta')],
        agents: [agent(id: 'a-1')],
      ),
    ], now: now);
    await fixture.refresh();

    final model = buildNavModel(
      const Place(PlaceKind.project, id: '/home/u/beta'),
      fixture.store,
    );
    expect([for (final row in model.projects) row.active], [false, true]);
  });

  test('the Needs you count follows the store', () async {
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

    // Before the first read the queue is empty, and the row says nothing: an
    // empty count is no count, so no `0` is drawn beside the label.
    expect(
      buildNavModel(const Place.overview(), fixture.store).fixed[1].count,
      isNull,
    );

    await fixture.refresh();
    expect(
      buildNavModel(const Place.overview(), fixture.store).fixed[1].count,
      2,
    );

    // Nothing else carries the queue's size. Pull requests carries its own
    // count — how many wait for review (F5 decision 14) — and none does
    // here, so only the Needs you row with something waiting is counted.
    expect(
      [
        for (final row in buildNavModel(
          const Place.overview(),
          fixture.store,
        ).fixed)
          if (row.count != null) row.label,
      ],
      ['Needs you'],
    );
  });

  test('an old Inbox link opens Needs you', () async {
    final fixture = await ModelFixture.start([
      const FixtureHost(name: 'local', projects: []),
    ], now: now);

    // The link written before the rename is the same place: the kind, the
    // route and `Place.inbox()` are untouched, and only the visible words
    // changed. Opening it lights the Needs you row and nothing else.
    const legacy = Place.inbox();
    expect(legacy.kind, PlaceKind.inbox);
    final active = [
      for (final row in buildNavModel(legacy, fixture.store).fixed)
        if (row.active) row,
    ];
    expect(active.single.kind, PlaceKind.inbox);
    expect(active.single.label, 'Needs you');
  });

  test(
    'Overview, Needs you and Pull requests are primary navigation',
    () async {
      final fixture = await ModelFixture.start([
        const FixtureHost(name: 'local', projects: []),
      ], now: now);
      final fixed = buildNavModel(const Place.overview(), fixture.store).fixed;

      // The fleet screens sit above the project tree, and the management places
      // stay reachable at its foot rather than competing with them.
      expect(
        [for (final row in fixed.take(navAboveProjects)) row.label],
        ['Overview', 'Needs you', 'Pull requests'],
      );
      expect(
        [for (final row in fixed.skip(navAboveProjects)) row.kind],
        [PlaceKind.automations, PlaceKind.library, PlaceKind.settings],
      );
    },
  );

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

  test('an open session is a current thread under its project', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
    ], now: now);
    serveSessions(fixture, 'local', {
      '/home/u/alpha': [
        session(
          title: 'Cache the reads',
          lastActive: now.subtract(const Duration(minutes: 3)),
        ),
      ],
    });
    await fixture.refresh();

    final row = buildNavModel(
      const Place.overview(),
      fixture.store,
    ).projects.single;
    expect(
      [for (final thread in row.current) thread.id],
      ['sess_0123456789abcdef'],
    );
    final thread = row.current.single;
    expect(thread.kind, ThreadKind.session);
    expect(thread.title, 'Cache the reads');
    expect(thread.project, '/home/u/alpha');
    expect(thread.host, 'local');
    expect(row.earlier, 0);
  });

  test('a closed session counts toward earlier', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
    ], now: now);
    serveSessions(fixture, 'local', {
      '/home/u/alpha': [
        // Active a minute ago, and closed: age does not keep it current.
        session(
          open: false,
          lastActive: now.subtract(const Duration(minutes: 1)),
        ),
        session(
          id: 'sess_ffffffffffffffff',
          lastActive: now.subtract(const Duration(minutes: 2)),
        ),
      ],
    });
    await fixture.refresh();

    final row = buildNavModel(
      const Place.overview(),
      fixture.store,
    ).projects.single;
    expect(row.earlier, 1);
    expect(
      [for (final thread in row.current) thread.id],
      ['sess_ffffffffffffffff'],
    );
    expect(
      [for (final thread in row.earlierThreads) thread.id],
      ['sess_0123456789abcdef'],
    );
  });

  test('a session on an unlisted project still has its project row', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(name: 'local', projects: []),
    ], now: now);
    serveSessions(fixture, 'local', {
      '/home/u/gamma': [session()],
    });
    await fixture.refresh();

    final row = buildNavModel(
      const Place.overview(),
      fixture.store,
    ).projects.single;
    expect(row.id, '/home/u/gamma');
    expect(row.name, 'gamma');
    expect(
      [for (final thread in row.current) thread.kind],
      [ThreadKind.session],
    );
  });

  test('sessions on two hosts share one project row', () async {
    final fixture = await ModelFixture.start([
      FixtureHost(name: 'local', projects: [project('/home/u/alpha')]),
      FixtureHost(name: 'studio', projects: [project('/home/u/alpha')]),
    ], now: now);
    serveSessions(fixture, 'local', {
      '/home/u/alpha': [
        session(
          id: 'sess_1111111111111111',
          lastActive: now.subtract(const Duration(minutes: 2)),
        ),
      ],
    });
    serveSessions(fixture, 'studio', {
      '/home/u/alpha': [
        session(
          id: 'sess_2222222222222222',
          lastActive: now.subtract(const Duration(minutes: 4)),
        ),
      ],
    });
    await fixture.refresh();

    final row = buildNavModel(
      const Place.overview(),
      fixture.store,
    ).projects.single;
    expect(row.id, '/home/u/alpha');
    expect(
      [for (final thread in row.current) thread.id],
      ['sess_1111111111111111', 'sess_2222222222222222'],
    );
    expect(
      [for (final thread in row.current) thread.host],
      ['local', 'studio'],
    );
  });

  // The sidebar's search: what is typed narrows the tree rather than opening a
  // page of its own, so these are the rules the field's rows follow.
  group('the search', () {
    NavPlace place(PlaceKind kind, String label) =>
        NavPlace(kind: kind, label: label);

    ThreadRef thread(
      String id,
      String title, {
      String project = '/home/u/alpha',
    }) => ThreadRef(
      host: 'local',
      id: id,
      project: project,
      title: title,
      kind: ThreadKind.agent,
      status: ThreadStatus.idle,
      headline: '',
      lastActive: now,
    );

    final fixed = [
      place(PlaceKind.overview, 'Overview'),
      place(PlaceKind.inbox, 'Needs you'),
      place(PlaceKind.pulls, 'Pull requests'),
    ];

    final projects = [
      NavProject(
        id: '/home/u/alpha',
        name: 'alpha',
        current: [
          thread('a-1', 'Cache the reads'),
          thread('a-2', 'Fix the login bug'),
        ],
        earlierThreads: [thread('a-3', 'Retire the old bridge')],
      ),
      NavProject(
        id: '/home/u/beta',
        name: 'Beta service',
        current: [thread('b-1', 'Cache the writes', project: '/home/u/beta')],
      ),
    ];

    test('search matches every word, in any case', () {
      // Every word must appear in the title, and case is not the question:
      // only the thread that says both words is found.
      expect(
        [
          for (final thread in searchNav(
            fixed,
            projects,
            'CACHE Reads',
          ).threads)
            thread.id,
        ],
        ['a-1'],
      );

      // A word may match anywhere in the name: a place by its label, a project
      // by its own name.
      expect(
        [
          for (final place in searchNav(fixed, projects, 'PULL').places)
            place.label,
        ],
        ['Pull requests'],
      );
      expect(
        [
          for (final project in searchNav(
            fixed,
            projects,
            'BETA service',
          ).projects)
            project.name,
        ],
        ['Beta service'],
      );

      // One word alone finds everything that says it, and nothing else.
      final cache = searchNav(fixed, projects, 'cache');
      expect([for (final thread in cache.threads) thread.id], ['a-1', 'b-1']);
      expect(cache.places, isEmpty);
      expect(cache.projects, isEmpty);
    });

    test('search finds earlier threads', () {
      final found = searchNav(fixed, projects, 'bridge');
      expect([for (final thread in found.threads) thread.id], ['a-3']);
      expect(found.places, isEmpty);
      expect(found.projects, isEmpty);

      // Threads keep the tree's order however wide the query: project by
      // project as listed, and a project's current threads before the ones
      // under its "Earlier".
      expect(
        [
          for (final thread in searchNav(fixed, projects, 'the').threads)
            thread.id,
        ],
        ['a-1', 'a-2', 'a-3', 'b-1'],
      );
    });

    test('a place is found by its visible name', () {
      // The place the code still calls the inbox is called Needs you on
      // screen, so that is what the search finds — and the old word finds
      // nothing at all, because no row says it any more.
      final needsYou = searchNav(fixed, projects, 'needs you');
      expect(
        [for (final place in needsYou.places) place.kind],
        [PlaceKind.inbox],
      );
      expect(needsYou.places.single.label, 'Needs you');
      expect(searchNav(fixed, projects, 'inbox').places, isEmpty);
    });

    test('a blank query finds nothing', () {
      // Whitespace is nothing typed: the sidebar keeps its tree rather than
      // drawing every place, thread and project as a "result".
      for (final query in ['', ' ', '   ', '\t\n ']) {
        final found = searchNav(fixed, projects, query);
        expect(found.isEmpty, isTrue, reason: 'query "$query"');
        expect(found.places, isEmpty);
        expect(found.threads, isEmpty);
        expect(found.projects, isEmpty);
      }
    });
  });
}
