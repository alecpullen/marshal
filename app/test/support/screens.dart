import 'package:marshal_app/src/client/contract.g.dart' as contract;
import 'package:marshal_app/src/client/contract.g.dart'
    show
        Binding,
        Budgets,
        Credential,
        DaemonInfo,
        DiffFile,
        EnvDoc,
        EnvListItem,
        EnvLoaded,
        EnvMeta,
        EnvNetwork,
        EnvPackages,
        EnvPolicy,
        EnvResources,
        EnvSetup,
        EnvWorkspace,
        FileEntry,
        GateResult,
        ModelsConfig,
        NetHostRow,
        Preset,
        ProviderView,
        PullCheck,
        PullOrigin,
        PullRecord,
        PullState,
        Recipe,
        Repo,
        Schedule,
        SecretsStatus,
        StackSnapshot,
        StepDiff,
        NotifyWebhook,
        StatusLink,
        UsagePoint,
        UsageReport,
        UsageTotals,
        AuditEvent,
        DiskStatus,
        WireCall,
        WireMessage,
        WireNode,
        WireReceipt,
        WireStep,
        WireTool;
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/bots_model.dart';
import 'package:marshal_app/src/model/clients_model.dart';
import 'package:marshal_app/src/model/connections_model.dart';
import 'package:marshal_app/src/model/environment_model.dart';
import 'package:marshal_app/src/model/environments_model.dart';
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
import 'package:marshal_app/src/model/general_model.dart';
import 'package:marshal_app/src/model/hosts_model.dart';
import 'package:marshal_app/src/model/library_model.dart';
import 'package:marshal_app/src/model/models_model.dart';
import 'package:marshal_app/src/model/network_model.dart';
import 'package:marshal_app/src/model/secrets_model.dart';
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/changes_model.dart';
import 'package:marshal_app/src/model/files_model.dart';
import 'package:marshal_app/src/model/inspect_model.dart';
import 'package:marshal_app/src/model/inspector_state.dart';
import 'package:marshal_app/src/model/preview_model.dart';
import 'package:marshal_app/src/model/pulls_model.dart';
import 'package:marshal_app/src/model/terminal_session.dart';
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/overview_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/planning_model.dart';
import 'package:marshal_app/src/model/run_model.dart';
import 'package:marshal_app/src/model/recipes_model.dart';
import 'package:marshal_app/src/model/schedules_model.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/model/usage_model.dart';
import 'package:marshal_app/src/model/notifications_model.dart';
import 'package:marshal_app/src/model/access_model.dart';
import 'package:marshal_app/src/model/watches_model.dart';
import 'package:marshal_app/src/model/work_item_model.dart' as wmodel;
import 'package:marshal_app/src/ui/automations/automations_place.dart';
import 'package:marshal_app/src/ui/automations/bots_tab.dart';
import 'package:marshal_app/src/ui/automations/recipes_tab.dart';
import 'package:marshal_app/src/ui/automations/schedules_tab.dart';
import 'package:marshal_app/src/ui/automations/watches_tab.dart';
import 'package:marshal_app/src/ui/debug/connection_check.dart';
import 'package:marshal_app/src/ui/gallery/gallery.dart';
import 'package:marshal_app/src/ui/library/library_place.dart';
import 'package:marshal_app/src/ui/library/mcp_tab.dart';
import 'package:marshal_app/src/ui/library/memory_tab.dart';
import 'package:marshal_app/src/ui/library/plugins_tab.dart';
import 'package:marshal_app/src/ui/library/skills_tab.dart';
import 'package:marshal_app/src/ui/settings/connections_section.dart';
import 'package:marshal_app/src/ui/settings/environment_designer.dart';
import 'package:marshal_app/src/ui/settings/environments_section.dart';
import 'package:marshal_app/src/ui/settings/general_section.dart';
import 'package:marshal_app/src/ui/settings/hosts_section.dart';
import 'package:marshal_app/src/ui/settings/models_section.dart';
import 'package:marshal_app/src/ui/settings/network_section.dart';
import 'package:marshal_app/src/ui/settings/secrets_section.dart';
import 'package:marshal_app/src/ui/settings/usage_section.dart';
import 'package:marshal_app/src/ui/settings/notifications_section.dart';
import 'package:marshal_app/src/ui/settings/access_section.dart';
import 'package:marshal_app/src/ui/settings/settings_place.dart';
import 'package:marshal_app/src/ui/inbox/inbox.dart';
import 'package:marshal_app/src/ui/inspector/changes_tab.dart';
import 'package:marshal_app/src/ui/inspector/files_tab.dart';
import 'package:marshal_app/src/ui/inspector/inspect_tab.dart';
import 'package:marshal_app/src/ui/inspector/preview_tab.dart';
import 'package:marshal_app/src/ui/inspector/terminal_tab.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/overview/overview.dart';
import 'package:marshal_app/src/ui/project/project_page.dart';
import 'package:marshal_app/src/ui/pulls/pulls_place.dart';
import 'package:marshal_app/src/ui/shell/placeholder.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';
import 'package:marshal_app/src/ui/factory/planning_screen.dart';
import 'package:marshal_app/src/ui/factory/run_screen.dart';
import 'package:marshal_app/src/ui/factory/work_item_screen.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';
import 'package:marshal_app/src/client/bridge_client.dart'
    show ProjectSettingsVersion;
import 'package:marshal_app/src/model/project_model.dart';

import '../model/support.dart' show agent, factoryItem;

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
    NavPlace(
      kind: PlaceKind.pulls,
      label: 'Pull requests',
      // F5: the place exists, so its row opens it and carries the ready count.
      count: 1,
      active: place.kind == PlaceKind.pulls,
    ),
    for (final kind in const [
      PlaceKind.automations,
      PlaceKind.library,
      PlaceKind.settings,
    ])
      NavPlace(
        kind: kind,
        label: switch (kind) {
          PlaceKind.automations => 'Automations',
          PlaceKind.library => 'Library',
          _ => 'Settings',
        },
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
    content: (_) => plannedPlace(place, nav),
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
  pulls: [
    _pull(number: 131, title: 'Quiet capture mode', group: PullGroup.ready),
    _pull(
      number: 132,
      title: 'Fix flaky calendar test',
      group: PullGroup.failing,
      checkSummary: '1 failing: unit',
      checks: const [
        PullCheckView('build', PullCheckState.passed),
        PullCheckView('unit', PullCheckState.failed),
      ],
    ),
  ],
  state: state,
  onRetry: () {},
  onSelect: (_) {},
  onAnswer: (_, _, {reply, answers}) {},
);

Widget _overview({StateKind? state}) => Overview(
  overview: _overviewModel(),
  now: shellNow,
  hosts: shellHosts,
  pulls: [
    _pull(number: 131, title: 'Quiet capture mode', group: PullGroup.ready),
    _pull(
      number: 132,
      title: 'Fix flaky calendar test',
      group: PullGroup.failing,
      checkSummary: '1 failing: unit',
      checks: const [
        PullCheckView('build', PullCheckState.passed),
        PullCheckView('unit', PullCheckState.failed),
      ],
    ),
  ],
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
  onAnswer: (_, _, {reply, answers}) {},
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

/// The Library fixture: one skill, one plugin, one client and one memory, with
/// the memory a promotion is suggested for.
Widget _library({StateKind? state, int tab = 0}) {
  final skills = SkillsModel.fixture(const [
    SkillEntry(
      name: 'recovery',
      description: 'How this project recovers a lost session',
      risk: 'low',
      scope: 'global',
    ),
  ], scope: 'global');
  final plugins = PluginsModel.fixture(const [
    PluginEntry(
      name: 'acme-tools',
      source: 'github.com/acme/tools',
      commit: '9f1ad475',
      contentHash: 'sha256:ab',
      installedAt: '2026-11-17T03:00:00Z',
      scope: 'global',
    ),
  ], scope: 'global');
  final clients = ClientsModel.fixture(
    const [
      MCPClient(
        id: 'c-1',
        name: 'editor',
        allowedRepos: ['acme/widgets'],
        ownerID: 'owner-1',
        createdAt: '2026-11-17T03:00:00Z',
      ),
    ],
    projects: const ['/home/u/marshal'],
  );
  final memory = MemoryModel.fixture(
    const [
      MemoryEntry(
        id: 7,
        kind: 'fact',
        content: 'The gate runs on Go 1.24',
        confidence: 'confirmed',
        createdAt: '2026-11-17T03:00:00Z',
        updatedAt: '2026-11-17T03:05:00Z',
        scope: 'project',
        learnedAgent: 'a-1',
        learnedProjectRoot: '/home/u/marshal',
      ),
    ],
    project: '/home/u/marshal',
    suggestions: const [
      MemorySuggestion(
        memoryID: 7,
        matchProjectRoot: '/home/u/other',
        suggestedScope: 'global',
      ),
    ],
  );
  return LibraryPlace(
    skills: (tabs, state) => SkillsTab(model: skills, tabs: tabs, state: state),
    plugins: (tabs, state) =>
        PluginsTab(model: plugins, tabs: tabs, state: state),
    mcp: (tabs, state) => McpTab(model: clients, tabs: tabs, state: state),
    memory: (tabs, state) => MemoryTab(model: memory, tabs: tabs, state: state),
    tab: tab,
    state: state,
  );
}

/// One pull request the enforcement tests draw: a record and the view a card
/// reads, so the place and the card are pumped together.
PullView _pull({
  required int number,
  required String title,
  required PullGroup group,
  String checkSummary = '2 of 2 checks passed',
  List<PullCheckView> checks = const [
    PullCheckView('build', PullCheckState.passed),
    PullCheckView('unit', PullCheckState.passed),
  ],
  String? failureTail,
  String? report,
  bool draft = false,
  bool sending = false,
  String? error,
}) => PullView(
  record: PullRecord(
    id: 'acme/widgets#$number',
    repoID: 'acme/widgets',
    projectRoot: '/home/u/marshal',
    number: number,
    url: 'https://forge.example/acme/widgets/pull/$number',
    title: title,
    headRef: 'agent/quiet-capture',
    baseRef: 'main',
    headSHA: 'a1b2c3d4',
    draft: draft,
    additions: 412,
    deletions: 96,
    origin: PullOrigin.pullOriginAgent,
    agentID: 'a-1',
    state: switch (group) {
      PullGroup.ready => PullState.pullReady,
      PullGroup.failing => PullState.pullFailing,
      PullGroup.beingFixed => PullState.pullBeingFixed,
      PullGroup.inProgress => PullState.pullInProgress,
      PullGroup.unavailable => PullState.pullChecksUnavailable,
    },
    checks: [
      for (final check in checks)
        PullCheck(name: check.name, state: _wireStateOf(check.state)),
    ],
    reviews: '2 reviews passed',
    report: report,
    observedAt: shellNow.millisecondsSinceEpoch,
    updatedAt: shellNow.millisecondsSinceEpoch - 26 * 60 * 1000,
  ),
  thread: _shellThread('a-1', 'Quiet capture mode', model.ThreadStatus.ready),
  group: group,
  checkSummary: checkSummary,
  checks: checks,
  failureTail: failureTail,
  sending: sending,
  error: error,
);

/// The wire check state a fixture's check state stands for. The kit's
/// `CheckState` is the drawn one, so the contract's is reached through its
/// prefix.
contract.CheckState _wireStateOf(PullCheckState state) => switch (state) {
  PullCheckState.passed => contract.CheckState.checkPassed,
  PullCheckState.failed => contract.CheckState.checkFailed,
  PullCheckState.running => contract.CheckState.checkRunning,
  PullCheckState.pending => contract.CheckState.checkPending,
};

/// The pull requests the place's fixture lists: one in every group.
Map<PullGroup, List<PullView>> _pullGroups() => {
  PullGroup.ready: [
    _pull(
      number: 118,
      title: 'CI-first change review',
      group: PullGroup.ready,
      checkSummary: '2 of 2 checks passed',
    ),
  ],
  PullGroup.failing: [
    _pull(
      number: 123,
      title: 'Voice planning: quiet capture mode',
      group: PullGroup.failing,
      checkSummary: '1 failing: unit',
      checks: const [
        PullCheckView('build', PullCheckState.passed),
        PullCheckView('unit', PullCheckState.failed),
      ],
      failureTail: '--- FAIL: TestScriptedRecorderQuietMode (4.21s)',
    ),
  ],
  PullGroup.beingFixed: [
    _pull(
      number: 122,
      title: 'Fix flaky calendar test',
      group: PullGroup.beingFixed,
      checkSummary: '1 failing: test',
      checks: const [
        PullCheckView('build', PullCheckState.passed),
        PullCheckView('test', PullCheckState.failed),
      ],
    ),
  ],
  PullGroup.inProgress: [
    _pull(
      number: 124,
      title: 'P6 plan 4: daemon upgrade',
      group: PullGroup.inProgress,
      checkSummary: '1 of 3 checks passed, the rest running',
      checks: const [
        PullCheckView('build', PullCheckState.passed),
        PullCheckView('test', PullCheckState.running),
        PullCheckView('lint', PullCheckState.pending),
      ],
      draft: true,
    ),
  ],
  PullGroup.unavailable: [
    _pull(
      number: 125,
      title: 'Release config: sign the binaries',
      group: PullGroup.unavailable,
      checkSummary:
          'Checks unavailable: the credential is an SSH key, which cannot '
          'call the API',
      checks: const [],
    ),
  ],
};

/// The Pull requests place with its fixture data.
Widget _pullsPlace({StateKind? state}) => PullsPlace(
  groups: _pullGroups(),
  now: shellNow,
  state: state,
  onRetry: () {},
  actionsFor: (context, pull) => const PrActions(),
);

/// The environment designer, on its fixture template.
Widget _designer({StateKind? state}) => EnvironmentDesigner(
  model: EnvironmentModel.fixture(
    name: 'go-service',
    source: _settingsEnvSource,
    builds: const EnvironmentBuilds(
      versions: [
        EnvironmentVersion(
          n: 3,
          detail: 'marshal-ws/go-service:3',
          state: 'ok',
        ),
      ],
      poolSize: 2,
      poolIdle: 1,
      poolStarting: 0,
      coldMs: 24000,
      warmMs: 1800,
      sizeBytes: 734003200,
    ),
  ),
  state: state,
);

/// The Settings place, with the sections this build has and the rest planned.
/// [section] is the index in the design's order: 0 Hosts … 9 General.
Widget _settings({int section = 0, StateKind? state}) {
  final preferences = GeneralPreferences();
  final hosts = HostsModel.fixture([
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
  ]);
  final general = GeneralModel.fixture(
    preferences: preferences,
    keyboard: [for (final key in threadKeys) (key.key, key.description)],
    daemon: _daemonInfo,
    now: () => shellNow,
  );
  final connections = ConnectionsModel.fixture(
    repos: const [
      Repo(
        id: 'acme-widgets',
        url: 'github.com/acme/widgets',
        branch: 'main',
        credRef: 'deploy',
        ownerID: 'owner-1',
        forge: 'github',
      ),
    ],
    credentials: const [
      Credential(
        id: 'deploy',
        kind: 'ssh',
        keyPath: '/home/u/.ssh/id_ed25519',
        set: true,
      ),
    ],
    models: _settingsModels,
  );
  final models = ModelsModel.fixture(_settingsModels);
  final environments = EnvironmentsModel.fixture(
    items: const [
      EnvListItem(
        source: 'studio',
        name: 'go-service',
        published: 3,
        pool: 2,
        usage: 4,
        draftChanges: true,
      ),
    ],
    loaded: const {'go-service': _settingsEnvLoaded},
  );
  final usage = UsageModel.fixture(
    report: const UsageReport(
      range: '7d',
      totals: UsageTotals(
        costUSD: 12.4,
        promptTokens: 820000,
        completionTokens: 96000,
        agentHours: 6.5,
        pRsShipped: 3,
      ),
      series: [
        UsagePoint(key: 'go-service', costUSD: 7.25, tokens: 610000),
        UsagePoint(key: 'widgets', costUSD: 5.15, tokens: 306000),
      ],
    ),
    audit: const [
      AuditEvent(ts: '2026-11-17T03:00:00Z', event: 'prune', bytes: 1048576),
      AuditEvent(ts: '2026-11-17T02:00:00Z', event: 'spawn', agentID: 'a-1'),
    ],
    disk: const DiskStatus(
      repos: 734003200,
      work: 209715200,
      total: 943718400,
      measuredAt: '2026-11-17T04:00:00Z',
      budgetMB: 4096,
    ),
    now: () => shellNow,
  );
  final notifications = NotificationsModel.fixture(
    webhooks: const [
      NotifyWebhook(
        id: 'wh-1',
        url: 'https://hooks.example.com/marshal',
        secretRef: 'vault:notify/signing',
        events: ['needs_you', 'run_finished'],
      ),
    ],
  );
  final access = AccessModel.fixture(
    links: const [
      StatusLink(
        id: 'sl-1',
        agentID: 'a-1',
        createdAt: '2026-11-17T03:00:00Z',
        expiresAt: '2026-11-18T03:00:00Z',
      ),
    ],
    now: () => shellNow,
  );
  final secrets = SecretsModel.fixture(
    status: const SecretsStatus(backend: 'local', healthy: true),
    refs: const ['vault:forge/github-token'],
    credentials: const [
      Credential(
        id: 'deploy',
        kind: 'vault',
        ref: 'vault:forge/github-token',
        set: true,
      ),
    ],
  );
  final network = NetworkModel.fixture(
    hosts: const [
      NetHostRow(
        workspace: 'go-service',
        agentID: 'a-1',
        host: 'proxy.golang.org',
        rule: 'allowlisted',
        requests: 42,
        blocked: 0,
        bytesUp: 1024,
        bytesDown: 65536,
        lastSeen: 1763352800000,
        decision: 'allow',
      ),
      NetHostRow(
        host: 'registry.npmjs.org',
        rule: 'blocked',
        requests: 7,
        blocked: 7,
        bytesUp: 0,
        bytesDown: 0,
        lastSeen: 1763352400000,
        decision: 'block',
      ),
    ],
    environments: const ['go-service'],
    agents: const ['a-1'],
    now: () => shellNow,
  );
  return SettingsPlace(
    preferences: preferences,
    index: section,
    state: state,
    sections: {
      SettingsSectionKind.hosts: (state) =>
          HostsSection(model: hosts, state: state, now: shellNow),
      SettingsSectionKind.environments: (state) => EnvironmentsSection(
        model: environments,
        state: state,
        designer: (row) => EnvironmentDesigner(
          model: EnvironmentModel.fixture(
            name: row.name,
            source: _settingsEnvSource,
            builds: const EnvironmentBuilds(
              versions: [
                EnvironmentVersion(
                  n: 3,
                  detail: 'marshal-ws/go-service:3',
                  state: 'ok',
                ),
              ],
              poolSize: 2,
              poolIdle: 1,
              poolStarting: 0,
              coldMs: 24000,
              warmMs: 1800,
              sizeBytes: 734003200,
            ),
          ),
        ),
      ),
      SettingsSectionKind.connections: (state) =>
          ConnectionsSection(model: connections, state: state),
      SettingsSectionKind.models: (state) =>
          ModelsSection(model: models, state: state),
      SettingsSectionKind.secrets: (state) =>
          SecretsSection(model: secrets, state: state),
      SettingsSectionKind.network: (state) =>
          NetworkSection(model: network, state: state),
      SettingsSectionKind.usage: (state) =>
          UsageSection(model: usage, state: state),
      SettingsSectionKind.notifications: (state) =>
          NotificationsSection(model: notifications, state: state),
      SettingsSectionKind.access: (state) =>
          AccessSection(model: access, state: state),
      SettingsSectionKind.general: (state) =>
          GeneralSection(model: general, state: state),
    },
  );
}

/// The source the Settings fixture's designer shows: two layers and a setup
/// command, so the panel has facts to report.
const _settingsEnvSource = '''[workspace]
name = "go-service"
base = "ubuntu:24.04"
toolchains = ["go@1.24"]

[packages]
go = ["golang.org/x/tools/gopls@latest"]

[network]
mode = "allowlist"

[setup]
run = "go mod download"
''';

/// One template's parsed document, as the Environments fixture shows it.
const _settingsEnvLoaded = EnvLoaded(
  source: '[workspace]\nname = "go-service"\nbase = "ubuntu:24.04"\n',
  version: 0,
  doc: EnvDoc(
    workspace: EnvWorkspace(
      name: 'go-service',
      base: 'ubuntu:24.04',
      toolchains: ['go@1.24'],
    ),
    packages: EnvPackages(),
    network: EnvNetwork(mode: 'allowlist'),
    resources: EnvResources(),
    policy: EnvPolicy(),
    setup: EnvSetup(),
  ),
  sections: [],
  diagnostics: [],
  meta: EnvMeta(
    name: 'go-service',
    ownerID: 'owner-1',
    createdAt: '2026-11-16T09:00:00Z',
    published: 3,
    pool: 2,
    versions: [],
  ),
);

/// The global models config the Settings fixtures show.
const _settingsModels = ModelsConfig(
  providers: {
    'local': ProviderView(
      type: 'ollama',
      baseURL: 'http://127.0.0.1:11434',
      toolCalling: true,
      hasKey: false,
      keySource: 'none',
    ),
  },
  presets: {
    'local/qwen': Preset(
      provider: 'local',
      model: 'qwen3-coder',
      contextWindow: 131072,
    ),
  },
  profiles: {
    'default': {'implementer': Binding(preset: 'local/qwen')},
  },
  customAgents: [],
  defaultProfile: 'default',
  activePreset: 'local/qwen',
  roles: ['implementer', 'reviewer'],
  budgets: Budgets(
    dailyUSD: 5,
    perAgentUSD: 2,
    onDailyCap: 'warn',
    onAgentCap: 'block',
  ),
);

/// The daemon document General's fixture reports.
final _daemonInfo = DaemonInfo(
  version: '1.4.0',
  profile: 'local',
  pid: 4821,
  startedAt: shellNow
      .subtract(const Duration(hours: 3, minutes: 12))
      .millisecondsSinceEpoch,
  socket: '/home/u/.local/state/marshal/daemon.sock',
  projects: const ['/home/u/marshal'],
  pinned: const ['/home/u/widgets'],
  children: const [],
);

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
    name: 'pull requests',
    ready: () => _pullsPlace(),
    states: {
      for (final kind in requiredStates) kind: () => _pullsPlace(state: kind),
    },
  ),
  ScreenCase(
    name: 'work item',
    ready: () => _workItemScreen(),
    states: {
      for (final kind in requiredStates)
        kind: () => _workItemScreen(state: kind),
    },
  ),
  ScreenCase(
    name: 'planning',
    ready: () => _planningScreen(),
    states: {
      for (final kind in requiredStates)
        kind: () => _planningScreen(state: kind),
    },
  ),
  ScreenCase(
    name: 'plan run',
    ready: () => _runScreen(),
    states: {
      for (final kind in requiredStates) kind: () => _runScreen(state: kind),
    },
  ),
  ScreenCase(
    name: 'project · work',
    ready: () => _projectScreen(),
    states: {
      for (final kind in requiredStates)
        kind: () => _projectScreen(state: kind),
    },
  ),
  ScreenCase(
    name: 'project · automations',
    ready: () => _projectScreen(tab: 1),
    states: {
      for (final kind in requiredStates)
        kind: () => _projectScreen(tab: 1, state: kind),
    },
  ),
  ScreenCase(
    name: 'project · defaults',
    ready: () => _projectScreen(tab: 2),
    states: {
      for (final kind in requiredStates)
        kind: () => _projectScreen(tab: 2, state: kind),
    },
  ),
  ScreenCase(
    name: 'project · health',
    ready: () => _projectScreen(tab: 3),
    states: {
      for (final kind in requiredStates)
        kind: () => _projectScreen(tab: 3, state: kind),
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
    name: 'library · skills',
    ready: () => _library(tab: 0),
    states: {
      for (final kind in requiredStates)
        kind: () => _library(tab: 0, state: kind),
    },
  ),
  ScreenCase(
    name: 'library · plugins',
    ready: () => _library(tab: 1),
    states: {
      for (final kind in requiredStates)
        kind: () => _library(tab: 1, state: kind),
    },
  ),
  ScreenCase(
    name: 'library · mcp',
    ready: () => _library(tab: 2),
    states: {
      for (final kind in requiredStates)
        kind: () => _library(tab: 2, state: kind),
    },
  ),
  ScreenCase(
    name: 'library · memory',
    ready: () => _library(tab: 3),
    states: {
      for (final kind in requiredStates)
        kind: () => _library(tab: 3, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · hosts',
    ready: () => _settings(section: 0),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 0, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · environment designer',
    ready: () => _designer(),
    states: {
      for (final kind in requiredStates) kind: () => _designer(state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · environments',
    ready: () => _settings(section: 1),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 1, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · connections',
    ready: () => _settings(section: 2),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 2, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · models',
    ready: () => _settings(section: 3),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 3, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · secrets',
    ready: () => _settings(section: 4),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 4, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · network',
    ready: () => _settings(section: 5),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 5, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · usage',
    ready: () => _settings(section: 6),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 6, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · notifications',
    ready: () => _settings(section: 7),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 7, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · access',
    ready: () => _settings(section: 8),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 8, state: kind),
    },
  ),
  ScreenCase(
    name: 'settings · general',
    ready: () => _settings(section: 9),
    states: {
      for (final kind in requiredStates)
        kind: () => _settings(section: 9, state: kind),
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
  ScreenCase(
    name: 'thread',
    ready: () => _threadScreen(),
    states: {
      for (final kind in requiredStates) kind: () => _threadScreen(state: kind),
    },
  ),
  ScreenCase(
    name: 'thread with inspector',
    ready: () => _threadScreen(inspector: _inspectorState()),
    states: {
      for (final kind in requiredStates)
        kind: () => _threadScreen(state: kind, inspector: _inspectorState()),
    },
  ),
  ScreenCase(
    name: 'thread with a review head',
    ready: () => _threadScreen(pull: _threadPull()),
    states: {
      for (final kind in requiredStates)
        kind: () => _threadScreen(state: kind, pull: _threadPull()),
    },
  ),
  ScreenCase(
    name: 'inspector preview',
    ready: () =>
        PreviewTab(model: PreviewModel.seeded(ports: const [5173, 8080])),
    states: {
      for (final kind in requiredStates)
        kind: () => PreviewTab(
          model: PreviewModel.seeded(ports: const [5173, 8080]),
          state: kind,
        ),
    },
  ),
  ScreenCase(
    name: 'inspector inspect',
    ready: () => InspectTab(model: _inspectModel()),
    states: {
      for (final kind in requiredStates)
        kind: () => InspectTab(model: _inspectModel(), state: kind),
    },
  ),
  ScreenCase(
    name: 'inspector terminal',
    ready: () => TerminalTab(session: TerminalSession.seeded()),
    states: {
      for (final kind in requiredStates)
        kind: () => TerminalTab(session: TerminalSession.seeded(), state: kind),
    },
  ),
  ScreenCase(
    name: 'inspector files',
    ready: () => FilesTab(model: _filesModel()),
    states: {
      for (final kind in requiredStates)
        kind: () => FilesTab(model: _filesModel(), state: kind),
    },
  ),
  ScreenCase(
    name: 'inspector changes',
    ready: () => ChangesTab(model: _changesModel()),
    states: {
      for (final kind in requiredStates)
        kind: () => ChangesTab(model: _changesModel(), state: kind),
    },
  ),
];

/// The inspector the screen cases show, with each tab's frame placeholder
/// until the task that builds that tab supplies its body.
InspectorState _inspectorState() => InspectorState();

/// The node the inspector's Inspect tab shows.
InspectModel _inspectModel() => InspectModel.seeded(
  facts: const InspectFacts(
    title: 'edit  internal/viewmodel/testdata/index.json',
    kind: 'tool',
    actor: 'agent',
    why: 'Drop three index entries whose files no test loads',
    took: '300 ms',
    sandbox: 'restricted · network isolated',
    approval: 'auto',
    callId: 'call_8f21c0',
    output: 'Applied 1 edit. 9 lines removed.',
    arguments: '{\n  "path": "internal/viewmodel/testdata/index.json"\n}',
    thinking: ['The fold test loads stack_fold.json.'],
  ),
);

/// The workspace tree the inspector's Files tab shows.
FilesModel _filesModel() => FilesModel.seeded(
  changed: const {
    'internal/viewmodel/testdata/index.json',
    'internal/repo/symbols_test.go',
  },
  root: '/home/u/marshal',
  tree: const {
    '': [
      FileEntry(name: 'internal', dir: true, size: 0),
      FileEntry(name: 'scripts', dir: true, size: 0),
      FileEntry(name: 'AGENTS.md', dir: false, size: 900),
      FileEntry(name: 'go.mod', dir: false, size: 412),
    ],
    'internal': [
      FileEntry(name: 'repo', dir: true, size: 0),
      FileEntry(name: 'viewmodel', dir: true, size: 0),
    ],
    'internal/repo': [
      FileEntry(name: 'symbols.go', dir: false, size: 300),
      FileEntry(name: 'symbols_test.go', dir: false, size: 220),
    ],
    'internal/viewmodel': [FileEntry(name: 'detail.go', dir: false, size: 400)],
  },
  files: const {
    'internal/repo/symbols.go': 'package repo\n\nfunc Symbols() {}\n',
    'AGENTS.md': '# Marshal\n\nGuidance for agents.\n',
  },
);

/// The changes the inspector's Changes tab shows: a failed gate, two files,
/// one of them new, and the steps that changed them.
ChangesModel _changesModel() => ChangesModel.seeded(
  gate: const GateResult(
    ok: false,
    skipped: false,
    failedCommand: 'go test ./...',
    output: 'FAIL marshal/internal/app',
  ),
  gateRanAt: shellNow.subtract(const Duration(minutes: 2)),
  files: const [
    DiffFile(
      path: 'internal/viewmodel/testdata/index.json',
      added: 0,
      removed: 9,
    ),
    DiffFile(path: 'internal/repo/symbols_test.go', added: 3, removed: 11),
    DiffFile(path: 'internal/repo/fixtures.go', added: 7, removed: 0),
  ],
  diff:
      'diff --git a/internal/viewmodel/testdata/index.json '
      'b/internal/viewmodel/testdata/index.json\n'
      '--- a/internal/viewmodel/testdata/index.json\n'
      '+++ b/internal/viewmodel/testdata/index.json\n'
      '@@ -4,4 +4,1 @@\n'
      '     "fixtures": [\n'
      '-     "turn_legacy_v1.json",\n'
      '-     "turn_legacy_v2.json",\n'
      '-     "stack_old_fold.json",\n'
      '     "stack_fold.json"\n',
  steps: const [
    StepDiff(
      stepNode: 'step:1',
      turnNode: 'turn:1',
      headline: 'Removed the fixtures that are plainly dead',
      files: ['internal/viewmodel/testdata/index.json'],
      diff:
          '@@ -4,4 +4,1 @@\n     "fixtures": [\n'
          '-     "turn_legacy_v1.json",\n     "stack_fold.json"\n',
    ),
  ],
);

/// The states every screen that shows data must handle (§7). Planned is a
/// row, not a screen state, so it is not required here.
const requiredStates = {
  StateKind.loading,
  StateKind.empty,
  StateKind.stale,
  StateKind.error,
  StateKind.unavailable,
};

/// The thread the screen cases show.
model.ThreadRef get _threadRef => model.ThreadRef(
  host: 'local',
  id: 'a-1',
  project: '/home/u/marshal',
  title: 'Clean up the test fixtures',
  kind: model.ThreadKind.agent,
  status: model.ThreadStatus.running,
  headline: 'Removing the fixtures nobody reads',
  lastActive: DateTime.utc(2026, 11, 17, 4, 10),
);

/// One turn as the agent projects it: a prompt, a step with its call, and the
/// receipt that closes it.
StackSnapshot _threadSnapshot() => StackSnapshot(
  sessionID: 's-1',
  rev: 3,
  roots: const ['turn:1'],
  nodes: [
    const WireNode(
      id: 'turn:1',
      kind: 'turn',
      children: ['msg:1', 'step:1', 'receipt:1'],
    ),
    const WireNode(
      id: 'msg:1',
      kind: 'message',
      parent: 'turn:1',
      message: WireMessage(
        role: 'user',
        content: 'Clean up the test fixtures nobody uses any more.',
      ),
    ),
    const WireNode(
      id: 'step:1',
      kind: 'step',
      parent: 'turn:1',
      live: true,
      children: ['tool:1'],
      step: WireStep(
        headline: 'Mapped the fixtures and who reads them',
        startedAt: 1000,
        endedAt: 42000,
      ),
    ),
    const WireNode(
      id: 'tool:1',
      kind: 'tool',
      parent: 'step:1',
      tool: WireTool(
        name: 'file.read',
        display: 'Read file',
        verb: 'read',
        calls: [
          WireCall(
            callID: 'c1',
            target: 'internal/acp/stack.go',
            summaryLine: '148 lines',
          ),
        ],
      ),
    ),
    const WireNode(
      id: 'receipt:1',
      kind: 'receipt',
      parent: 'turn:1',
      receipt: WireReceipt(
        durationMs: 95000,
        tasks: 2,
        steps: 4,
        tools: 3,
        files: 2,
      ),
    ),
  ],
);

/// The pull request the review-head case draws: a ready one from the thread
/// the case shows.
PullView _threadPull() => _pull(
  number: 131,
  title: 'Quiet capture mode',
  group: PullGroup.ready,
  checkSummary: '2 of 2 checks passed',
  report: 'The clock crossed midnight in the test, so it is pinned now.',
);

Widget _threadScreen({
  StateKind? state,
  InspectorState? inspector,
  PullView? pull,
}) => ThreadScreen(
  thread: _threadRef,
  store: StackStore.seeded(_threadSnapshot()),
  view: ThreadViewState(),
  inspector: inspector,
  pull: pull,
  state: state,
);

/// One work item as the enforcement tests pump it: a decision pending, one
/// worker, a round with a passing check and a review finding, and its history.
contract.FactoryItem _workItem() => contract.FactoryItem.fromJson({
  'id': 'item-8',
  'title': 'Work item thread',
  'state': 'needs_input',
  'stage': 'implement',
  'repoId': 'repo-1',
  'projectRoot': '/home/u/alpha',
  'hostId': 'local',
  'branch': 'factory/item-8',
  'baseSha': 'base-1',
  'version': 4,
  'draft': {
    'requestId': 'req-1',
    'title': 'Work item thread',
    'plan': '## Task 1\nRead the item.\n## Task 2\nApprove it.\n',
    'spec': 'Ship the work item thread.',
    'config': {
      'repoId': 'repo-1',
      'projectRoot': '/home/u/alpha',
      'hostId': 'local',
      'targetBranch': 'main',
      'workspace': '',
      'native': true,
      'implementerRouting': null,
      'reviewerRouting': null,
      'reviewerInstructions': '',
      'implementerAuto': true,
      'expectedChecks': <Object?>[],
      'noRequiredCiAcknowledged': true,
      'maxRounds': 3,
      'ciPollSeconds': 30,
      'ciWaitSeconds': 900,
    },
  },
  'approvals': <Object?>[],
  'grants': <Object?>[],
  'rounds': <Object?>[
    {
      'number': 1,
      'approvalRevision': 1,
      'implementEffectId': 'effect-implementer',
      'baseSha': 'base-1',
      'headSha': 'head-1',
      'treeSha': 'tree-1',
      'ci': {
        'headSha': 'head-1',
        'checks': <Object?>[
          {
            'key': {'kind': 'check_run', 'name': 'test', 'appId': 7},
            'id': 'check-test',
            'sha': 'head-1',
            'status': 'completed',
            'conclusion': 'success',
            'url': '',
            'logTail': '',
            'observedAt': '2026-11-17T04:00:00Z',
          },
        ],
        'complete': true,
        'observedAt': '2026-11-17T04:00:00Z',
      },
      'implementation': null,
      'review': {
        'operationId': 'effect-reviewer',
        'outcome': 'approved',
        'summary': 'One finding.',
        'question': '',
        'reason': '',
        'baseSha': 'base-1',
        'headSha': 'head-1',
        'inputsAccessible': true,
        'findings': <Object?>[
          {
            'severity': 'minor',
            'path': 'app/lib/src/ui/factory/work_item_screen.dart',
            'line': 42,
            'title': 'Name the stage in words',
          },
        ],
      },
      'startedAt': '2026-11-17T03:50:00Z',
      'finishedAt': '2026-11-17T04:00:00Z',
    },
  ],
  'workers': <Object?>[
    {
      'agentId': 'agent-imp',
      'sessionId': 'sess-imp',
      'role': 'implementer',
      'spawnEffectId': 'effect-implementer',
      'parked': false,
      'subagents': {'enabled': false, 'concurrency': 0},
    },
  ],
  'prs': <Object?>[],
  'effects': <Object?>[],
  'decision': {
    'id': 'decision-1',
    'kind': 'scope',
    'question': 'Which inspection does the inspector show?',
    'reason': 'the plan names both',
    'effectId': 'effect-implementer',
    'createdAt': '2026-11-17T04:05:00Z',
  },
  'history': <Object?>[
    {
      'sequence': 1,
      'at': '2026-11-17T03:40:00Z',
      'kind': 'created',
      'message': 'the draft was created',
      'effectId': '',
    },
    {
      'sequence': 2,
      'at': '2026-11-17T03:50:00Z',
      'kind': 'implement_started',
      'message': 'the implementer started',
      'effectId': 'effect-implementer',
    },
  ],
  'createdAt': '2026-11-17T03:40:00Z',
  'approvedAt': '2026-11-17T03:45:00Z',
  'updatedAt': '2026-11-17T04:05:00Z',
});

/// The work item's thread, or one of its §7 states.
Widget _workItemScreen({StateKind? state}) => WorkItemScreen(
  item: wmodel.WorkItemView.of('local', _workItem()),
  state: state,
  now: shellNow,
);

/// A planner whose routes answer from the fixture: one journaled turn, one
/// question waiting on a person, and one frozen snapshot.
PlanningModel _planningModel() => PlanningModel(
  plannerID: 'p-1',
  events: (after) async => [
    contract.PlannerEvent(
      sequence: 1,
      kind: 'planner.started',
      data: const {'mode': 'bypassPermissions', 'workspaceId': 'w-1'},
    ),
    contract.PlannerEvent(
      sequence: 2,
      kind: 'turn.text',
      data: const {'text': 'I read the intake path. The plan has two tasks.'},
    ),
    contract.PlannerEvent(
      sequence: 3,
      kind: 'turn.question',
      data: const {
        'requestId': 'req-1',
        'method': 'session/request_question',
        'params': {
          'questions': [
            {'question': 'Should the inspector dock or open as a sheet?'},
          ],
        },
      },
    ),
  ],
  promptPlanner: (request) async => contract.PlannerTurnReply(
    requestID: request.requestID,
    state: 'dispatched',
  ),
  cancelPlanner: () async {},
  answerPlanner: (requestID, response) async {},
  capture: (request) async => contract.PlannerSnapshotReply(
    id: 'snap-1',
    sha256: '9f2c1a55',
    baseCommit: 'base-1',
    artifacts: [
      contract.PlannerSnapshotArtifact(
        kind: 'plan',
        content: '## Task 1\nRead the item.\n## Task 2\nApprove it.\n',
        sha256: 'a1',
      ),
      contract.PlannerSnapshotArtifact(
        kind: 'spec',
        content: 'Ship the work item thread.',
        sha256: 'a2',
      ),
    ],
    files: const [],
    createdAt: '2026-11-17T04:00:00Z',
    rejected: const [],
  ),
);

/// A plan run: two tasks, the first passed with a repair round, the second
/// running, and a question waiting on a person.
RunModel _runModel() => RunModel(
  agentID: 'a-1',
  run: contract.SDDRun.fromJson({
    'active': true,
    'planName': 'F6 plan 2',
    'branch': 'sdd/run-1',
    'totalTasks': 2,
    'doneTasks': 1,
    'currentTask': 2,
    'phase': 'verify',
    'detail': 'go test ./... is running',
    'fixRound': 0,
    'maxFixRounds': 3,
    'tokensUsed': 12000,
    'tokensMax': 200000,
    'finished': false,
    'succeeded': false,
    'startedAt': 1763351900000,
    'phaseStartedAt': 1763352000000,
    'tasks': [
      {
        'n': 1,
        'title': 'Read the item',
        'dependsOn': <int>[],
        'status': 'done',
        'startedAt': 1763351900000,
        'endedAt': 1763351990000,
        'fixRounds': 1,
        'commit': {'base': 'base-1', 'head': 'head-1'},
        'stages': [
          {'name': 'implement', 'state': 'done'},
          {'name': 'verify', 'state': 'done', 'detail': 'go test ./... passed'},
          {'name': 'review', 'state': 'done'},
          {'name': 'commit', 'state': 'done'},
        ],
      },
      {
        'n': 2,
        'title': 'Approve it',
        'dependsOn': [1],
        'status': 'active',
        'startedAt': 1763352000000,
        'fixRounds': 0,
        'stages': [
          {'name': 'implement', 'state': 'active'},
        ],
      },
    ],
    'gate': {
      'taskN': 2,
      'question': 'Should the fixtures be regenerated in the same commit?',
    },
  }),
  answer: (answer) async {},
);

/// The plan run, or one of its §7 states.
Widget _runScreen({StateKind? state}) =>
    RunScreen(model: _runModel(), state: state, now: shellNow);

/// The planning thread, or one of its §7 states.
Widget _planningScreen({StateKind? state}) => PlanningScreen(
  plan: _planningModel(),
  title: 'Planning',
  subtitle: 'Permissive · planning in w-1',
  state: state,
  now: shellNow,
  onHandToFactory: () {},
);

/// One project's page state as the enforcement tests pump it: settings with an
/// inherited factory role, a health report, one plan run and a discovery report
/// the person can ask for.
ProjectModel _projectModel() => ProjectModel(
  root: '/home/u/alpha',
  name: 'alpha',
  now: () => shellNow,
  read: () async => ProjectSettingsVersion(
    settings: contract.ProjectSettings.fromJson({
      'routing': {'profile': 'balanced'},
      'mode': 'act',
      'auto': true,
      'shipTarget': 'merge',
      'intake': {
        'repoId': 'repo-1',
        'labels': ['studio'],
      },
      'factoryDefaults': {
        'targetBranch': 'main',
        'maxRounds': 3,
        'ciPollSeconds': 30,
        'ciWaitSeconds': 900,
        'reviewerInstructions': 'Look for missing tests.',
      },
      'automations': {
        'reviewBot': {'enabled': true, 'repoId': 'repo-1', 'autoPost': false},
        'ciFixer': {
          'enabled': false,
          'repoId': 'repo-1',
          'branches': ['main'],
        },
      },
    }),
    etag: 'v1',
  ),
  write: (next, etag) async =>
      ProjectSettingsVersion(settings: next, etag: 'v2'),
  readHealth: () async => contract.ProjectHealth.fromJson({
    'gateRunnable': 'yes',
    'mirrorFresh': [
      {
        'repoId': 'repo-1',
        'present': true,
        'ageSeconds': 480,
        'head': 'abc1234',
      },
    ],
    'orphanWorktrees': <Object?>[],
    'trust': 'trusted',
    'workspaceResolves': {'ref': 'repo:alpha', 'resolves': true, 'built': true},
    'verify': {'build': 'go build ./...', 'test': 'go test ./...'},
  }),
  readRuns: () async => [
    contract.RunListEntry.fromJson({
      'agentId': 'a-run',
      'name': 'Plan run',
      'project': '/home/u/alpha',
      'run': {
        'active': true,
        'planName': 'F6 plan 2',
        'branch': 'sdd/run-1',
        'totalTasks': 3,
        'doneTasks': 1,
        'fixRound': 0,
        'maxFixRounds': 3,
        'tokensUsed': 0,
        'tokensMax': 200000,
        'finished': false,
        'succeeded': false,
        'tasks': <Object?>[],
      },
      'at': 1763352000000,
    }),
  ],
  discover: (repoID) async => contract.DiscoveryReport.fromJson({
    'repoId': repoID,
    'forge': 'github',
    'status': 'ok',
    'branch': 'main',
    'revision': 'abc1234f',
    'capabilities': [
      {
        'key': 'forge_metadata',
        'state': 'supported',
        'detail': 'The forge answered',
      },
    ],
    'facts': [
      {
        'origin': 'detected',
        'section': 'checkout_instructions',
        'kind': 'file',
        'value': 'present',
        'source': 'checkout:AGENTS.md',
      },
    ],
    'recommended': <Object?>[],
    'gaps': <Object?>[],
  }),
);

/// The project's page, or one of its §7 states, on one tab.
Widget _projectScreen({int tab = 0, StateKind? state}) => ProjectPage(
  model: _projectModel(),
  items: [
    for (final item in [
      factoryItem(id: 'item-1', title: 'Draft the intake', state: 'queued'),
      factoryItem(
        id: 'item-2',
        title: 'Reliable session recovery',
        state: 'implementing',
      ),
      factoryItem(
        id: 'item-3',
        title: 'Review the inspector',
        state: 'reviewing',
      ),
      factoryItem(id: 'item-4', title: 'Ready to land', state: 'ready'),
    ])
      wmodel.WorkItemView.of('local', item),
  ],
  agents: [
    model.threadFor(
      'local',
      agent(
        id: 'a-1',
        name: 'Recovery',
        project: '/home/u/alpha',
        status: contract.AgentState.agentRunning,
        activity: 'Reading internal/acp',
      ),
    ),
  ],
  state: state,
  now: shellNow,
  initialTab: tab,
);
