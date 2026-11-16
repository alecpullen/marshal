import 'package:marshal_app/src/client/contract.g.dart' show Recipe, Schedule;
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/bots_model.dart';
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/overview_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/recipes_model.dart';
import 'package:marshal_app/src/model/schedules_model.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/model/watches_model.dart';
import 'package:marshal_app/src/ui/automations/automations_place.dart';
import 'package:marshal_app/src/ui/automations/bots_tab.dart';
import 'package:marshal_app/src/ui/automations/placeholder.dart';
import 'package:marshal_app/src/ui/automations/recipes_tab.dart';
import 'package:marshal_app/src/ui/automations/schedules_tab.dart';
import 'package:marshal_app/src/ui/automations/watches_tab.dart';
import 'package:marshal_app/src/ui/debug/connection_check.dart';
import 'package:marshal_app/src/ui/gallery/gallery.dart';
import 'package:marshal_app/src/ui/inbox/inbox.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/overview/overview.dart';
import 'package:marshal_app/src/ui/shell/placeholder.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';

/// One screen the enforcement tests pump: its ready state with fixture data,
/// and one case for each §7 state it handles.
class ScreenCase {
  const ScreenCase({
    required this.name,
    required this.ready,
    required this.states,
  });

  final String name;

  /// The screen with fixture data and nothing wrong.
  final Widget Function() ready;

  /// The screen in each data state. design-system.md §7 requires loading,
  /// empty, stale, error and unavailable (`states_test.dart`).
  final Map<StateKind, Widget Function()> states;

  /// Every way the screen can be pumped, by name.
  Map<String, Widget Function()> get cases => {
    'ready': ready,
    for (final MapEntry(key: kind, value: build) in states.entries)
      kind.name: build,
  };
}

/// The hosts the connection check shows in the tests: one live host with
/// agents, and one unreachable host whose count could not be read.
final _checkHosts = [
  HostRow(
    name: 'local',
    kind: 'local',
    reach: HostReachView.live,
    lastContact: DateTime.utc(2026, 11, 17, 4, 8),
    agentCount: 3,
  ),
  HostRow(
    name: 'studio',
    kind: 'server',
    reach: HostReachView.unreachable,
    lastContact: DateTime.utc(2026, 11, 17, 3, 55),
    agentCount: null,
  ),
];

/// The moment the shell's fixtures are measured against.
final shellNow = DateTime.utc(2026, 11, 17, 4, 12);

/// The hosts the shell's fixture lists: one answering, one away for four
/// minutes.
final shellHosts = [
  HostView(
    name: 'local',
    kind: HostKind.local,
    reach: HostReach.live,
    lastContact: shellNow,
  ),
  HostView(
    name: 'studio',
    kind: HostKind.server,
    reach: HostReach.unreachable,
    lastContact: shellNow.subtract(const Duration(minutes: 4)),
  ),
];

model.ThreadRef _shellThread(
  String id,
  String title,
  model.ThreadStatus status,
) => model.ThreadRef(
  host: 'local',
  id: id,
  project: '/home/u/marshal',
  title: title,
  kind: model.ThreadKind.agent,
  status: status,
  headline: 'Reading internal/acp',
  lastActive: shellNow.subtract(const Duration(minutes: 8)),
);

/// The sidebar the shell's fixture draws, at [place].
NavModel shellNavFor(Place place) => NavModel(
  fixed: [
    NavPlace(
      kind: PlaceKind.overview,
      label: 'Overview',
      active: place.kind == PlaceKind.overview,
    ),
    NavPlace(
      kind: PlaceKind.inbox,
      label: 'Inbox',
      count: 3,
      active: place.kind == PlaceKind.inbox,
    ),
    for (final kind in const [
      PlaceKind.pulls,
      PlaceKind.automations,
      PlaceKind.library,
      PlaceKind.settings,
    ])
      NavPlace(
        kind: kind,
        label: switch (kind) {
          PlaceKind.pulls => 'Pull requests',
          PlaceKind.automations => 'Automations',
          PlaceKind.library => 'Library',
          _ => 'Settings',
        },
        planned: true,
        active: place.kind == kind,
      ),
  ],
  projects: [
    NavProject(
      id: '/home/u/marshal',
      name: 'marshal',
      current: [
        _shellThread(
          'a-1',
          'Reliable session recovery',
          model.ThreadStatus.running,
        ),
        _shellThread('a-2', 'Web studio contract', model.ThreadStatus.needsYou),
        _shellThread('a-3', 'Bump the gate', model.ThreadStatus.ready),
      ],
      earlier: 12,
      active: place.kind == PlaceKind.thread && place.id == 'a-1',
    ),
  ],
);

/// The shell with its fixture data, in [state] when one is given.
Widget _shell({StateKind? state, Place place = const Place.overview()}) {
  final nav = shellNavFor(place);
  return Shell(
    nav: nav,
    hosts: shellHosts,
    place: place,
    state: state,
    now: shellNow,
    onRetry: () {},
    onSelect: (_) {},
    content: plannedPlace(place, nav),
  );
}

/// One ask in the Overview's fixture.
model.Ask _overviewAsk(
  String id,
  String title,
  model.AskKind kind,
  Duration age,
) => model.Ask(
  host: 'local',
  id: id,
  kind: kind,
  thread: _shellThread(
    'a-1',
    'Reliable session recovery',
    model.ThreadStatus.needsYou,
  ),
  title: title,
  body: kind == model.AskKind.approval ? 'The branch is ready' : '',
  actions: const [
    model.AskAction('Approve once', primary: true),
    model.AskAction('Deny'),
  ],
  since: shellNow.subtract(age),
);

/// The Overview's fixture: two asks, one running thread, one project, two
/// hosts and today's spend.
OverviewModel _overviewModel() => OverviewModel(
  needsYou: [
    _overviewAsk(
      'tc-1',
      'git push',
      model.AskKind.approval,
      const Duration(minutes: 12),
    ),
    _overviewAsk(
      'q-1',
      'Which branch?',
      model.AskKind.question,
      const Duration(minutes: 30),
    ),
  ],
  running: [
    _shellThread(
      'a-1',
      'Reliable session recovery',
      model.ThreadStatus.running,
    ),
  ],
  projects: const [
    ProjectSummary(
      id: '/home/u/marshal',
      name: 'marshal',
      needsYou: 2,
      running: 1,
      ready: 1,
      failed: 0,
      idle: 9,
    ),
  ],
  capacity: [
    HostCapacity(
      host: 'local',
      reach: HostReach.live,
      lastContact: shellNow,
      threads: 3,
      needsYou: 2,
    ),
    HostCapacity(
      host: 'studio',
      reach: HostReach.unreachable,
      lastContact: shellNow.subtract(const Duration(minutes: 4)),
      threads: 1,
      needsYou: 0,
    ),
  ],
  spend: const SpendSummary(
    day: '2026-11-17',
    spentUSD: 4.5,
    capUSD: 10,
    blocked: false,
  ),
);

Widget _inbox({StateKind? state}) => Inbox(
  asks: [
    _overviewAsk(
      'tc-1',
      'git push',
      model.AskKind.approval,
      const Duration(minutes: 12),
    ),
    _overviewAsk(
      's-1',
      'Fix the flaky test',
      model.AskKind.intake,
      const Duration(minutes: 30),
    ),
  ],
  now: shellNow,
  state: state,
  onRetry: () {},
  onSelect: (_) {},
  onAnswer: (_, _, {reply}) {},
);

Widget _overview({StateKind? state}) => Overview(
  overview: _overviewModel(),
  now: shellNow,
  hosts: shellHosts,
  automations: [
    TrippedWatch(
      name: 'the gate',
      agent: 'Agent a-1',
      action: 'Reroutes implementer to local',
      age: '4m',
    ),
  ],
  state: state,
  onRetry: () {},
  onSelect: (_) {},
  onAnswer: (_, _) {},
);

/// The schedules the Automations fixture lists: one that ran, one paused.
SchedulesModel _schedulesFixture() => SchedulesModel.fixture(const [
  Schedule(
    id: 's-1',
    name: 'nightly gate',
    recipe: 'gate',
    project: '/home/u/marshal',
    cron: '0 3 * * *',
    enabled: true,
    lastRun: '2026-11-17T03:00:00Z',
    lastResult: 'passed',
    ownerID: 'owner-1',
  ),
  Schedule(
    id: 's-2',
    name: 'weekly review',
    recipe: 'review',
    cron: '0 9 * * 1',
    enabled: false,
    ownerID: 'owner-1',
  ),
]);

/// The recipes the Automations fixture lists: one of the bridge's, one of the
/// user's.
RecipesModel _recipesFixture() => RecipesModel.fixture(const [
  Recipe(
    name: 'gate',
    title: 'The gate',
    kind: 'prompt',
    prompt: 'Run the gate on {{ branch }}',
    builtin: true,
  ),
  Recipe(
    name: 'nightly-gate',
    title: 'Nightly gate',
    kind: 'prompt',
    prompt: 'Run the gate',
    builtin: false,
  ),
]);

/// The watches the Automations fixture lists: one that tripped with a
/// reroute, and one watching quietly from the bridge's own session.
WatchesModel _watchesFixture() => WatchesModel.fixture([
  WatchInfo(
    agentID: 'a-1',
    id: 'w-1',
    name: 'the gate',
    kind: 'command',
    state: 'fired',
    condition: 'exit != 0',
    mode: 'interval',
    intervalMs: 5000,
    owner: 'a-1',
    fireCount: 2,
    lastSample: 'exit 1',
    createdAt: 1763352000000,
    lastFiredAt: shellNow.millisecondsSinceEpoch - 4 * 60 * 1000,
    samples: const [
      WatchSample(at: 1763352300000, value: 0, tripped: false),
      WatchSample(at: 1763352600000, value: 1, tripped: true),
    ],
    onTrip: const OnTrip(
      notify: true,
      resume: false,
      reroute: RerouteRule(role: 'implementer', preset: 'local'),
    ),
    notify: true,
    resume: false,
  ),
  WatchInfo(
    agentID: 'studio',
    id: 'w-2',
    name: 'the queue',
    kind: 'file',
    state: 'watching',
    condition: 'the file appears',
    mode: 'interval',
    intervalMs: 60000,
    fireCount: 0,
    createdAt: 1763352000000,
    samples: const [
      WatchSample(at: 1763352300000, value: 0, tripped: false),
      WatchSample(at: 1763352600000, value: 0, tripped: false),
    ],
  ),
], now: shellNow);

/// The bots the Automations fixture lists: the review bot with one held
/// review, and the CI fixer with one recent run.
BotsModel _botsFixture() => BotsModel.fixture(
  [
    Bot(
      kind: BotKind.review,
      enabled: true,
      repoID: 'acme/widgets',
      summary: '1 review waits for you',
      facts: const [
        ('State', 'On'),
        ('Repository', 'acme/widgets'),
        ('Reviews', 'labelled ready · drafts too'),
        ('Posts on its own', 'No: every review waits for you'),
      ],
      drafts: const [
        ReviewDraft(
          id: 'd-1',
          repoID: 'acme/widgets',
          number: 42,
          title: 'Make the scan report a file it cannot read',
          headSHA: '9f1ad475',
          findings: [
            Finding(
              id: 'f-1',
              severity: 'blocking',
              path: 'internal/repo/scan.go',
              line: 42,
              title: 'The scan drops a file it cannot read',
              body:
                  'A permission error ends the walk instead of being reported.',
            ),
          ],
          summary: 'One blocking finding.',
          status: 'draft',
          createdAt: '2026-11-17T03:00:00Z',
        ),
      ],
    ),
    Bot(
      kind: BotKind.fixer,
      enabled: true,
      repoID: 'acme/widgets',
      summary: '1 run lately',
      facts: const [
        ('State', 'On'),
        ('Repository', 'acme/widgets'),
        ('Watches', 'main, release/*'),
        ('Sends a fix', 'As a pull request'),
      ],
      history: const [
        CIHistory(
          id: 'c-1',
          repoID: 'acme/widgets',
          branch: 'main',
          sha: '9f1ad475',
          check: 'go',
          status: 'fixed',
          reason: 'a nil dereference in the parser',
          costUSD: 0.42,
          createdAt: '2026-11-17T03:10:00Z',
        ),
      ],
    ),
  ],
  project: '/home/u/marshal',
  now: shellNow,
);

/// The Automations place, on [tab] (0 is Bots, 1 Schedules, 2 Watches,
/// 3 Recipes).
Widget _automations({StateKind? state, int tab = 1}) {
  final bots = _botsFixture();
  final schedules = _schedulesFixture();
  final watches = _watchesFixture();
  final recipes = _recipesFixture();
  return AutomationsPlace(
    bots: (tabs, state) => BotsTab(model: bots, tabs: tabs, state: state),
    schedules: (tabs, state) =>
        SchedulesTab(model: schedules, tabs: tabs, state: state),
    watches: (tabs, state) =>
        WatchesTab(model: watches, tabs: tabs, state: state),
    recipes: (tabs, state) =>
        RecipesTab(model: recipes, tabs: tabs, state: state),
    tab: tab,
    state: state,
  );
}

/// The registry of screens. F0 has one, the gallery; each later phase adds
/// its screens here with fixture data, and `tooltips_test.dart`,
/// `states_test.dart` and `overflow_test.dart` then hold them to the design
/// system without being edited.
final screens = <ScreenCase>[
  ScreenCase(
    name: 'gallery',
    ready: () => const Gallery(),
    states: {
      for (final kind in StateKind.values) kind: () => Gallery(state: kind),
    },
  ),
  ScreenCase(
    name: 'connection check',
    ready: () => ConnectionCheck(
      hosts: _checkHosts,
      now: DateTime.utc(2026, 11, 17, 4, 12),
      onRetry: () {},
    ),
    states: {
      for (final kind in StateKind.values)
        kind: () => ConnectionCheck(
          hosts: _checkHosts,
          now: DateTime.utc(2026, 11, 17, 4, 12),
          state: kind,
          onRetry: () {},
        ),
    },
  ),
  ScreenCase(
    name: 'inbox',
    ready: () => _inbox(),
    states: {
      for (final kind in requiredStates) kind: () => _inbox(state: kind),
    },
  ),
  ScreenCase(
    name: 'overview',
    ready: () => _overview(),
    states: {
      for (final kind in requiredStates) kind: () => _overview(state: kind),
    },
  ),
  ScreenCase(
    name: 'shell',
    ready: () => _shell(),
    states: {
      for (final kind in requiredStates) kind: () => _shell(state: kind),
    },
  ),
  ScreenCase(
    name: 'automations · bots',
    ready: () => _automations(tab: 0),
    states: {
      for (final kind in requiredStates)
        kind: () => _automations(tab: 0, state: kind),
    },
  ),
  ScreenCase(
    name: 'automations · schedules',
    ready: () => _automations(),
    states: {
      for (final kind in requiredStates) kind: () => _automations(state: kind),
    },
  ),
  ScreenCase(
    name: 'automations · watches',
    ready: () => _automations(tab: 2),
    states: {
      for (final kind in requiredStates)
        kind: () => _automations(tab: 2, state: kind),
    },
  ),
  ScreenCase(
    name: 'automations · recipes',
    ready: () => _automations(tab: 3),
    states: {
      for (final kind in requiredStates)
        kind: () => _automations(tab: 3, state: kind),
    },
  ),
];

/// The states every screen that shows data must handle (§7). Planned is a
/// row, not a screen state, so it is not required here.
const requiredStates = {
  StateKind.loading,
  StateKind.empty,
  StateKind.stale,
  StateKind.error,
  StateKind.unavailable,
};
