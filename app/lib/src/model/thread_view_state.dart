import 'package:flutter/foundation.dart';

import 'place.dart';

/// How much of each row the transcript draws (design-system.md §5).
enum Density {
  /// Headlines only: a step's first line, a tool row's name.
  outline,

  /// The default: headlines and their one-line context.
  comfortable,

  /// Everything a row carries: narration, output, arguments.
  full,
}

/// Where one handoff assignment stands (contract `CoordinatorAssignment.state`).
///
/// The reserved `stopped` value is named so a later scope's assignments still
/// read as words rather than as an unknown state; nothing in this build writes it.
enum HandoffAssignmentState {
  dispatching,
  pending,
  running,
  linked,
  failed,
  expired,
  stopped,
}

/// One assignment of a handoff, as the thread that handed the work off draws it:
/// the project it went to, where it stands, the thread it can be opened at, and —
/// when it is blocked on a person — the ask it is waiting on.
@immutable
class HandoffAssignmentRow {
  const HandoffAssignmentRow({
    required this.id,
    required this.project,
    required this.state,
    this.instruction = '',
    this.threadTitle = '',
    this.thread,
    this.failure,
    this.reason,
    this.updatedAt,
  });

  /// The assignment's identity: what a scoped retry names.
  final String id;

  /// The target project's name, as the daemon named it. Empty when the daemon
  /// knows no name for it, and the row then falls back to its id.
  final String project;

  final HandoffAssignmentState state;

  /// What this project was asked to do. It is the ask a blocked worker is
  /// waiting on, which is why it is kept rather than dropped after dispatch.
  final String instruction;

  /// What the thread is called, when the daemon reported one.
  final String threadTitle;

  /// Where opening this assignment's thread goes, or null when there is no
  /// thread to open (a pending submission has none yet).
  final Place? thread;

  /// The machine-readable failure, when the state is failed.
  final String? failure;

  /// The daemon's sentence for a failure, an expiry or a link it could not make.
  final String? reason;

  /// When the daemon last changed this assignment: what a row's age counts from.
  final DateTime? updatedAt;

  /// Whether this assignment is waiting on a person: a submission nobody has
  /// confirmed yet.
  bool get blocked => state == HandoffAssignmentState.pending;

  /// Whether a scoped retry may be offered. Only work that never started is
  /// retried here: a running or linked thread keeps its own Stop.
  bool get retryable =>
      state == HandoffAssignmentState.failed ||
      state == HandoffAssignmentState.expired;

  /// Where it stands, in words (§8: say what happened). The reason the daemon
  /// gave follows a failure, so the row never says only "failed".
  String get stateWords => switch (state) {
    HandoffAssignmentState.dispatching => 'Starting',
    HandoffAssignmentState.pending => 'Waiting for you',
    HandoffAssignmentState.running => 'Running',
    HandoffAssignmentState.linked => 'Linked',
    HandoffAssignmentState.failed =>
      'Failed${(reason ?? '').trim().isEmpty ? '' : ': ${reason!.trim()}'}',
    HandoffAssignmentState.expired => 'Expired',
    HandoffAssignmentState.stopped => 'Stopped',
  };

  /// What a row is called: the project, or its id when the daemon named none.
  String get name => project.trim().isEmpty ? id : project.trim();

  /// The first line of the ask, for a row or a control that must stay short.
  String get askLine {
    for (final line in instruction.split('\n')) {
      final text = line.trim();
      if (text.isNotEmpty) return text;
    }
    return '';
  }

  @override
  String toString() => 'HandoffAssignmentRow($id, ${state.name})';
}

/// One thread's handoff, as its transcript draws it: the coordinated task the
/// thread handed work to, the assignments each target resolved to, and the result
/// the daemon reported back.
///
/// It belongs to the thread rather than to a connection: a reload that reads the
/// same handoff redraws it, switching views keeps it, and a view whose read
/// failed says how old what it shows is instead of pretending it is current.
@immutable
class HandoffView {
  const HandoffView({
    required this.taskId,
    this.taskTitle = '',
    this.task,
    this.rows = const [],
    this.result,
    this.resultAt,
    this.observedAt,
    this.stale = false,
  });

  /// The durable task the thread is linked to.
  final String taskId;

  /// What the task is called.
  final String taskTitle;

  /// Where opening the coordinated task goes — Marshal's destination on the
  /// host that served it. Null when this build cannot link one.
  final Place? task;

  /// Each target the task dispatched to, in the daemon's order.
  final List<HandoffAssignmentRow> rows;

  /// What the daemon last reported back into this thread. It is display text,
  /// never markup.
  final String? result;

  /// When that result was delivered.
  final DateTime? resultAt;

  /// When this handoff was last read.
  final DateTime? observedAt;

  /// Whether the last read failed, so what this shows may be behind.
  final bool stale;

  /// Whether any assignment is waiting on a person.
  bool get blocked => rows.any((row) => row.blocked);

  /// Whether any assignment failed or lapsed, so a row offers a retry.
  bool get retryable => rows.any((row) => row.retryable);

  /// What a task with no title is called (the same words every other surface
  /// uses for an untitled task).
  String get name =>
      taskTitle.trim().isEmpty ? 'Marshal task' : taskTitle.trim();

  @override
  String toString() =>
      'HandoffView($taskId, ${rows.length} rows${stale ? ', stale' : ''})';
}

/// What one thread's screen remembers between rebuilds and between visits:
/// the density, where the browse cursor is, whether the view follows the live
/// end, which nodes the user folded, and the composer's draft.
///
/// It is per thread and holds no transcript: the tree comes from
/// [StackStore], and this holds only what the user chose. Nothing here is
/// derived from a node, so a patch that replaces a node leaves every choice
/// standing — which is why a fold survives the row being rewritten.
class ThreadViewState extends ChangeNotifier {
  ThreadViewState();

  Density _density = Density.comfortable;
  String? _cursor;
  bool _following = true;
  String _draft = '';
  final _folded = <String>{};
  HandoffView? _handoff;

  /// How much of each row to draw.
  Density get density => _density;

  /// Switches the density, in the order the spec cycles them: outline,
  /// comfortable, full, outline.
  void cycleDensity() {
    _density = switch (_density) {
      Density.outline => Density.comfortable,
      Density.comfortable => Density.full,
      Density.full => Density.outline,
    };
    notifyListeners();
  }

  /// Sets the density directly, for the settings screen.
  set density(Density value) {
    if (_density == value) return;
    _density = value;
    notifyListeners();
  }

  /// The node the browse cursor is on, or null before the user moves it.
  String? get cursor => _cursor;

  set cursor(String? value) {
    if (_cursor == value) return;
    _cursor = value;
    notifyListeners();
  }

  /// Whether the transcript stays at the live end. Scrolling up clears it and
  /// scrolling back to the end sets it again (decision 6).
  bool get following => _following;

  set following(bool value) {
    if (_following == value) return;
    _following = value;
    notifyListeners();
  }

  /// The composer's text for this thread: kept across rebuilds, and across a
  /// reconnect, because it belongs to the thread rather than to a connection.
  String get draft => _draft;

  set draft(String value) {
    if (_draft == value) return;
    _draft = value;
    notifyListeners();
  }

  /// Whether the user folded [nodeId]'s children away. A node the agent marks
  /// foldable starts expanded; only an explicit choice is remembered.
  bool folded(String nodeId) => _folded.contains(nodeId);

  /// The handoff this thread is the source of, or null when it handed nothing
  /// off (agent-first Studio, task 7).
  ///
  /// It belongs to the thread, as the draft does: reading it again replaces it,
  /// and switching to another view and back keeps what the thread last knew.
  HandoffView? get handoff => _handoff;

  set handoff(HandoffView? value) {
    if (_handoff == value) return;
    _handoff = value;
    notifyListeners();
  }

  /// Folds or unfolds [nodeId], remembering the choice for the rest of the
  /// session.
  void toggleFold(String nodeId) {
    if (!_folded.remove(nodeId)) _folded.add(nodeId);
    notifyListeners();
  }
}
