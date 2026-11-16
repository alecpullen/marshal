import 'package:marshal_app/src/client/contract.g.dart'
    show
        DiffFile,
        FileEntry,
        GateResult,
        StackSnapshot,
        StepDiff,
        WireCall,
        WireMessage,
        WireNode,
        WireReceipt,
        WireStep,
        WireTool;
import 'package:marshal_app/src/client/host.dart' show HostKind, HostReach;
import 'package:marshal_app/src/model/fleet_store.dart' show HostView;
import 'package:marshal_app/src/model/inbox_model.dart' as model;
import 'package:marshal_app/src/model/changes_model.dart';
import 'package:marshal_app/src/model/files_model.dart';
import 'package:marshal_app/src/model/terminal_session.dart';
import 'package:marshal_app/src/model/inspector_state.dart';
import 'package:marshal_app/src/model/overview_model.dart';
import 'package:marshal_app/src/model/nav_model.dart';
import 'package:marshal_app/src/model/place.dart';
import 'package:marshal_app/src/model/stack_store.dart';
import 'package:marshal_app/src/model/thread_ref.dart' as model;
import 'package:marshal_app/src/model/thread_view_state.dart';
import 'package:marshal_app/src/ui/debug/connection_check.dart';
import 'package:marshal_app/src/ui/gallery/gallery.dart';
import 'package:marshal_app/src/ui/inbox/inbox.dart';
import 'package:marshal_app/src/ui/inspector/changes_tab.dart';
import 'package:marshal_app/src/ui/inspector/files_tab.dart';
import 'package:marshal_app/src/ui/inspector/terminal_tab.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/overview/overview.dart';
import 'package:marshal_app/src/ui/shell/placeholder.dart';
import 'package:marshal_app/src/ui/shell/shell.dart';
import 'package:marshal_app/src/ui/thread/thread_screen.dart';

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
  onAnswer: (_, _, {reply, answers}) {},
);

Widget _overview({StateKind? state}) => Overview(
  overview: _overviewModel(),
  now: shellNow,
  hosts: shellHosts,
  state: state,
  onRetry: () {},
  onSelect: (_) {},
  onAnswer: (_, _, {reply, answers}) {},
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
    name: 'shell',
    ready: () => _shell(),
    states: {
      for (final kind in requiredStates) kind: () => _shell(state: kind),
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

Widget _threadScreen({StateKind? state, InspectorState? inspector}) =>
    ThreadScreen(
      thread: _threadRef,
      store: StackStore.seeded(_threadSnapshot()),
      view: ThreadViewState(),
      inspector: inspector,
      state: state,
    );
