/// The kit gallery: one screen showing every kit component in every variant
/// and state (design-system.md §10, f0-foundations.md decision 2).
///
/// It is reachable only from the tests and from the debug entry
/// `lib/gallery_main.dart`; the shipped app never shows it. It is a screen
/// like any other, so it imports the kit and nothing else, and it is the
/// first entry in the registry of screens the enforcement tests pump.
library;

import '../../model/section_model.dart';
import '../kit/kit.dart';
import '../sections/section_page.dart';

/// The gallery. With [state] it draws that §7 state instead of its content,
/// as every screen that shows data must be able to.
class Gallery extends StatefulWidget {
  const Gallery({super.key, this.state, this.onToggleTheme});

  /// The data state to show; null is the ready state, with everything.
  final StateKind? state;

  /// Switches between light and dark, when the host offers it.
  final VoidCallback? onToggleTheme;

  @override
  State<Gallery> createState() => _GalleryState();
}

class _GalleryState extends State<Gallery> {
  int _tab = 0;
  int _filter = 0;
  bool _boxOpened = false;

  /// The composer specimen's text, which the gallery never edits.
  late final TextEditingController _composer = TextEditingController();

  static void _noop() {}

  @override
  void dispose() {
    _composer.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final e = Ember.of(context);
    final toggle = widget.onToggleTheme;
    final actions = <Widget>[
      Btn.primary(
        'Open the sheet',
        icon: Glyphs.rectangle_on_rectangle,
        onTap: () => Sheet.show<void>(context, _sheet(context)),
      ),
      if (toggle != null)
        Btn(e.isDark ? 'Show light' : 'Show dark', onTap: toggle),
    ];

    final state = widget.state;
    if (state != null && state != StateKind.stale) {
      return PageBody(
        title: 'Kit gallery',
        subtitle: 'Every component in every variant and state',
        actions: actions,
        children: [_stateOnly(state)],
      );
    }

    return PageBody(
      title: 'Kit gallery',
      subtitle: 'Every component in every variant and state',
      actions: actions,
      children: [
        if (state == StateKind.stale) ...[
          const StateMessage.stale(
            age: '4m',
            detail: 'Reconnecting to studio-box',
          ),
          const SizedBox(height: Space.s24),
        ],
        ..._section('Type', _type(e)),
        ..._section('Box', _boxes()),
        ..._section('Hairline', _hairlines()),
        ..._section('Btn', _buttons()),
        ..._section('IconBtn', _iconButtons()),
        ..._section('Pill', _pills()),
        ..._section('Dot', _dots(e)),
        ..._section('Label', _labels()),
        ..._section('Tabs', _tabs()),
        ..._section('Segments', _segments()),
        ..._section('Meter', _meters()),
        ..._section('Spark', _spark()),
        ..._section('CheckStrip', _strips()),
        ..._section('DiffView', _diffViews()),
        ..._section('GateList', _gateLists()),
        ..._section('Rows', _rows()),
        ..._section('NavRow', _navRows()),
        ..._section('AskCard', _asks()),
        ..._section('PrCard', _pulls()),
        ..._section('ThreadRow', _threads()),
        ..._section('TurnPrompt', _turnPrompts()),
        ..._section('StepHeader', _stepHeaders()),
        ..._section('ToolRow', _toolRows()),
        ..._section('Narration', _narrations()),
        ..._section('OutputBlock', _outputBlocks()),
        ..._section('LiveRow', _liveRows()),
        ..._section('Composer', _composers()),
        ..._section('StateMessage', _states()),
        ..._section('Field', _fields()),
        ..._section('Menu', _menus()),
        ..._section('Sheet', [Box(child: _sheet(context))]),
        ..._section('Tip', [_tip(e)]),
        ..._section('SectionPage', [_sectionPage()]),
      ],
    );
  }

  /// One section: its label, then its specimens a gap apart.
  List<Widget> _section(String name, List<Widget> specimens) => [
    Label(name),
    const SizedBox(height: Space.s12),
    for (final (i, specimen) in specimens.indexed) ...[
      if (i > 0) const SizedBox(height: Space.s12),
      specimen,
    ],
    const SizedBox(height: Space.s32),
  ];

  /// Specimens side by side, wrapping on a narrow window.
  Widget _row(List<Widget> children) => Wrap(
    spacing: Space.s12,
    runSpacing: Space.s12,
    crossAxisAlignment: WrapCrossAlignment.center,
    children: children,
  );

  Widget _stateOnly(StateKind state) => switch (state) {
    StateKind.loading => const StateMessage.loading(lines: 4),
    StateKind.empty => StateMessage.empty(
      message: 'Components you add to the kit appear here.',
      action: StateAction('Read the design system', onTap: () {}),
    ),
    StateKind.error => StateMessage.error(
      message: 'The gallery could not be drawn.',
      retry: StateAction('Try again', onTap: () {}),
    ),
    StateKind.unavailable => StateMessage.unavailable(
      host: 'studio-box',
      lastSeen: '4m',
      action: StateAction('Try again', onTap: () {}),
    ),
    StateKind.planned => const StateMessage.planned(
      name: 'Gallery search',
      note: 'Finding a component by name is planned.',
    ),
    StateKind.stale => const SizedBox.shrink(),
  };

  List<Widget> _type(Ember e) => [
    for (final MapEntry(key: name, value: style) in e.roles.entries)
      Row(
        crossAxisAlignment: CrossAxisAlignment.baseline,
        textBaseline: TextBaseline.alphabetic,
        children: [
          Flexible(
            child: Text(
              name == 'label'
                  ? 'SECTION LABEL'
                  : 'Should recovery survive a daemon restart?',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: style,
            ),
          ),
          const SizedBox(width: Space.s12),
          Text(name, style: e.monoSmall.tint(e.muted)),
        ],
      ),
  ];

  List<Widget> _boxes() => [
    const Box(child: Text('A plain group: the border is border')),
    const Box(
      tone: BoxTone.attention,
      child: Text('Something that needs you: the border is attention'),
    ),
    const Box(
      tone: BoxTone.failing,
      child: Text('Something that failed: the border is failing'),
    ),
    Box(
      onTap: () => setState(() => _boxOpened = !_boxOpened),
      child: Text(
        _boxOpened
            ? 'Opened. Clickable boxes fill on hover'
            : 'A clickable box: it fills on hover',
      ),
    ),
    const Box.flush(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: EdgeInsets.all(Space.s16),
            child: Text('Box.flush: rows pad themselves'),
          ),
          Hairline(),
          Padding(
            padding: EdgeInsets.all(Space.s16),
            child: Text('and a hairline separates them'),
          ),
        ],
      ),
    ),
  ];

  List<Widget> _hairlines() => [
    const Hairline(),
    const SizedBox(
      height: Space.s32,
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text('Left'),
          SizedBox(width: Space.s12),
          Hairline.vertical(),
          SizedBox(width: Space.s12),
          Text('Right'),
        ],
      ),
    ),
  ];

  // One group of actions per row: at most one primary in a group, first.
  List<Widget> _buttons() => [
    _row([
      Btn.primary('Review', onTap: () {}),
      Btn('Merge', onTap: () {}),
      Btn('Open on forge', icon: Glyphs.arrow_up_right, onTap: () {}),
    ]),
    _row([
      Btn.primary('Fix with a worker', icon: Glyphs.wrench, onTap: () {}),
      Btn('Full log', onTap: () {}),
    ]),
    _row(const [
      Btn.primary('Start agent'),
      Btn('Discard'),
      Btn('Open on forge', icon: Glyphs.arrow_up_right),
    ]),
  ];

  List<Widget> _iconButtons() => [
    _row([
      IconBtn(Glyphs.plus, tooltip: 'New agent', onTap: () {}),
      IconBtn(Glyphs.search, tooltip: 'Search', onTap: () {}),
      IconBtn(
        Glyphs.sidebar_left,
        tooltip: 'Projects and threads',
        onTap: () {},
      ),
      const IconBtn(Glyphs.mic, tooltip: 'Talking it through is planned'),
    ]),
  ];

  List<Widget> _pills() => [
    _row(const [
      Pill('Draft', tooltip: 'Not yet marked ready on the forge'),
      Pill('Auto'),
      Pill.icon('marshal', icon: Glyphs.folder, tooltip: 'Project'),
      Pill.icon(
        'main',
        icon: Glyphs.arrow_branch,
        tooltip: 'Base branch',
        mono: true,
      ),
      Pill('qwen3.5-9b · local', mono: true),
    ]),
  ];

  List<Widget> _dots(Ember e) => [
    _row([
      for (final status in Status.values)
        Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Dot(status),
            const SizedBox(width: Space.s6),
            Text(status.word, style: e.small.tint(e.muted)),
          ],
        ),
    ]),
  ];

  List<Widget> _labels() => [
    const Label('Projects'),
    const Label('Needs you', count: 6),
    Label(
      'Pull requests',
      count: 5,
      link: LabelLink('All pull requests', onTap: () {}),
    ),
  ];

  List<Widget> _tabs() => [
    Tabs(
      tabs: const [
        TabItem('Changes'),
        TabItem('Files'),
        TabItem('Terminal'),
        TabItem('Preview'),
        TabItem('Inspect'),
      ],
      index: _tab,
      onChanged: (i) => setState(() => _tab = i),
    ),
    Tabs(
      tabs: const [
        TabItem('All', count: 9),
        TabItem('Blocking', count: 4),
        TabItem('To look at', count: 2),
        TabItem('Ready', count: 3),
      ],
      index: _filter,
      onChanged: (i) => setState(() => _filter = i),
    ),
  ];

  List<Widget> _segments() => const [
    Segments(done: 2, total: 5, status: Status.needsYou),
    Segments(done: 3, total: 6, status: Status.running),
    Segments(done: 1, total: 4, status: Status.failed),
    Segments(done: 4, total: 4, status: Status.ready),
  ];

  List<Widget> _meters() => const [
    Meter(label: 'studio-box', value: '6 of 8 slots', fraction: 0.75),
    Meter(label: 'Local daemon', value: '1 of 2 slots', fraction: 0.5),
    Meter.fault(label: 'vps-syd', value: 'unreachable 4m', fraction: 0),
  ];

  List<Widget> _spark() => const [
    Spark([
      0.2,
      0.3,
      0.3,
      0.6,
      0.9,
      1.4,
      1.5,
      2.2,
      3.1,
      3.4,
      4.6,
      5.1,
      5.9,
    ], semanticLabel: 'Spend today, rising to 5.92 dollars'),
  ];

  static const _checks = [
    Check('build', CheckState.passed),
    Check('vet', CheckState.passed),
    Check('lint', CheckState.passed),
    Check('unit', CheckState.failed),
    Check('race', CheckState.running),
    Check('usability', CheckState.running),
    Check('web', CheckState.pending),
    Check('release config', CheckState.pending),
  ];

  List<Widget> _strips() => const [
    CheckStrip(_checks),
    CheckStrip([
      Check('build', CheckState.passed),
      Check('test', CheckState.passed),
      Check('lint', CheckState.passed),
    ]),
  ];

  /// A diff a person reads: a header, two hunks and the three body kinds.
  static const _diff =
      'diff --git a/internal/viewmodel/index.json b/internal/viewmodel/index.json\n'
      'index 3f1c2aa..a09e771 100644\n'
      '--- a/internal/viewmodel/index.json\n'
      '+++ b/internal/viewmodel/index.json\n'
      '@@ -4,3 +4,1 @@\n'
      '     "fixtures": [\n'
      '-     "turn_legacy_v1.json",\n'
      '-     "turn_legacy_v2.json",\n'
      '     "turn_fixtures.json"\n'
      '\\ No newline at end of file\n';

  List<Widget> _diffViews() => [
    const DiffView(diff: _diff),
    DiffView(diff: _diff, onComment: (line) {}),
    const DiffView(diff: '', emptyNote: 'No changes in this file'),
  ];

  /// Every state a gate check can be in, and a check with nothing to say
  /// beside it.
  List<Widget> _gateLists() => const [
    GateList([
      GateCheck('Build', CheckState.passed, detail: '4.1s'),
      GateCheck('Tests', CheckState.passed, detail: '412 passed'),
      GateCheck('Lint', CheckState.failed, detail: '1 warning'),
      GateCheck('Usability', CheckState.running, detail: 'running'),
      GateCheck('Release config', CheckState.pending),
    ]),
    GateList([GateCheck('Ran 2m ago', CheckState.passed)]),
  ];

  List<Widget> _rows() => [
    Rows([
      RowItem(
        name: 'studio-box',
        description: 'Server · Tailscale · linux/arm64 · containers',
        value: '6 of 8 slots',
        onTap: () {},
      ),
      RowItem(
        name: 'Local daemon',
        description: 'This Mac · host processes · launch at login',
        value: '1 of 2 slots',
        onTap: () {},
      ),
      const RowItem(
        name: 'Disk',
        description: 'Mirrors, workspaces and images',
        value: '41 of 120 GB',
      ),
      const RowItem(
        name: 'The gate',
        description: 'command · exit != 0 · every 5s',
        spark: RowSpark([
          0,
          0,
          1,
          1,
          0,
          1,
          0,
        ], label: 'Today’s readings of the gate'),
        value: 'Fired 2m ago',
      ),
      const RowItem.planned(
        name: 'Signal and Element',
        description: 'Notifications there are planned.',
      ),
    ]),
    // A row whose name is an identifier, and the row the list is showing.
    Rows([
      RowItem(
        name: 'internal/viewmodel/index.json',
        description: 'Changed by this thread',
        value: '+0 −9',
        mono: true,
        selected: true,
        onTap: () {},
      ),
      RowItem(
        name: 'internal/repo/symbols_test.go',
        description: 'Changed by this thread',
        value: '+3 −11',
        mono: true,
        onTap: () {},
      ),
    ]),
  ];

  List<Widget> _asks() => [
    AskCard(
      kind: AskKind.decision,
      title: 'Should recovery survive a daemon restart?',
      body:
          'The plan covers reconnects. Persisting approvals across a restart '
          'widens the scope by one pull request.',
      waiting: '12m',
      waitingSince: 'Asked at 14:02',
      source: 'marshal › Reliable session recovery',
      onOpenSource: () {},
      actions: [
        AskAction('Keep to reconnects', onTap: () {}),
        AskAction('Expand scope', onTap: () {}),
        AskAction('Discuss', onTap: () {}),
      ],
    ),
    AskCard(
      kind: AskKind.question,
      title: 'Keep the legacy test fixtures?',
      body: 'Three fixtures are read by nothing since the recorder moved.',
      waiting: '8m',
      source: 'marshal › Fixture cleanup',
      placement: AskPlacement.overview,
      actions: [
        AskAction('Remove them', onTap: () {}),
        AskAction('Keep them', onTap: () {}),
      ],
    ),
    AskCard(
      kind: AskKind.approval,
      title: 'go test ./internal/... -run TestRecorder',
      body: 'Runs the recorder tests in the workspace.',
      waiting: '1m',
      placement: AskPlacement.thread,
      actions: [
        AskAction('Allow once', onTap: () {}),
        AskAction('Allow for this agent', onTap: () {}),
        AskAction('Refuse', onTap: () {}),
      ],
    ),
    AskCard(
      kind: AskKind.network,
      title: 'Allow api.stripe.com?',
      body: 'Booking updates asked to reach it from node-web.',
      waiting: '3m',
      source: 'av-portal › Booking updates',
      actions: [
        AskAction('Allow for this agent', onTap: () {}),
        AskAction('Add to environment', onTap: () {}),
        AskAction('Block', onTap: () {}),
      ],
    ),
    AskCard(
      kind: AskKind.review,
      title: 'Review draft for #131: 2 blocking findings',
      body: 'The review bot holds anything blocking until you look.',
      waiting: '20m',
      source: 'marshal › PR review bot',
      actions: [
        AskAction('Post it', onTap: () {}),
        AskAction('Edit', onTap: () {}),
      ],
    ),
    AskCard(
      kind: AskKind.intake,
      title: 'Issue #127 proposed as an agent',
      body: 'Fleet screen loses selection after reconnect.',
      waiting: '1h 4m',
      source: 'marshal › Issue intake',
      actions: [
        AskAction('Start it', onTap: () {}),
        AskAction('Dismiss', onTap: () {}),
      ],
    ),
  ];

  static const _fixChoices = [
    ('Investigate only', 'Report the cause, change nothing'),
    ('Fix with a note', 'Tell the worker what you suspect'),
    ('Choose the worker', 'Model, and how many attempts'),
    ('Always fix failures in marshal', 'For every pull request here'),
  ];

  List<Widget> _pulls() => [
    PrCard(
      pull: const PullRequestFacts(
        number: 118,
        title: 'CI-first change review',
        repo: 'alecpullen/marshal',
        branch: 'factory/ci-first-review',
        adds: 412,
        dels: 96,
        checks: [
          Check('build', CheckState.passed),
          Check('vet', CheckState.passed),
          Check('lint', CheckState.passed),
          Check('unit', CheckState.passed),
        ],
        reviews: '2 reviews passed',
        age: '26m',
        state: PrState.ready,
        sourceKind: ThreadKind.workItem,
        sourceTitle: 'CI-first change review',
      ),
      actions: PrActions(
        review: () {},
        merge: () {},
        openOnForge: () {},
        openSource: () {},
      ),
    ),
    PrCard(
      pull: const PullRequestFacts(
        number: 123,
        title: 'Voice planning: quiet capture mode',
        repo: 'alecpullen/marshal',
        branch: 'factory/voice-planning',
        adds: 288,
        dels: 41,
        checks: _checks,
        reviews: 'review waits for checks',
        age: '4m',
        state: PrState.failing,
        failureTail:
            '--- FAIL: TestScriptedRecorderQuietMode (4.21s)\n'
            '    recorder_test.go:88: planner question interrupted a quiet '
            'capture',
        sourceKind: ThreadKind.workItem,
        sourceTitle: 'Voice planning',
      ),
      actions: PrActions(
        fix: () {},
        fixChoices: [
          for (final (name, why) in _fixChoices)
            MenuChoice(name, why, onSelect: () {}),
        ],
        fullLog: () {},
        openOnForge: () {},
        openSource: () {},
      ),
    ),
    PrCard(
      pull: const PullRequestFacts(
        number: 122,
        title: 'Fix flaky calendar test',
        repo: 'alecpullen/av-portal',
        branch: 'agent/flaky-calendar',
        adds: 18,
        dels: 6,
        checks: [
          Check('build', CheckState.passed),
          Check('test', CheckState.failed),
        ],
        reviews: 'no reviewers configured',
        age: '31m',
        state: PrState.fixing,
        sourceKind: ThreadKind.agent,
        sourceTitle: 'Fix flaky calendar test',
      ),
      actions: PrActions(watch: () {}, stop: () {}, openSource: () {}),
    ),
    PrCard(
      pull: const PullRequestFacts(
        number: 124,
        title: 'P6 plan 4: daemon upgrade',
        repo: 'alecpullen/marshal',
        branch: 'plan/p6-4',
        adds: 96,
        dels: 12,
        checks: [
          Check('build', CheckState.passed),
          Check('test', CheckState.running),
          Check('lint', CheckState.pending),
        ],
        reviews: 'no reviews yet',
        age: '2m',
        state: PrState.inProgress,
        draft: true,
        sourceKind: ThreadKind.planRun,
        sourceTitle: 'P6 plan 4',
      ),
      actions: PrActions(openOnForge: () {}, openSource: () {}),
    ),
  ];

  List<Widget> _threads() => [
    _narrow(
      Layout.sidebar,
      Column(
        children: [
          ThreadRow(
            status: Status.needsYou,
            title: 'Reliable session recovery',
            kind: ThreadKind.workItem,
            selected: true,
            onTap: () {},
          ),
          ThreadRow(
            status: Status.running,
            title: 'Voice planning',
            kind: ThreadKind.workItem,
            onTap: () {},
          ),
          ThreadRow(
            status: Status.ready,
            title: 'CI-first change review',
            kind: ThreadKind.workItem,
            onTap: () {},
          ),
          ThreadRow(
            status: Status.failed,
            title: 'Fix flaky calendar test',
            kind: ThreadKind.agent,
            onTap: () {},
          ),
          ThreadRow(
            status: Status.idle,
            title: 'P6 plan 4: daemon upgrade',
            kind: ThreadKind.planRun,
            onTap: () {},
          ),
        ],
      ),
    ),
  ];

  /// A specimen held to [width], as it would be in a sidebar or a form.
  /// The sidebar's rows (F2): a selected place with a count, a plain place,
  /// and a project open and closed.
  List<Widget> _navRows() => [
    _narrow(
      Layout.sidebar,
      Column(
        children: [
          NavRow(
            icon: Glyphs.tray,
            label: 'Inbox',
            count: 3,
            selected: true,
            onTap: () {},
          ),
          NavRow(
            icon: Glyphs.arrow_merge,
            label: 'Pull requests',
            onTap: () {},
          ),
          NavRow(
            label: 'marshal',
            expanded: true,
            onToggle: () {},
            onTap: () {},
          ),
          NavRow(
            label: 'Design docs',
            expanded: false,
            onToggle: () {},
            onTap: () {},
          ),
          NavRow(
            icon: Glyphs.gear,
            label: 'Settings',
            indent: true,
            onTap: () {},
          ),
        ],
      ),
    ),
  ];

  Widget _narrow(double width, Widget child) => Align(
    alignment: Alignment.topLeft,
    child: ConstrainedBox(
      constraints: BoxConstraints(maxWidth: width),
      child: child,
    ),
  );

  List<Widget> _states() => [
    const StateMessage.loading(),
    StateMessage.empty(
      message: 'Agents you start in this project appear here.',
      action: StateAction('Start an agent', onTap: () {}),
    ),
    const StateMessage.stale(age: '4m', detail: 'Reconnecting to studio-box'),
    StateMessage.error(
      message: 'The fleet could not be read.',
      retry: StateAction('Try again', onTap: () {}),
    ),
    StateMessage.unavailable(
      host: 'vps-syd',
      lastSeen: '4m',
      action: StateAction('Try again', onTap: () {}),
    ),
    const Box.flush(
      child: StateMessage.planned(
        name: 'Voice planning',
        note: 'Talking a plan through is planned.',
      ),
    ),
  ];

  List<Widget> _fields() => [
    _narrow(
      Layout.inspector,
      const Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Field(label: 'Branch', hint: 'main'),
          SizedBox(height: Space.s16),
          Field(
            label: 'Base branch',
            error: 'That branch does not exist on the forge',
          ),
          SizedBox(height: Space.s16),
          Field(label: 'Workspace', hint: 'new workspace', enabled: false),
          SizedBox(height: Space.s16),
          Field.multiline(
            label: 'Prompt',
            hint: 'Run the gate on {{ branch }}',
          ),
        ],
      ),
    ),
  ];

  List<Widget> _menus() => [
    _row([
      Menu(
        choices: [
          for (final (name, why) in _fixChoices)
            MenuChoice(name, why, onSelect: () {}),
        ],
        anchor: (context, toggle) => IconBtn(
          Glyphs.chevron_down,
          tooltip: 'Other ways to send this',
          onTap: toggle,
        ),
      ),
      Menu(
        choices: [
          MenuChoice('studio-box', 'Server · 6 of 8 slots', onSelect: () {}),
          MenuChoice(
            'Local daemon',
            'This Mac · 1 of 2 slots',
            onSelect: () {},
          ),
        ],
        anchor: (context, toggle) =>
            Btn('All hosts', icon: Glyphs.chevron_down, onTap: toggle),
      ),
    ]),
  ];

  /// The transcript rows (F3): what the user said, a step's header, its calls,
  /// the agent's words and output, the live row, and the field that drives it.
  List<Widget> _turnPrompts() => [
    _narrow(
      Layout.reading,
      const Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          TurnPrompt(text: 'Clean up test fixtures nobody uses any more.'),
          TurnPrompt(text: 'Two lines\nstay two lines'),
        ],
      ),
    ),
  ];

  List<Widget> _stepHeaders() => [
    _narrow(
      Layout.reading,
      Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const StepHeader(
            number: '1',
            headline: 'Mapped the fixtures and who reads them',
            took: '41s',
          ),
          const StepHeader(
            number: '2',
            headline: 'Removed the fixtures that are plainly dead',
            took: '1m 08s',
            foldable: true,
            onTap: _noop,
          ),
          const StepHeader(
            number: '3',
            headline: 'Waiting for your call on testdata/legacy',
            live: true,
          ),
          StepHeader(
            number: '4',
            headline: 'Ran the suite',
            took: '2m 01s',
            foldable: true,
            folded: true,
            onTap: _noop,
          ),
        ],
      ),
    ),
  ];

  List<Widget> _toolRows() => [
    _narrow(
      Layout.reading,
      Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          ToolRow(
            verb: 'read',
            target: 'internal/acp/acptest/fixtures.go',
            result: '148 lines',
            onTap: _noop,
          ),
          ToolRow(
            verb: 'shell',
            target: 'scripts/test.sh',
            result: '412 passed',
            onTap: _noop,
          ),
          ToolRow(
            verb: 'shell',
            target: 'go test ./internal/nope',
            result: 'exit 1',
            outcome: ToolOutcome.failed,
            onTap: _noop,
          ),
          ToolRow(
            verb: 'search',
            target: 'testdata/ --type go',
            result: 'running',
            outcome: ToolOutcome.running,
            selected: true,
            onTap: _noop,
          ),
        ],
      ),
    ),
  ];

  List<Widget> _narrations() => [
    _narrow(
      Layout.reading,
      const Narration(
        text:
            'There are **96** fixture files. `82` are loaded by a test.\n\n'
            '- 82 are loaded\n'
            '- 14 are referenced nowhere\n\n'
            '1. Remove them\n'
            '2. Run the suite\n\n'
            '> The legacy directory is different.\n\n'
            '```\nok  marshal/internal/viewmodel  0.12s\n```',
      ),
    ),
  ];

  List<Widget> _outputBlocks() => [
    _narrow(
      Layout.reading,
      const Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          OutputBlock(
            text:
                'ok  marshal/internal/viewmodel\t0.12s\n'
                'FAIL marshal/internal/repo\t0.44s',
          ),
          SizedBox(height: Space.s12),
          OutputBlock(text: '… 4096 bytes kept', truncated: true),
        ],
      ),
    ),
  ];

  List<Widget> _liveRows() => [
    _narrow(
      Layout.reading,
      const Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          LiveRow(
            headline: 'Running the tests',
            detail: 'ok marshal/internal/viewmodel',
            mono: true,
          ),
          LiveRow(
            headline: 'Waiting for your call',
            detail: 'Approval needed for shell.run',
          ),
          LiveRow(
            headline: 'The command failed',
            detail: 'exit 1',
            mono: true,
            failed: true,
          ),
        ],
      ),
    ),
  ];

  List<Widget> _composers() => [
    _narrow(
      Layout.reading,
      Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Composer(
            controller: _composer,
            hint: 'Reply to the agent…',
            mode: 'Act',
            model: 'qwen3-coder',
            onSend: _noop,
          ),
          const SizedBox(height: Space.s16),
          Composer(
            controller: _composer,
            hint: 'Steer the agent…',
            running: true,
            mode: 'Auto',
            model: 'qwen3-coder',
            onSend: _noop,
            onStop: _noop,
          ),
          const SizedBox(height: Space.s16),
          Composer(
            controller: _composer,
            hint: 'Reply to the agent…',
            readOnly: true,
            readOnlyReason:
                'This session is in ask mode with no editing granted.',
          ),
        ],
      ),
    ),
  ];

  /// A tooltip a screen adds itself: the exact time behind a relative one.
  Widget _tip(Ember e) => Row(
    children: [
      Text('Asked', style: e.small.tint(e.muted)),
      const SizedBox(width: Space.s6),
      Tip(
        message: 'Asked at 14:02 today',
        child: Text('12m ago', style: e.monoSmall.tint(e.muted)),
      ),
    ],
  );

  /// The one list-and-detail pattern of F7 (decision 2), drawn from a
  /// fixture section: every section of Automations, Library and Settings is
  /// this page.
  Widget _sectionPage() => SectionPage<String>(
    title: 'Schedules',
    subtitle: 'What starts work without you',
    model: _GallerySection(),
    emptyMessage: 'A schedule you add appears here.',
    row: (name, onTap) => RowItem(
      name: name,
      description: 'The nightly gate, on a cron',
      value: 'Daily · 03:00',
      onTap: onTap,
    ),
    detail: (context, name) => Field(label: 'Recipe', hint: name),
  );

  Sheet _sheet(BuildContext context) => Sheet(
    title: 'Discard this work?',
    actions: [
      Btn(
        'Discard 14 changed files',
        onTap: () => Navigator.of(context).maybePop(),
      ),
      Btn('Keep it', onTap: () => Navigator.of(context).maybePop()),
    ],
    child: const Text(
      'The workspace and its 14 changed files are deleted. The branch on '
      'the forge is kept.',
    ),
  );
}

/// A section with fixture rows, so the gallery draws `SectionPage` without
/// reaching a bridge. Its rows are there from the first frame, so the
/// specimen is the same pixels every time.
class _GallerySection extends SectionModel<String> {
  static const _rows = ['nightly gate', 'weekly review'];

  @override
  List<String> get items => _rows;

  @override
  Future<List<String>> fetch() async => _rows;

  @override
  Object keyOf(String item) => item;

  @override
  bool isNew(String item) => !_rows.contains(item);

  @override
  Future<String> persist(String item) async => item;
}
