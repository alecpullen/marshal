import 'dart:async';

import '../../model/age.dart';
import '../../model/expected_checks.dart';
import '../../model/project_model.dart';
import '../../model/section_model.dart' show SaveState;
import '../../model/thread_ref.dart' as threads;
import '../../model/work_item_model.dart' show WorkItemView;
import '../sections/unsaved_guard.dart';
import '../kit/kit.dart';
import 'board.dart';
import 'defaults_tab.dart';
import 'health_tab.dart';
import 'status.dart';

/// One project's page (F6 decision 12): its work, its automations, its
/// defaults and its health.
///
/// Work is the factory's board of the project's items, then its agents and its
/// plan runs. Defaults edits the project's settings and saves them with the
/// conditional write the bridge requires, asking before leaving an unsaved
/// edit. Health reports what the bridge can see and offers discovery.
///
/// The line above the sections names what the section is for, and the page's
/// own header carries the project's name with its root, so two same-named
/// projects are told apart. A project that cannot be reached is called out
/// once, above the sections, whatever one is open.
///
/// The page takes the model and the views the shell holds and never fetches:
/// the factory store is the one reader of the item list (F6 decision 14), and
/// the model reads settings, health and runs behind its own interface (§5).
class ProjectPage extends StatefulWidget {
  const ProjectPage({
    super.key,
    required this.model,
    this.items = const [],
    this.agents = const [],
    this.state,
    this.now,
    this.initialTab = 0,
    this.guard,
    this.onOpenItem,
    this.onOpenAgent,
    this.onOpenRun,
    this.onOpenAutomations,
    this.onNewWork,
    this.observedChecks = const <ObservedCheck>[],
  });

  final ProjectModel model;

  /// The project's work items, from the factory store, read as views.
  final List<WorkItemView> items;

  /// The project's agents, the factory's workers already left out (F6
  /// decision 13).
  final List<threads.ThreadRef> agents;

  /// The page's data state, when it is not ready (§7).
  final StateKind? state;

  /// What a relative age is measured against.
  final DateTime? now;

  /// The tab to open: Work is 0, Automations 1, Defaults 2, Health 3.
  final int initialTab;

  /// The shell's one guard. The page watches the model while it is mounted,
  /// so leaving with an unsaved edit asks first (design-system.md §9).
  final UnsavedGuard? guard;

  final void Function(WorkItemView item)? onOpenItem;
  final void Function(threads.ThreadRef agent)? onOpenAgent;
  final void Function(ProjectRun run)? onOpenRun;

  /// Opens the Automations place. Null leaves the summary without its link.
  final VoidCallback? onOpenAutomations;

  /// Opens the Studio's new-work dialog for this project: the page's one
  /// primary action, "New" (design.md, "Starting work"). Null leaves the header
  /// without one rather than drawing a button that opens nothing.
  final VoidCallback? onNewWork;

  /// The checks this project's recorded work has observed: the evidence the
  /// Defaults tab may offer beside the editor, never a requirement.
  final List<ObservedCheck> observedChecks;

  @override
  State<ProjectPage> createState() => _ProjectPageState();
}

class _ProjectPageState extends State<ProjectPage> {
  static const _labels = ['Work', 'Automations', 'Defaults', 'Health'];

  /// What the line under the tab bar says about the section shown: one
  /// short line per label, in the design's own words (§8).
  static const _descriptions = [
    'The project\'s work items, its agents and its plan runs',
    'The forge automations configured for this project',
    'What new agents of this project run as, and what a handoff inherits',
    'What the bridge can see about this project, and the discovery it offers',
  ];

  late int _tab = _clamped(widget.initialTab);

  static int _clamped(int tab) =>
      tab < 0 ? 0 : (tab >= _labels.length ? _labels.length - 1 : tab);

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_changed);
    widget.guard?.watch(widget.model);
    unawaited(widget.model.load());
    unawaited(widget.model.loadHealth());
    unawaited(widget.model.loadRuns());
  }

  @override
  void dispose() {
    widget.guard?.unwatch(widget.model);
    widget.model.removeListener(_changed);
    super.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final model = widget.model;
    final e = Ember.of(context);
    return PageBody(
      title: model.name,
      subtitle: model.root,
      width: PageWidth.full,
      actions: _actions(),
      children: [
        if (widget.state == StateKind.unavailable) ...[
          // An unreachable project is called out once, above the sections,
          // whichever one is open (§7: unavailable replaces the content, so
          // there is no second copy beneath and nothing half-read).
          _stateMessage(StateKind.unavailable),
          const SizedBox(height: Space.s12),
        ],
        Tabs(
          tabs: [
            TabItem('Work', count: widget.items.length),
            const TabItem('Automations'),
            const TabItem('Defaults'),
            const TabItem('Health'),
          ],
          index: _tab,
          onChanged: (index) => setState(() => _tab = index),
        ),
        const SizedBox(height: Space.s8),
        Text(_descriptions[_tab], style: e.small.tint(e.muted)),
        const SizedBox(height: Space.s20),
        // A tab is a page of its own: a fresh subtree per tab keeps one tab's
        // open sheet and half-typed edit off the next. An unreachable project
        // shows the call-out above and nothing beneath it: the sections read
        // that project, so what they would draw is not what is there.
        KeyedSubtree(
          key: ValueKey(_tab),
          child: widget.state == StateKind.unavailable
              ? const SizedBox.shrink()
              : switch (_tab) {
                  0 => _work(context),
                  1 => _automations(context),
                  2 => DefaultsTab(
                    model: model,
                    state: widget.state,
                    now: widget.now,
                    observedChecks: widget.observedChecks,
                  ),
                  _ => HealthTab(
                    model: model,
                    state: widget.state,
                    now: widget.now,
                  ),
                },
        ),
      ],
    );
  }

  /// The page's actions: New, which starts work in this project, and — while
  /// the Defaults tab holds an edit — Save.
  ///
  /// New is the page's one primary, and it comes first (§5). Save is plain:
  /// the page is for the project's work, and its settings document is edited
  /// on one tab of it.
  List<Widget> _actions() {
    final model = widget.model;
    final newWork = widget.onNewWork;
    final saving = model.saveState == SaveState.saving;
    return [
      if (newWork != null)
        Btn.primary('New', icon: Glyphs.plus, onTap: newWork),
      if (_tab == 2)
        Btn(
          saving ? 'Saving…' : 'Save',
          onTap: model.dirty && !saving ? () => unawaited(model.save()) : null,
        ),
    ];
  }

  /// Work: the factory's board, then the project's agents and its plan runs.
  Widget _work(BuildContext context) {
    final state = widget.state;
    // Unavailable is the page's one call-out above the tabs; the tab draws
    // its content, never a second copy.
    if (state != null &&
        state != StateKind.stale &&
        state != StateKind.unavailable) {
      return _stateMessage(state);
    }
    final e = Ember.of(context);
    final model = widget.model;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (state == StateKind.stale) ...[
          StateMessage.stale(age: relativeAge(model.lastRead, _now)),
          const SizedBox(height: Space.s16),
        ],
        ProjectBoard(items: widget.items, onOpen: widget.onOpenItem),
        const SizedBox(height: Space.s20),
        Label('Agents', count: widget.agents.length),
        const SizedBox(height: Space.s8),
        if (widget.agents.isEmpty)
          Text(
            'No agent is running in this project.',
            style: e.small.tint(e.muted),
          )
        else
          Rows([
            for (final agent in widget.agents)
              RowItem(
                name: agent.title,
                description: agent.headline.trim().isEmpty
                    ? 'Agent ${agent.id}'
                    : agent.headline.trim(),
                value: threadStatusOf(agent.status).word,
                onTap: widget.onOpenAgent == null
                    ? null
                    : () => widget.onOpenAgent!(agent),
              ),
          ]),
        const SizedBox(height: Space.s20),
        Label('Plan runs', count: model.runs.length),
        const SizedBox(height: Space.s8),
        _runs(context),
      ],
    );
  }

  /// The project's plan runs: each one's agent, what it is doing and how far
  /// it has got.
  Widget _runs(BuildContext context) {
    final e = Ember.of(context);
    final model = widget.model;
    if (model.runsError != null) {
      return StateMessage.error(
        message: model.runsError!,
        retry: StateAction(
          'Read the runs again',
          onTap: () => unawaited(model.loadRuns()),
        ),
      );
    }
    if (model.runsLoading && model.runs.isEmpty) {
      return const StateMessage.loading(lines: 2);
    }
    if (model.runs.isEmpty) {
      return Text(
        'No plan run has been started in this project.',
        style: e.small.tint(e.muted),
      );
    }
    return Rows([
      for (final run in model.runs)
        RowItem(
          name: run.name.isEmpty ? run.agentID : run.name,
          description: run.line,
          value: run.progress,
          mono: run.name.isEmpty,
          onTap: widget.onOpenRun == null ? null : () => widget.onOpenRun!(run),
        ),
    ]);
  }

  /// The automations as they are configured, read-only until F7's place is
  /// what the project links to.
  Widget _automations(BuildContext context) {
    final state = widget.state;
    // Unavailable is the page's one call-out above the tabs; the tab draws
    // its content, never a second copy.
    if (state != null &&
        state != StateKind.stale &&
        state != StateKind.unavailable) {
      return _stateMessage(state);
    }
    final e = Ember.of(context);
    final automations = widget.model.automations;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        if (state == StateKind.stale) ...[
          StateMessage.stale(age: relativeAge(widget.model.lastRead, _now)),
          const SizedBox(height: Space.s16),
        ],
        Label('Automations'),
        const SizedBox(height: Space.s8),
        if (automations == null)
          Text(
            'No forge automation is configured for this project.',
            style: e.small.tint(e.muted),
          )
        else
          Rows([
            RowItem(
              name: 'Review bot',
              description: automations.reviewBotRepo.isEmpty
                  ? 'Reviews this project\'s pull requests'
                  : 'Reviewing ${automations.reviewBotRepo}',
              value: _onOff(automations.reviewBotOn),
            ),
            RowItem(
              name: 'CI fixer',
              description: automations.ciFixerBranches.isEmpty
                  ? 'Fixes failing checks on watched branches'
                  : 'Watching ${automations.ciFixerBranches.join(', ')}',
              value: _onOff(automations.ciFixerOn),
            ),
            RowItem(
              name: 'Auto fix',
              description: 'Fixes a failing check without being asked',
              value: _onOff(automations.autoFix),
            ),
          ]),
        const SizedBox(height: Space.s16),
        if (widget.onOpenAutomations != null)
          Align(
            alignment: Alignment.centerLeft,
            child: Btn(
              'Open automations',
              icon: Glyphs.arrow_right,
              onTap: widget.onOpenAutomations,
            ),
          ),
      ],
    );
  }

  DateTime get _now => widget.now ?? DateTime.now();

  static String _onOff(bool? value) => switch (value) {
    true => 'On',
    false => 'Off',
    null => 'Not set',
  };

  Widget _stateMessage(StateKind state) {
    final retry = StateAction(
      'Read the project again',
      onTap: () => unawaited(widget.model.load()),
    );
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 5),
      StateKind.empty => StateMessage.empty(
        message:
            'This project has no work yet: a work item or a plan run '
            'appears here as soon as one starts.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: widget.model.error ?? 'The project could not be read.',
        retry: retry,
      ),
      StateKind.unauthorized => const StateMessage.unauthorized(),
      StateKind.unavailable => StateMessage.unavailable(
        host: widget.model.name,
        lastSeen: relativeAge(widget.model.lastRead, _now),
        action: retry,
      ),
      StateKind.stale => StateMessage.stale(
        age: relativeAge(widget.model.lastRead, _now),
      ),
      StateKind.planned => StateMessage.planned(
        name: 'The project page',
        note: 'Not built yet',
      ),
    };
  }
}
