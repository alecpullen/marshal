import 'dart:async';

import '../../model/age.dart';
import '../../model/run_model.dart';
import '../kit/kit.dart';

/// A plan run's thread (F6 decision 11): its tasks as lanes, as a dependency
/// graph or as a timeline, the live step beneath, and the question it stopped
/// on pinned above.
///
/// The three views are the same run drawn three ways, so switching between them
/// never changes what the run is. Selecting a cell opens that role's
/// conversation through F3's thread screen — [center] — and [onBack] returns to
/// the run.
class RunScreen extends StatefulWidget {
  const RunScreen({
    super.key,
    required this.model,
    this.state,
    this.now,
    this.center,
    this.onBack,
    this.onRetry,
    this.onOpenCell,
  });

  final RunModel model;

  /// The run's data state, when it is not ready (§7).
  final StateKind? state;

  final DateTime? now;

  /// The role's own conversation, when a cell is open: the centre swaps for it
  /// and [onBack] returns to the run.
  final Widget? center;
  final VoidCallback? onBack;

  final VoidCallback? onRetry;

  /// Opens one cell's role conversation. Null leaves the cells read only.
  final void Function(String lane, int task)? onOpenCell;

  @override
  State<RunScreen> createState() => _RunScreenState();
}

class _RunScreenState extends State<RunScreen> {
  static const _views = ['Lanes', 'Graph', 'Timeline'];

  DateTime get _now => widget.now ?? DateTime.now();

  @override
  void initState() {
    super.initState();
    widget.model.addListener(_changed);
    // The lanes name the roles they drew, so the roster is read once here. A
    // model the app built without a reader draws nothing rather than guessing.
    unawaited(widget.model.loadRoster());
  }

  @override
  void dispose() {
    widget.model.removeListener(_changed);
    super.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final model = widget.model;
    final center = widget.center;
    if (center != null) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(
              Layout.pageSide,
              Layout.pageTop,
              Layout.pageSide,
              Space.s8,
            ),
            child: Align(
              alignment: Alignment.centerLeft,
              child: Btn(
                'Back to the run',
                icon: Glyphs.chevron_left,
                onTap: widget.onBack,
              ),
            ),
          ),
          Expanded(child: center),
        ],
      );
    }
    final state = widget.state;
    if (state != null && state != StateKind.stale) {
      return PageBody(title: _title(model), children: [_stateMessage(state)]);
    }
    return PageBody(
      title: _title(model),
      subtitle: model.progress,
      children: [
        if (state == StateKind.stale) ...[
          StateMessage.stale(
            age: relativeAge(_lastActive(model), _now),
            detail: 'The run has not reported; this is what it last said.',
          ),
          const SizedBox(height: Space.s16),
        ],
        if (model.hasGate) ...[_gate(model), const SizedBox(height: Space.s16)],
        Tabs(
          tabs: [for (final view in _views) TabItem(view)],
          index: model.view.index,
          onChanged: (index) => model.show(RunView.values[index]),
        ),
        const SizedBox(height: Space.s12),
        _view(model),
        const SizedBox(height: Space.s16),
        Label('Live step'),
        const SizedBox(height: Space.s4),
        _live(model),
      ],
    );
  }

  /// The run's own name, or its branch: what it is called.
  String _title(RunModel model) {
    final name = (model.run.planName ?? '').trim();
    if (name.isNotEmpty) return name;
    final branch = (model.run.branch ?? '').trim();
    return branch.isEmpty ? 'Plan run' : branch;
  }

  /// The question the run stopped on, as the kit's ask card.
  ///
  /// A run opened without the app's answer seam can be read but not answered,
  /// so its action is drawn disabled with the reason (§9) rather than as a tap
  /// that would open a sheet and post nothing.
  Widget _gate(RunModel model) => AskCard(
    kind: AskKind.question,
    title: model.gateQuestion.isEmpty
        ? 'Task ${model.gateTask} needs an answer'
        : model.gateQuestion,
    body: 'Task ${model.gateTask}',
    waiting: relativeAge(_lastActive(model), _now),
    placement: AskPlacement.thread,
    actions: [
      AskAction(
        'Answer',
        onTap: model.canAnswer ? () => unawaited(_answer(model)) : null,
        note: model.canAnswer
            ? null
            : 'This run was opened without the app’s answer route, so it can '
                  'be read here but not answered.',
      ),
    ],
  );

  /// Asks the person the gate's answer, through the kit's sheet.
  Future<void> _answer(RunModel model) async {
    final e = Ember.of(context);
    final controller = TextEditingController();
    final answer = await Sheet.show<String>(
      context,
      Sheet(
        title: 'Answer the run',
        actions: [
          Btn.primary(
            'Answer',
            onTap: () => Navigator.maybePop(context, controller.text.trim()),
          ),
        ],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Field.multiline(
              label: model.gateQuestion.isEmpty
                  ? 'Task ${model.gateTask}'
                  : model.gateQuestion,
              controller: controller,
              hint: 'What the run should do',
            ),
            const SizedBox(height: Space.s8),
            Text(
              'The run continues with this answer and nothing else.',
              style: e.small.tint(e.muted),
            ),
          ],
        ),
      ),
    );
    controller.dispose();
    final text = answer?.trim() ?? '';
    if (text.isEmpty) return;
    await model.answer(text);
  }

  Widget _view(RunModel model) => switch (model.view) {
    RunView.lanes => Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Box.flush(child: _lanes(model)),
        _roles(model),
      ],
    ),
    RunView.graph => Box(
      child: TaskGraph(
        nodes: [
          for (final node in model.graph.nodes)
            GraphNode(
              number: node.number,
              label: _nodeLabel(model, node.number),
              layer: node.layer,
              index: node.index,
              status: _nodeStatus(model, node.number),
              selected: model.selectedTask?.number == node.number,
            ),
        ],
        edges: [
          for (final edge in model.graph.edges)
            GraphEdge(from: edge.from, to: edge.to),
        ],
        empty: 'The run has no tasks yet',
      ),
    ),
    // One bar per role over the run's own tasks, in `Segments`' colours: the
    // lane's finished tasks are `ok`, the one it is on is its status colour,
    // and the rest are `border`.
    RunView.timeline => Box(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (final lane in RunLane.all) ...[
            Row(
              children: [
                SizedBox(
                  width: Layout.sidebar / 3,
                  child: Text(
                    lane,
                    style: Ember.of(context).label
                        .tint(Ember.of(context).muted),
                  ),
                ),
                Expanded(child: _timelineBar(model, lane)),
              ],
            ),
            const SizedBox(height: Space.s8),
          ],
        ],
      ),
    ),
  };

  /// One lane's bar: how many of the run's tasks that role has finished.
  Widget _timelineBar(RunModel model, String lane) {
    final tasks = model.tasks;
    final done = tasks
        .where((task) => task.cell(lane).state == CellState.passed)
        .length;
    final failed = tasks.any(
      (task) => task.cell(lane).state == CellState.failed,
    );
    final running = tasks.any(
      (task) => task.cell(lane).state == CellState.running,
    );
    final status = failed
        ? Status.failed
        : running
        ? Status.running
        : done == tasks.length && tasks.isNotEmpty
        ? Status.ready
        : Status.idle;
    if (tasks.isEmpty) {
      return Text(
        'No tasks yet',
        style: Ember.of(context).small.tint(Ember.of(context).muted),
      );
    }
    return Segments(done: done, total: tasks.length, status: status);
  }

  /// The lanes view: one row per task, one cell per role.
  Widget _lanes(RunModel model) {
    final tasks = model.tasks;
    if (tasks.isEmpty) {
      return Padding(
        padding: const EdgeInsets.all(Space.s16),
        child: Text(
          'The run has no tasks yet.',
          style: Ember.of(context).body.tint(Ember.of(context).muted),
        ),
      );
    }
    return Lanes(
      lanes: RunLane.all,
      rows: [
        for (final task in tasks)
          LaneRow(
            title: task.words,
            detail: task.fixRounds > 0
                ? '${task.fixRounds} ${task.fixRounds == 1 ? 'repair round' : 'repair rounds'}'
                : null,
            cells: [for (final lane in RunLane.all) _cell(model, task, lane)],
          ),
      ],
    );
  }

  /// The roles the lanes drew, under the grid (web/ui/src/lib/runs/Lanes.svelte):
  /// each role and the model behind it. A roster the bridge could not answer
  /// is stated, never hidden; a run the app opened without a reader draws
  /// nothing at all, rather than guessing bindings.
  Widget _roles(RunModel model) {
    if (!model.canReadRoster) return const SizedBox.shrink();
    final e = Ember.of(context);
    final rosterFailure = model.rosterFailure;
    if (rosterFailure != null) {
      return Padding(
        padding: const EdgeInsets.fromLTRB(
          Space.s16,
          Space.s12,
          Space.s16,
          Space.s12,
        ),
        child: Text(
          'The roles could not be read: $rosterFailure',
          style: e.small.tint(e.err),
        ),
      );
    }
    final roles = model.rosterRoles;
    if (!model.rosterSettled || roles.isEmpty) return const SizedBox.shrink();
    return Padding(
      padding: const EdgeInsets.fromLTRB(
        Space.s16,
        Space.s12,
        Space.s16,
        Space.s12,
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('Roles', style: e.label.tint(e.muted)),
          const SizedBox(height: Space.s8),
          Wrap(
            spacing: Space.s16,
            runSpacing: Space.s4,
            children: [
              for (final role in roles)
                Text('${role.role} · ${_binding(role)}', style: e.small),
            ],
          ),
        ],
      ),
    );
  }

  /// One role's binding in words: the provider and model it resolves to, why
  /// it is unresolved, or "default" when the bridge left both empty.
  static String _binding(RosterRole role) {
    final model = (role.model ?? '').trim();
    final provider = (role.provider ?? '').trim();
    if (model.isEmpty) {
      return (role.error ?? '').trim().isEmpty ? 'default' : 'unresolved';
    }
    return provider.isEmpty ? model : '$provider/$model';
  }

  /// One cell of one lane, as the kit's `LaneCell`: its status, its word, its
  /// note, and the action that opens the role's conversation.
  LaneCell _cell(RunModel model, RunTaskRow task, String lane) {
    final cell = task.cell(lane);
    final note = [
      if (cell.detail.isNotEmpty) cell.detail,
      if (cell.fixRounds > 0) '${cell.fixRounds} rounds',
      if (cell.commit.isNotEmpty) cell.commit,
    ].join(' · ');
    return LaneCell(
      status: _status(cell.state),
      words: cell.state.word,
      note: note.isEmpty ? null : note,
      selected: model.selectedCell == '$lane/${task.number}',
      onTap: widget.onOpenCell == null
          ? null
          : () {
              model.select(lane, task.number);
              widget.onOpenCell!(lane, task.number);
            },
    );
  }

  /// The graph node's label: its number and title.
  String _nodeLabel(RunModel model, int number) {
    for (final task in model.tasks) {
      if (task.number == number) return '${task.number} · ${task.title}';
    }
    return '$number';
  }

  /// A task's status, from its own status word.
  Status _nodeStatus(RunModel model, int number) {
    for (final task in model.tasks) {
      if (task.number != number) continue;
      return switch (task.status) {
        'done' || 'passed' => Status.ready,
        'failed' || 'error' => Status.failed,
        'active' || 'running' => Status.running,
        _ => Status.idle,
      };
    }
    return Status.idle;
  }

  /// The live step: what the run is doing now, in its own words.
  Widget _live(RunModel model) {
    final detail = (model.run.detail ?? '').trim();
    final phase = (model.run.phase ?? '').trim();
    final words = detail.isNotEmpty
        ? detail
        : phase.isNotEmpty
        ? phase
        : model.running
        ? 'Working'
        : 'Finished';
    return LiveRow(
      headline: 'Task ${model.run.currentTask}',
      detail: words,
      failed: (model.run.error ?? '').isNotEmpty,
    );
  }

  /// How long ago the run last reported.
  DateTime _lastActive(RunModel model) {
    final stamp = model.run.phaseStartedAt ?? model.run.startedAt;
    if (stamp == null || stamp == 0) return _now;
    return DateTime.fromMillisecondsSinceEpoch(stamp);
  }

  /// A cell's state in the kit's vocabulary.
  static Status _status(CellState state) => switch (state) {
    CellState.passed => Status.ready,
    CellState.failed => Status.failed,
    CellState.running => Status.running,
    CellState.pending => Status.idle,
  };

  Widget _stateMessage(StateKind state) {
    final retry = StateAction(
      'Read the run again',
      onTap: widget.onRetry ?? () {},
    );
    return switch (state) {
      StateKind.loading => const StateMessage.loading(lines: 4),
      StateKind.empty => StateMessage.empty(
        message: 'No plan run here: a run appears once a plan starts.',
        action: retry,
      ),
      StateKind.error => StateMessage.error(
        message: 'The run could not be read.',
        retry: retry,
      ),
      StateKind.unauthorized => const StateMessage.unauthorized(),
      StateKind.unavailable => StateMessage.unavailable(
        host: widget.model.agentID,
        lastSeen: relativeAge(_now, _now),
        action: retry,
      ),
      StateKind.stale => const StateMessage.stale(age: 'now'),
      StateKind.planned => StateMessage.planned(
        name: 'Plan run',
        note: 'Not built yet',
      ),
    };
  }
}
