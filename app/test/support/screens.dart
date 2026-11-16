import 'package:marshal_app/src/client/contract.g.dart' show Recipe, Schedule;
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/overview_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/recipes_model.dart';
import 'package:marshal_app/src/model/schedules_model.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/ui/automations/automations_place.dart';
import 'package:marshal_app/src/ui/automations/placeholder.dart';
import 'package:marshal_app/src/ui/automations/recipes_tab.dart';
import 'package:marshal_app/src/ui/automations/schedules_tab.dart';
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

/// The Automations place, on [tab] (1 is Schedules, 3 Recipes). Bots and
/// Watches are planned until F7 plan 1's later steps land.
Widget _automations({StateKind? state, int tab = 1}) {
  final schedules = _schedulesFixture();
  final recipes = _recipesFixture();
  return AutomationsPlace(
    bots: plannedAutomationsTab(
      'Bots',
      'The review bot and the CI fixer arrive with a later step of this phase.',
    ),
    schedules: (tabs, state) =>
        SchedulesTab(model: schedules, tabs: tabs, state: state),
    watches: plannedAutomationsTab(
      'Watches',
      'Watches arrive with a later step of this phase.',
    ),
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
    name: 'automations · schedules',
    ready: () => _automations(),
    states: {
      for (final kind in requiredStates) kind: () => _automations(state: kind),
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
