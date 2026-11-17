import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';

// The digest and the routes are passed in, so their fields stay private: Dart
// has no private named parameter.
// ignore_for_file: prefer_initializing_formals

/// The three views of one run (F6 decision 11): the same run's tasks as lanes,
/// as a dependency graph, or as a timeline of the roles.
enum RunView { lanes, graph, timeline }

/// What one cell of a lane is. A stage's own state maps onto four of these: a
/// state this build does not know is drawn as its own word, never hidden.
enum CellState {
  pending('Pending'),
  running('Running'),
  passed('Passed'),
  failed('Failed');

  const CellState(this.word);

  final String word;

  /// Reads a stage state. `done` passed, `active`/`running` is running,
  /// `failed` failed, and anything else — `pending`, `skipped`, a state a
  /// newer bridge sent — is pending, because a stage that has not reported
  /// success has not passed.
  static CellState of(String state) => switch (state) {
    'done' || 'passed' || 'complete' => CellState.passed,
    'active' || 'running' => CellState.running,
    'failed' || 'error' => CellState.failed,
    _ => CellState.pending,
  };
}

/// One cell of a lane: what one role did for one task.
@immutable
class RunCell {
  const RunCell({
    required this.state,
    this.detail = '',
    this.fixRounds = 0,
    this.commit = '',
  });

  final CellState state;

  /// What the stage said about itself, in its own words.
  final String detail;

  /// How many repair rounds the review asked for, when it did.
  final int fixRounds;

  /// The commit's short head, for the commit column.
  final String commit;

  /// The cell in one line, for its tooltip.
  String get words => detail.isEmpty ? state.word : '${state.word} · $detail';
}

/// The four lanes of a run, in order (F6 decision 11): what the implementer did,
/// what the gate said, what the reviewer read, and what was committed.
abstract final class RunLane {
  static const implement = 'Implement';
  static const verify = 'Verify';
  static const review = 'Review';
  static const commit = 'Commit';

  /// Every lane, in the order the lanes view draws them.
  static const all = [implement, verify, review, commit];

  /// The stage name a lane reads, lower-cased as the digest writes it.
  static String stage(String lane) => lane.toLowerCase();
}

/// One task as a lane is drawn: its number, its title, what each role did, and
/// what it depends on.
@immutable
class RunTaskRow {
  const RunTaskRow({
    required this.number,
    required this.title,
    required this.cells,
    required this.dependsOn,
    required this.status,
    this.fixRounds = 0,
    this.commitSHA = '',
  });

  /// Reads one digest task.
  factory RunTaskRow.of(RunTask task) {
    final cells = <String, RunCell>{};
    for (final lane in RunLane.all) {
      final stage = task.stages
          .where((stage) => stage.name.toLowerCase() == RunLane.stage(lane))
          .firstOrNull;
      cells[lane] = RunCell(
        state: stage == null ? CellState.pending : CellState.of(stage.state),
        detail: stage?.detail ?? '',
        // Fix rounds are the loop between review and implement, so the review
        // lane carries them.
        fixRounds: lane == RunLane.review ? task.fixRounds : 0,
        commit: lane == RunLane.commit ? _commitSHA(task) : '',
      );
    }
    return RunTaskRow(
      number: task.n,
      title: task.title,
      cells: Map.unmodifiable(cells),
      dependsOn: List.unmodifiable(task.dependsOn),
      status: task.status,
      fixRounds: task.fixRounds,
      commitSHA: _commitSHA(task),
    );
  }

  final int number;
  final String title;
  final Map<String, RunCell> cells;
  final List<int> dependsOn;
  final String status;
  final int fixRounds;
  final String commitSHA;

  /// The cell one lane holds.
  RunCell cell(String lane) =>
      cells[lane] ?? const RunCell(state: CellState.pending);

  /// The task in one line: its number and title.
  String get words => 'Task $number · $title';

  static String _short(String sha) =>
      sha.length <= 7 ? sha : sha.substring(0, 7);

  /// The commit's short head, or "" when the task has committed nothing.
  static String _commitSHA(RunTask task) {
    final commit = task.commit;
    return commit == null ? '' : _short(commit.head);
  }
}

/// One node of the graph: its task, the layer it sits in, and its place within
/// that layer.
@immutable
class RunNode {
  const RunNode({
    required this.number,
    required this.layer,
    required this.index,
  });

  final int number;
  final int layer;
  final int index;
}

/// One edge of the graph: [from] must finish before [to] begins.
@immutable
class RunEdge {
  const RunEdge({required this.from, required this.to});

  final int from;
  final int to;
}

/// The graph as it is drawn: a layered layout, left to right, with straight
/// edges (F6 decisions: the graph draws `Depends on` edges where the plan has
/// them, and there is no free dragging).
@immutable
class GraphLayout {
  const GraphLayout({required this.nodes, required this.edges});

  final List<RunNode> nodes;
  final List<RunEdge> edges;

  /// The widest layer, which is how many columns the view needs.
  int get columns => nodes.isEmpty
      ? 0
      : nodes.map((node) => node.layer).reduce((a, b) => a > b ? a : b) + 1;

  /// The tallest column, which is how many rows the view needs.
  int get rows => nodes.isEmpty
      ? 0
      : nodes.map((node) => node.index).reduce((a, b) => a > b ? a : b) + 1;

  /// Lays the tasks out in dependency layers.
  ///
  /// A node's layer is the length of the longest path from a root, and nodes
  /// within a layer are ordered by task number, so the layout is stable. A
  /// dependency on a task the run does not have is ignored, and a cycle — which
  /// a valid plan cannot have — is cut rather than looped on.
  ///
  /// A plan that declares no dependency at all is linear work: its tasks are
  /// one chain in order, so the view reads left to right the way the run
  /// proceeds rather than stacking every task in one column.
  factory GraphLayout.of(List<RunTaskRow> tasks, {bool chained = false}) {
    final ordered = [...tasks]..sort((a, b) => a.number.compareTo(b.number));
    final byNumber = {for (final task in ordered) task.number: task};
    bool declaresDependencies = false;
    for (final task in ordered) {
      if (task.dependsOn.any(byNumber.containsKey)) declaresDependencies = true;
    }
    // A plan that declares no dependency is linear work: one chain in order.
    final asChain = chained || !declaresDependencies;

    List<int> depsOf(RunTaskRow task) {
      if (!asChain) {
        return [
          for (final dep in task.dependsOn)
            if (byNumber.containsKey(dep)) dep,
        ];
      }
      // A chain: each task waits on the one before it.
      final at = ordered.indexOf(task);
      return at <= 0 ? const [] : [ordered[at - 1].number];
    }

    final layerOf = <int, int>{};
    final visiting = <int>{};
    int depth(int number) {
      final known = layerOf[number];
      if (known != null) return known;
      if (visiting.contains(number)) return 0;
      visiting.add(number);
      var layer = 0;
      final task = byNumber[number];
      if (task != null) {
        for (final dep in depsOf(task)) {
          final from = depth(dep) + 1;
          if (from > layer) layer = from;
        }
      }
      visiting.remove(number);
      layerOf[number] = layer;
      return layer;
    }

    final perLayer = <int, int>{};
    final nodes = [
      for (final task in ordered)
        () {
          final layer = depth(task.number);
          final index = perLayer[layer] ?? 0;
          perLayer[layer] = index + 1;
          return RunNode(number: task.number, layer: layer, index: index);
        }(),
    ];
    final edges = [
      for (final task in ordered)
        for (final dep in depsOf(task)) RunEdge(from: dep, to: task.number),
    ];
    return GraphLayout(
      nodes: List.unmodifiable(nodes),
      edges: List.unmodifiable(edges),
    );
  }
}

/// One bar of the timeline: a lane's tasks, in the order they ran.
@immutable
class TimelineBar {
  const TimelineBar({
    required this.lane,
    required this.start,
    required this.end,
    required this.task,
  });

  final String lane;

  /// The fraction of the run each bar starts and ends at, 0 to 1.
  final double start;
  final double end;

  /// The task the bar is, for its tooltip.
  final int task;
}

/// One plan run's screen state (F6 decision 11): its tasks as lanes, the view
/// the person chose, the cell they selected, and the question it stopped on.
///
/// It reads the digest the bridge reports and projects it once, so the lanes,
/// the graph and the timeline are three drawings of one set of tasks rather
/// than three reads that can disagree.
class RunModel extends ChangeNotifier {
  RunModel({
    required this.agentID,
    required SDDRun run,
    Future<void> Function(String answer)? answer,
    RunView view = RunView.lanes,
  }) : _run = run,
       _answer = answer,
       _view = view;

  /// A model over the run digest a list carried (F6 decision 12), for a run a
  /// project page lists: the shell opens it without naming a wire type.
  factory RunModel.fromDigest(
    String agentID,
    Map<String, dynamic> digest, {
    Future<void> Function(String answer)? answer,
    RunView view = RunView.lanes,
  }) => RunModel(
    agentID: agentID,
    run: SDDRun.fromJson(digest),
    answer: answer,
    view: view,
  );

  /// The agent the run is on: the id its questions are answered through.
  final String agentID;

  final Future<void> Function(String answer)? _answer;
  SDDRun _run;
  RunView _view;
  String? _selectedCell;
  String? _failure;
  bool _answering = false;

  /// The digest this screen draws.
  SDDRun get run => _run;

  /// The view the person chose.
  RunView get view => _view;

  /// Every task, in lane order, one row each.
  List<RunTaskRow> get tasks =>
      List.unmodifiable([for (final task in _run.tasks) RunTaskRow.of(task)]);

  /// The graph's layout for the tasks the run has.
  GraphLayout get graph => GraphLayout.of(tasks);

  /// The timeline: one bar per task, per lane, over the run's own span.
  List<TimelineBar> get timeline {
    final started = _run.startedAt ?? 0;
    final endedAt = _run.endedAt ?? 0;
    final ended = endedAt == 0 ? _now() : endedAt;
    final span = ended - started;
    if (started == 0 || span <= 0) return const [];
    double at(int stamp) =>
        ((stamp - started) / span).clamp(0.0, 1.0).toDouble();
    return List.unmodifiable([
      for (final task in _run.tasks)
        if ((task.startedAt ?? 0) != 0 && (task.endedAt ?? 0) != 0)
          for (final lane in RunLane.all)
            if (task.stages.any(
              (stage) => stage.name.toLowerCase() == RunLane.stage(lane),
            ))
              TimelineBar(
                lane: lane,
                start: at(task.startedAt ?? 0),
                end: at(task.endedAt ?? 0),
                task: task.n,
              ),
    ]);
  }

  /// The cell the person selected, as `lane/task`, or null.
  String? get selectedCell => _selectedCell;

  /// The task a selected cell belongs to, for the role's conversation.
  RunTaskRow? get selectedTask {
    final selected = _selectedCell;
    if (selected == null) return null;
    final number = int.tryParse(selected.split('/').last);
    if (number == null) return null;
    for (final task in tasks) {
      if (task.number == number) return task;
    }
    return null;
  }

  /// Whether the run stopped on a question a person must answer.
  bool get hasGate => _run.gate != null;

  /// The question the run stopped on, in its own words, or "".
  String get gateQuestion => (_run.gate?.question ?? '').trim();

  /// The task the question belongs to, or 0.
  int get gateTask => _run.gate?.taskN ?? 0;

  /// Why the last answer failed, in the bridge's words.
  String? get failure => _failure;

  /// Whether an answer is in flight.
  bool get answering => _answering;

  /// Whether the app wired a way to post the run's answer. A run a project
  /// page lists is opened with the app's `answerRunFor` seam; one opened
  /// without it can be read but not answered, and its gate says so rather
  /// than offering a control that would do nothing.
  bool get canAnswer => _answer != null;

  /// Whether the run is still doing something.
  bool get running => _run.active && !_run.finished;

  /// The run in one line: its phase and how far it has got.
  String get progress {
    final phase = (_run.phase ?? '').trim();
    final done = '${_run.doneTasks} of ${_run.totalTasks}';
    return phase.isEmpty ? done : '$phase · $done';
  }

  /// Chooses a view.
  void show(RunView view) {
    if (_view == view) return;
    _view = view;
    notifyListeners();
  }

  /// Selects one cell, which opens that role's conversation for the task.
  void select(String lane, int task) {
    final cell = '$lane/$task';
    _selectedCell = _selectedCell == cell ? null : cell;
    notifyListeners();
  }

  /// Clears the selection, returning to the run's own views.
  void clearSelection() {
    if (_selectedCell == null) return;
    _selectedCell = null;
    notifyListeners();
  }

  /// Takes the run's newest digest. A run that has finished stops being
  /// drawn as running, and its last digest is kept.
  void apply(SDDRun run) {
    _run = run;
    notifyListeners();
  }

  /// Answers the question the run stopped on.
  Future<void> answer(String answer) async {
    final post = _answer;
    final words = answer.trim();
    if (post == null || words.isEmpty) return;
    _answering = true;
    _failure = null;
    notifyListeners();
    try {
      await post(words);
    } catch (error) {
      _failure = '$error';
    }
    _answering = false;
    notifyListeners();
  }

  int _now() => DateTime.now().millisecondsSinceEpoch;
}
