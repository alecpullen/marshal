// The routes are named for what they do and their fields stay private, so the
// parameters cannot be initializing formals: Dart has no private named
// parameter.
// ignore_for_file: prefer_initializing_formals

import 'dart:async';

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/transport.dart';
import 'fleet_store.dart' show describeFailure;
import 'place.dart';
import 'thread_view_state.dart';

/// Where one coordinated task stands, as the destination draws it.
///
/// `stopping` is a stop the daemon has recorded and not yet confirmed; it is what
/// the screen shows while a stop is pending, so a person is never told a task
/// stopped before the daemon says it did.
enum MarshalTaskState { open, stopping, stopped }

/// One coordinated task, as Marshal's destination lists and restores it.
///
/// It is this directory's own value rather than the bridge's
/// [CoordinatorTask] because `ui/` reads `model/` and never `client/` (F1's
/// boundary), and because the destination needs only what it draws: an id to
/// link, a session to read a transcript from, a title and where it stands.
@immutable
class MarshalTask {
  const MarshalTask({
    required this.id,
    required this.sessionId,
    required this.title,
    required this.state,
    required this.updatedAt,
  });

  /// Reads the wire task as the destination lists it.
  ///
  /// The wire's times are ISO strings and its title is optional, so both are
  /// read here rather than at every draw: a task with no title is named by its
  /// first message's line, and one with an unreadable time lists last instead
  /// of failing the whole read.
  factory MarshalTask.of(CoordinatorTask task) => MarshalTask(
    id: task.id,
    sessionId: task.sessionID,
    title: (task.title ?? '').trim(),
    state: switch (task.status) {
      CoordinatorTaskStatus.coordinatorTaskStopped => MarshalTaskState.stopped,
      CoordinatorTaskStatus.coordinatorTaskStopping =>
        MarshalTaskState.stopping,
      _ => MarshalTaskState.open,
    },
    updatedAt:
        DateTime.tryParse(task.updatedAt) ??
        DateTime.fromMillisecondsSinceEpoch(0, isUtc: true),
  );

  /// The durable task id: what a Marshal link names.
  final String id;

  /// The coordinator conversation's session, which a transcript is read from.
  final String sessionId;

  /// What the task is called. Empty when the daemon stored no title, and the
  /// screen names it by its first message instead.
  final String title;

  final MarshalTaskState state;

  /// When the task last changed: what "recent tasks" is ordered by.
  final DateTime updatedAt;

  /// What a menu or a row calls it.
  String get name => title.isEmpty ? 'Marshal task' : title;

  @override
  String toString() => 'MarshalTask($id, ${state.name})';
}

/// What one attention entry is (contract `CoordinatorAttentionEntry.kind`): an
/// ask that needs a decision, or a review that is ready.
enum MarshalEntryKind { ask, review }

/// One source an entry points at: the project it belongs to and what kind of
/// thing it is. The project is the daemon's opaque id, which is what an access
/// check is keyed by — never a path.
@immutable
class MarshalSourceRef {
  const MarshalSourceRef({
    required this.projectId,
    required this.kind,
    required this.id,
  });

  final String projectId;

  /// "agent", "intake", "pull" or "review".
  final String kind;
  final String id;

  @override
  String toString() => 'MarshalSourceRef($kind $id)';
}

/// One thing Marshal found, with where it opens.
@immutable
class MarshalEntry {
  const MarshalEntry({
    required this.kind,
    required this.text,
    required this.age,
    required this.source,
    this.place,
  });

  final MarshalEntryKind kind;

  /// Display text the daemon wrote, never markup.
  final String text;

  /// How long it has waited.
  final Duration age;

  final MarshalSourceRef source;

  /// The Studio place that opens this at its source — the thread on the host
  /// that served the read, Needs you, or Pull requests. Null when the daemon
  /// named no destination this build can honour, and the row is then a fact
  /// without a link rather than a link that lies.
  final Place? place;

  /// What the row calls its kind (§8: the product's nouns, not a synonym).
  String get kindLabel => switch (kind) {
    MarshalEntryKind.ask => 'Ask',
    MarshalEntryKind.review => 'Review',
  };

  /// What opening it does, for the row's tooltip: the place it goes to, in the
  /// words that place is named by.
  String? get opensLabel {
    final place = this.place;
    if (place == null) return null;
    return switch (place.kind) {
      PlaceKind.thread => 'Opens the thread the ask came from',
      PlaceKind.inbox => 'Opens Needs you, where the asks are answered',
      PlaceKind.pulls => 'Opens Pull requests, where the reviews are',
      _ => 'Opens the source of this ask',
    };
  }

  @override
  String toString() => 'MarshalEntry(${kind.name}, ${age.inMinutes}m)';
}

/// Where one handoff assignment stands, as the destination draws it (contract
/// `CoordinatorAssignment.state`).
enum MarshalAssignmentState {
  dispatching,
  pending,
  running,
  linked,
  failed,
  expired,
  stopped,
}

/// One assignment of the open task: the project it went to, where it stands, and
/// the thread it resolved to.
///
/// It is the Marshal destination's own value rather than the bridge's
/// `CoordinatorAssignment`, for the same reason [MarshalTask] is: `ui/` reads
/// `model/` and never `client/`, and the destination draws only what it needs.
@immutable
class MarshalAssignment {
  const MarshalAssignment({
    required this.id,
    required this.projectId,
    this.projectName = '',
    required this.state,
    this.instruction = '',
    this.threadId,
    this.failure,
    this.reason,
    this.updatedAt,
  });

  /// The assignment's identity: what a scoped retry names.
  final String id;

  /// The target project's canonical id, never a name or a path.
  final String projectId;

  /// What the daemon calls that project. Empty when it named none, and the row
  /// then falls back to the id.
  final String projectName;

  final MarshalAssignmentState state;

  /// What the project was asked to do.
  final String instruction;

  /// The agent whose thread this assignment resolved to: the worker's, or the
  /// linked thread's. Null before either exists.
  final String? threadId;

  /// The machine-readable failure, when the state is failed.
  final String? failure;

  /// The daemon's sentence for a failure, an expiry or a link it could not make.
  final String? reason;

  /// When the daemon last changed this assignment.
  final DateTime? updatedAt;

  /// What the row is called: the project's name, or its id when it has none.
  String get name =>
      projectName.trim().isEmpty ? projectId : projectName.trim();

  @override
  String toString() => 'MarshalAssignment($id, ${state.name})';
}

/// One project's contribution to an attention read.
enum MarshalFreshness { fresh, stale, unavailable, unauthorized }

/// How much one project contributed, and why when it did not.
@immutable
class MarshalSource {
  const MarshalSource({required this.freshness, this.reason, this.observedAt});

  final MarshalFreshness freshness;

  /// The daemon's own sentence for a source that is not fresh; null when it
  /// gave none. It names no path, so it is safe to draw.
  final String? reason;

  /// The newest evidence the source contributed; null when it contributed
  /// none.
  final DateTime? observedAt;

  bool get fresh => freshness == MarshalFreshness.fresh;

  /// What the source line says it is (§8: say what happened).
  String get words => switch (freshness) {
    MarshalFreshness.fresh => 'Read just now',
    MarshalFreshness.stale => 'Read, but its evidence has aged',
    MarshalFreshness.unavailable => 'Could not be read',
    MarshalFreshness.unauthorized => 'Reading it was refused',
  };

  @override
  String toString() => 'MarshalSource(${freshness.name})';
}

/// One task's attention result, as the destination draws it.
///
/// It is a projection of `GET /api/coordinator/tasks/{id}/attention`: the
/// entries to show, per-source freshness, and the one claim the daemon is
/// allowed to make — that it read every source fresh and found nothing.
@immutable
class MarshalRead {
  const MarshalRead({
    required this.entries,
    required this.sources,
    required this.allClear,
    required this.partial,
    required this.observedAt,
  });

  final List<MarshalEntry> entries;
  final List<MarshalSource> sources;

  /// The daemon's own all-clear: every source fresh and nothing waiting.
  final bool allClear;

  /// The read covers less than the task's whole scope.
  final bool partial;

  /// When the daemon read it.
  final DateTime observedAt;

  /// Whether "nothing needs you" may be said. Only a whole, fresh read may say
  /// it — a partial one never can, and the screen says what it could not read
  /// instead.
  bool get clear => allClear && !partial;

  /// The sources this read could not cover, in the daemon's order.
  List<MarshalSource> get limits => [
    for (final source in sources)
      if (!source.fresh) source,
  ];

  /// The entries that open somewhere, and the ones that do not: a row without
  /// a destination is still worth showing, and is drawn without a link.
  Iterable<MarshalEntry> get linked => entries.where((e) => e.place != null);

  @override
  String toString() =>
      'MarshalRead(${entries.length} entries, ${limits.length} limits'
      '${clear ? ', all clear' : ''})';
}

/// The Marshal destination's load state, as §7's states are told apart.
enum MarshalStatus {
  /// The host's tasks are being read for the first time.
  loading,

  /// The host has no coordinated task yet: New opens the first one.
  empty,

  /// The read failed for a reason no retry clears: the proxy refused it.
  unauthorized,

  /// The host could not be reached, or its coordinator store could not be
  /// read.
  unavailable,

  /// The read failed in any other way.
  error,

  /// There is something to draw.
  ready,
}

/// Marshal's destination (agent-first Studio, task 5): the host-local tasks a
/// person can resume, the drafts New opened, and the read-only attention
/// result the conversation answers from.
///
/// It holds no policy of its own: the routes are functions so a test drives
/// every outcome with no socket, and a link is only ever built from a
/// destination the daemon named — never guessed. Everything it does is scoped
/// to one host, because a task is: the same task id on another daemon names a
/// different task.
class MarshalModel extends ChangeNotifier {
  MarshalModel({
    required this.host,
    required Future<CoordinatorTaskList> Function() tasks,
    required Future<CoordinatorAttention> Function(String taskId) attention,
    required Future<CoordinatorCreateResult> Function(CoordinatorCreateRequest)
    create,
    Future<CoordinatorAssignmentList> Function(String taskId)? assignments,
    Future<CoordinatorAssignmentList> Function(
      String taskId,
      String assignmentId,
    )?
    retryAssignment,
    Future<CoordinatorStopResult> Function(String taskId)? stopTask,
    String? Function(String key)? read,
    void Function(String key, String value)? save,
    DateTime Function()? now,
  }) : _tasks = tasks,
       _attention = attention,
       _create = create,
       _assignments = assignments,
       _retryAssignment = retryAssignment,
       _stop = stopTask,
       _read = read,
       _save = save,
       _now = now ?? DateTime.now,
       _handoffStale = false;

  /// The model one host's bridge backs.
  factory MarshalModel.forClient(
    String host,
    BridgeClient client, {
    Future<CoordinatorAssignmentList> Function(String taskId)? assignments,
    Future<CoordinatorAssignmentList> Function(
      String taskId,
      String assignmentId,
    )?
    retryAssignment,
    String? Function(String key)? read,
    void Function(String key, String value)? save,
    DateTime Function()? now,
  }) => MarshalModel(
    host: host,
    tasks: client.coordinatorTasks,
    attention: client.coordinatorAttention,
    create: client.createCoordinatorTask,
    // The assignment read, the scoped retry and the scoped stop are the host's
    // own routes (agent-first Studio, tasks 7-8): wired here so a running Studio
    // draws the handoff band, offers the retry and can stop the task, rather
    // than only the fixtures doing so.
    assignments: assignments ?? client.coordinatorAssignments,
    retryAssignment: retryAssignment ?? client.retryCoordinatorAssignment,
    stopTask: client.stopCoordinatorTask,
    read: read,
    save: save,
    now: now,
  );

  /// A model holding what a fixture hands it, with no route behind it.
  ///
  /// It is the same shape [`PreviewModel.seeded`] and [`StackStore.seeded`]
  /// take: a screen case draws a real state — a task, its answer, its limits,
  /// the assignments it dispatched — without a socket, and every route it does
  /// not need fails loudly rather than answering something invented.
  factory MarshalModel.seeded({
    required String host,
    List<MarshalTask> tasks = const [],
    MarshalTask? task,
    MarshalRead? read,
    DateTime? readAt,
    String? draft,
    List<MarshalAssignment> assignments = const [],
    String? result,
    DateTime? resultAt,
    DateTime? observedAt,
    bool handoffStale = false,
    DateTime Function()? now,
  }) => MarshalModel._seeded(
    host,
    tasks: tasks,
    task: task,
    read: read,
    readAt: readAt,
    draft: draft,
    assignments: assignments,
    result: result,
    resultAt: resultAt,
    observedAt: observedAt,
    handoffStale: handoffStale,
    now: now,
  );

  MarshalModel._seeded(
    this.host, {
    required List<MarshalTask> tasks,
    MarshalTask? task,
    MarshalRead? read,
    DateTime? readAt,
    String? draft,
    List<MarshalAssignment> assignments = const [],
    String? result,
    DateTime? resultAt,
    DateTime? observedAt,
    bool handoffStale = false,
    DateTime Function()? now,
  }) : _tasks = _noTasks,
       _attention = _noAttention,
       _create = _noCreate,
       _assignments = null,
       _retryAssignment = null,
       _stop = null,
       _read = null,
       _save = null,
       _now = now ?? DateTime.now,
       _list = List.unmodifiable(tasks),
       _task = task,
       _draft = draft,
       _attentionRead = read,
       _readAt = readAt,
       _assignmentRows = List.unmodifiable(assignments),
       _resultText = result,
       _resultAt = resultAt,
       _assignmentsAt = observedAt,
       _handoffStale = handoffStale;

  static Future<CoordinatorTaskList> _noTasks() =>
      Future.error(StateError('a seeded MarshalModel reads no host'));

  static Future<CoordinatorAttention> _noAttention(String taskId) =>
      Future.error(StateError('a seeded MarshalModel reads no task'));

  static Future<CoordinatorCreateResult> _noCreate(
    CoordinatorCreateRequest request,
  ) => Future.error(StateError('a seeded MarshalModel creates no task'));

  /// The host this destination reads. A task is host-local, so every link the
  /// screen opens is built for this host.
  final String host;

  /// The preference key the host's last task is remembered under, so the
  /// sidebar entry resumes it. Percent-encoded, as the project memory's keys
  /// are, so one host's name cannot be read as another's.
  static String taskKey(String host) =>
      'marshal.task.${Uri.encodeComponent(host)}';

  final Future<CoordinatorTaskList> Function() _tasks;
  final Future<CoordinatorAttention> Function(String taskId) _attention;
  final Future<CoordinatorCreateResult> Function(CoordinatorCreateRequest)
  _create;

  /// The task's assignments and the one scoped retry, when this build was wired
  /// with those routes (agent-first Studio, task 7). A host that wired neither
  /// leaves the handoff band off rather than drawing an invented one.
  final Future<CoordinatorAssignmentList> Function(String taskId)? _assignments;
  final Future<CoordinatorAssignmentList> Function(
    String taskId,
    String assignmentId,
  )?
  _retryAssignment;

  /// The scoped stop: one task's own work stopped, with the intent made durable
  /// before anything is cancelled (agent-first Studio, task 8). A host this build
  /// wired no route for leaves the stop control out rather than drawing one that
  /// goes nowhere.
  final Future<CoordinatorStopResult> Function(String taskId)? _stop;
  final String? Function(String key)? _read;
  final void Function(String key, String value)? _save;
  final DateTime Function() _now;

  /// One view per destination: the composer's draft, and the transcript's
  /// own choices. Keeping them here — rather than in the screen — is what
  /// makes each view keep its draft when a person switches between two tasks
  /// or two drafts.
  final _views = <String, ThreadViewState>{};

  List<MarshalTask> _list = const [];
  MarshalTask? _task;
  String? _draft;
  MarshalRead? _attentionRead;
  DateTime? _readAt;
  String? _readFailure;
  Object? _readError;
  bool _loading = false;
  bool _starting = false;
  int _drafts = 0;
  bool _disposed = false;

  /// How many attention reads are in flight: [reading] is true while this is
  /// not zero, so a read whose answer arrived too late to keep cannot leave
  /// the screen saying a read still runs.
  int _attendInFlight = 0;

  /// How many handoff reads are in flight, as [_attendInFlight] is for the
  /// attention reads.
  int _assignInFlight = 0;

  /// The answer an attention read may keep: one step up for each read asked.
  /// An answer keeps only while its read is still the newest, so a late
  /// answer for one task can never land under another task's name.
  int _attentionEpoch = 0;

  /// The same bound for the handoff reads, the scoped retries and the stop:
  /// whichever was asked last owns what the handoff draws.
  int _assignmentsEpoch = 0;

  /// The open task's assignments and the one result it reported back into this
  /// thread (agent-first Studio, task 7).
  List<MarshalAssignment> _assignmentRows = const [];
  String? _resultText;
  DateTime? _resultAt;
  DateTime? _assignmentsAt;
  String? _assignmentsFailure;
  String? _retrying;

  /// The stop in flight, and what the last one answered with when it failed: a
  /// stop shows as pending until the daemon answers, and a refusal is the
  /// daemon's own words rather than a task the screen claims is stopped.
  bool _stopping = false;
  String? _stopFailure;

  /// Whether a seeded model's handoff is stale: a fixture's way of drawing the
  /// state a failed read leaves, without a route to fail.
  final bool _handoffStale;

  /// The tasks a person can reopen on this host, newest first.
  List<MarshalTask> get tasks => List.unmodifiable(_list);

  /// The task the destination has open, or null when it is showing the entry
  /// or a draft.
  MarshalTask? get task => _task;

  /// The draft New opened, or null when no draft is open.
  String? get draftId => _draft;

  /// The last attention result, or null when none has been read.
  MarshalRead? get read => _attentionRead;

  /// When [read] was read, or null when nothing has been.
  DateTime? get readAt => _readAt;

  /// What the last read answered with, when it failed: the bridge's words.
  String? get failure => _readFailure;

  /// Whether an attention read is in flight.
  bool get reading => _attendInFlight > 0;

  /// Whether a first send is creating a task.
  bool get starting => _starting;

  /// The open task's assignments, oldest first. Empty for a task that has
  /// dispatched nothing, and for a host this build wired no route for.
  List<MarshalAssignment> get assignments => List.unmodifiable(_assignmentRows);

  /// What the daemon last reported back into the source thread, or null when it
  /// has reported nothing yet.
  String? get resultText => _resultText;

  /// When that result was delivered.
  DateTime? get resultAt => _resultAt;

  /// When the assignments were last observed, or null when they never were.
  DateTime? get assignmentsAt => _assignmentsAt;

  /// What the last assignments read answered with, when it failed: the bridge's
  /// words. The handoff band then says what it shows may be behind.
  String? get assignmentsFailure => _assignmentsFailure;

  /// Whether an assignments read is in flight.
  bool get readingAssignments => _assignInFlight > 0;

  /// The assignment a scoped retry is running for, or null when none is.
  String? get retrying => _retrying;

  /// Whether a scoped stop is in flight: the screen shows the stop as pending
  /// until the daemon answers.
  bool get stopping => _stopping;

  /// What the last stop answered with, when it was refused. The task is not
  /// reported stopped on a refusal — the daemon's own words are.
  String? get stopFailure => _stopFailure;

  /// Whether the open task may be stopped at all: it has a route, it is open or
  /// already stopping, and no stop is in flight.
  bool get canStop {
    final task = _task;
    return _stop != null &&
        task != null &&
        task.state != MarshalTaskState.stopped &&
        !_stopping;
  }

  /// The open task's handoff as its transcript and this destination draw it, or
  /// null when no task is open or no route read one.
  ///
  /// Every link is built from this host and a durable id the daemon minted: the
  /// task is Marshal's own destination on this host, and an assignment's thread
  /// is the agent its record names. Nothing here guesses a destination.
  HandoffView? get handoff {
    final task = _task;
    if (task == null) return null;
    if (_assignmentRows.isEmpty &&
        _resultText == null &&
        _assignmentsFailure == null) {
      return null;
    }
    return handoffViewOf(
      host: host,
      task: task,
      rows: _assignmentRows,
      result: _resultText,
      resultAt: _resultAt,
      observedAt: _assignmentsAt,
      stale: _handoffStale || _assignmentsFailure != null,
    );
  }

  /// Where one wire state stands, in the value the thread model draws.
  static HandoffAssignmentState handoffStateOf(MarshalAssignmentState state) =>
      switch (state) {
        MarshalAssignmentState.dispatching =>
          HandoffAssignmentState.dispatching,
        MarshalAssignmentState.pending => HandoffAssignmentState.pending,
        MarshalAssignmentState.running => HandoffAssignmentState.running,
        MarshalAssignmentState.linked => HandoffAssignmentState.linked,
        MarshalAssignmentState.failed => HandoffAssignmentState.failed,
        MarshalAssignmentState.expired => HandoffAssignmentState.expired,
        MarshalAssignmentState.stopped => HandoffAssignmentState.stopped,
      };

  /// Whether the answer on screen covers less than the task's scope: a source
  /// that was not fresh, a partial read, or a refresh that failed. It is never
  /// an all-clear while this is true.
  bool get limited =>
      _readFailure != null || (_attentionRead?.partial ?? false);

  /// Whether [read] is an answer this build could not refresh: its age is what
  /// a person is reading, and the screen says so.
  bool get cachedOnly => _readFailure != null && _attentionRead != null;

  /// Whether "nothing needs you" may be said: the daemon read every source
  /// fresh, found nothing, **and** this build did not fail to refresh it. A
  /// cached all-clear is not one — the evidence behind it may have changed.
  bool get cleared => (_attentionRead?.clear ?? false) && !limited;

  /// How old the last answer is, as of now, or null when there is none.
  Duration? get cachedAge {
    final at = _readAt;
    if (at == null) return null;
    final age = _now().difference(at);
    return age.isNegative ? Duration.zero : age;
  }

  /// Where the destination stands, in §7's states.
  MarshalStatus get status {
    if (_loading && _list.isEmpty && _task == null && _draft == null) {
      return MarshalStatus.loading;
    }
    if (_readFailure != null && _attentionRead == null) {
      final error = _readError;
      if (error is BridgeFailure) {
        if (error.status == 401 || error.status == 403) {
          return MarshalStatus.unauthorized;
        }
        if (error.status == 503) return MarshalStatus.unavailable;
        return MarshalStatus.error;
      }
      // Nothing a bridge said: the host could not be reached at all.
      return MarshalStatus.unavailable;
    }
    if (_task == null && _draft == null && _list.isEmpty) {
      return MarshalStatus.empty;
    }
    return MarshalStatus.ready;
  }

  /// The view state of one destination: a task's, or a draft's.
  ///
  /// [key] is [viewKeyForTask] or [viewKeyForDraft]; each is created on first
  /// ask and kept until the model is disposed, which is what keeps a draft
  /// while its view is away.
  ThreadViewState viewFor(String key) =>
      _views.putIfAbsent(key, ThreadViewState.new);

  /// The key a task's view is kept under.
  static String viewKeyForTask(String id) => 'task:$id';

  /// The key a draft's view is kept under.
  static String viewKeyForDraft(String id) => 'draft:$id';

  /// The key the destination is drawn under, which is what its transcript and
  /// composer read: the open task's, the open draft's, or the entry's.
  String get viewKey {
    final task = _task;
    if (task != null) return viewKeyForTask(task.id);
    final draft = _draft;
    if (draft != null) return viewKeyForDraft(draft);
    return 'entry';
  }

  /// Mints a draft's own id: New opens a conversation that has no task yet.
  ///
  /// It is the app's id and the daemon never sees it as a task — it becomes
  /// the first send's request id, so an id is minted per draft and never
  /// reused: a reused one would replay whatever task it first created.
  String newDraft() => 'marshal-${_now().millisecondsSinceEpoch}-${++_drafts}';

  /// Reads this host's tasks and opens what the link named: a task, a draft,
  /// or the task the host's person read last.
  ///
  /// [taskId] and [draft] come from the place. The attention result is read
  /// afresh for a task — a cached all-clear is never served as today's answer.
  Future<void> open({String? taskId, String? draft}) async {
    if (draft != null) {
      // The draft is the destination now: the task a person came from keeps
      // its reads to itself, and an answer still in flight cannot land here.
      _leaveTask();
      _adoptTask(null);
      _draft = draft;
      notifyListeners();
      return;
    }
    await load(select: taskId);
  }

  /// Reads this host's task list, restoring [select] or the task remembered for
  /// this host when no task is named.
  Future<void> load({String? select}) async {
    _loading = true;
    _readFailure = null;
    _readError = null;
    // The destination is being read again: a read still in flight belongs to
    // the entry a person is leaving, never to the one that appears.
    _leaveTask();
    notifyListeners();
    try {
      final answer = await _tasks();
      _list = List.unmodifiable([
        for (final task in answer.tasks) MarshalTask.of(task),
      ]);
    } catch (error) {
      _loading = false;
      _readFailure = describeFailure(error);
      _readError = error;
      notifyListeners();
      return;
    }
    _loading = false;
    final id = select ?? _read?.call(taskKey(host));
    final chosen = id == null ? null : _taskNamed(id);
    if (chosen == null && select == null) {
      // The remembered task is gone: the memory names nothing, and the entry
      // is the honest place rather than a link that cannot open.
      _save?.call(taskKey(host), '');
    }
    _draft = null;
    _adoptTask(chosen);
    notifyListeners();
    if (chosen == null) return;
    await _readAttentionFor(chosen);
  }

  /// Opens one task, remembering it as this host's last, and reads its
  /// attention afresh.
  Future<void> selectTask(String id) async {
    final task = _taskNamed(id);
    if (task == null) return;
    // One task's attention and handoff must never read as another's: the
    // reads of the task being left stop applying, and what it kept on screen
    // is set aside until the new task's own reads answer.
    _leaveTask();
    _draft = null;
    _adoptTask(task);
    _save?.call(taskKey(host), task.id);
    notifyListeners();
    await _readAttentionFor(task);
  }

  /// Re-reads the task list and the open task's attention: the next query is a
  /// new read, never the answer the screen already had.
  Future<void> refresh() async {
    final current = _task;
    if (current == null) {
      await load();
      return;
    }
    await _readAttentionFor(current);
  }

  /// The first send of a draft: creates the coordinator task once, under the
  /// draft's own request id, and opens it.
  ///
  /// The daemon's create is idempotent on that id, so a retried first send
  /// answers the task it already made rather than a second one — which is why
  /// the draft is only dropped once the create has answered. A failure keeps
  /// the draft (its view's text included) so nothing typed is lost.
  Future<MarshalTask?> start(String draft, String text) async {
    if (_starting) return null;
    final message = text.trim();
    _starting = true;
    _readFailure = null;
    _readError = null;
    notifyListeners();
    try {
      final result = await _create(
        CoordinatorCreateRequest(requestID: draft, title: titleOf(message)),
      );
      if (_disposed) return null;
      final created = MarshalTask.of(result.task);
      if (!_list.any((task) => task.id == created.id)) {
        _list = List.unmodifiable([created, ..._list]);
      }
      _starting = false;
      // The draft has become a task: the task a person came from keeps its
      // reads to itself, and whatever is still in flight cannot land under
      // the new one's name.
      _leaveTask();
      _adoptTask(created);
      _draft = null;
      _save?.call(taskKey(host), created.id);
      notifyListeners();
      await _readAttentionFor(created);
      return created;
    } catch (error) {
      _starting = false;
      _readFailure = describeFailure(error);
      _readError = error;
      notifyListeners();
      return null;
    }
  }

  /// The title a first message gives its task: its first line, so the recent
  /// list reads as the person wrote it rather than as "Marshal task" repeated.
  static String titleOf(String text) {
    final line = text.split('\n').first.trim();
    if (line.isEmpty) return '';
    return line.length <= 72 ? line : '${line.substring(0, 71)}…';
  }

  /// Bumps both read generations: the destination has moved to another task,
  /// or to the entry or a draft, so a read still in flight for the one a
  /// person left can no longer land.
  void _leaveTask() {
    _attentionEpoch++;
    _assignmentsEpoch++;
  }

  /// Sets the destination's task and leaves the previous task's reads behind:
  /// one task's attention and handoff must never read as another's. Null is
  /// the entry or a draft, which hold no conversation and no reads.
  void _adoptTask(MarshalTask? task) {
    _task = task;
    _attentionRead = null;
    _readAt = null;
    _readFailure = null;
    _readError = null;
    _assignmentRows = const [];
    _resultText = null;
    _resultAt = null;
    _assignmentsAt = null;
    _assignmentsFailure = null;
  }

  MarshalTask? _taskNamed(String id) {
    for (final task in _list) {
      if (task.id == id) return task;
    }
    return null;
  }

  /// Reads one task's attention, and keeps what it found.
  ///
  /// A read that fails keeps the last answer and says the read is limited: an
  /// answer this build could not refresh is never an all-clear, and its age is
  /// what the screen offers instead.
  Future<void> _readAttentionFor(MarshalTask task) async {
    final seq = ++_attentionEpoch;
    _attendInFlight++;
    notifyListeners();
    try {
      try {
        final attention = await _attention(task.id);
        if (_disposed || seq != _attentionEpoch) return;
        _attentionRead = _project(attention);
        _readAt = _now();
        _readFailure = null;
        _readError = null;
      } catch (error) {
        if (_disposed || seq != _attentionEpoch) return;
        _readFailure = describeFailure(error);
        _readError = error;
      }
    } finally {
      _attendInFlight--;
      if (!_disposed && seq == _attentionEpoch) notifyListeners();
    }
    // The handoff this task dispatched, read beside the attention result: the
    // destination draws both, and neither is served from a cache (task 7).
    // A stale read was dropped above, so only the newest read chains.
    if (_disposed || seq != _attentionEpoch) return;
    await _readAssignmentsFor(task);
  }

  /// Reads one task's assignments and the result it reported back, and keeps
  /// what it found.
  ///
  /// A read that fails keeps the last answer and says it may be behind: the
  /// handoff band then draws its age rather than pretending the list is current.
  /// A host this build wired no route for reads nothing, so the band is simply
  /// absent.
  Future<void> _readAssignmentsFor(MarshalTask task) async {
    final read = _assignments;
    if (read == null) return;
    final seq = ++_assignmentsEpoch;
    _assignInFlight++;
    notifyListeners();
    try {
      try {
        final answer = await read(task.id);
        if (_disposed || seq != _assignmentsEpoch) return;
        _applyAssignmentList(answer);
        _assignmentsFailure = null;
        _assignmentsAt = _now();
      } catch (error) {
        if (_disposed || seq != _assignmentsEpoch) return;
        _assignmentsFailure = describeFailure(error);
      }
    } finally {
      _assignInFlight--;
      if (!_disposed && seq == _assignmentsEpoch) notifyListeners();
    }
  }

  /// Keeps one assignment read: the rows, and the result it carried.
  void _applyAssignmentList(CoordinatorAssignmentList list) {
    _assignmentRows = List.unmodifiable([
      for (final assignment in list.assignments) assignmentOf(assignment),
    ]);
    final delivery = list.delivery;
    _resultText = (delivery?.text ?? '').trim().isEmpty
        ? null
        : delivery!.text.trim();
    _resultAt = delivery == null ? null : DateTime.tryParse(delivery.at);
  }

  /// Projects one wire assignment into what the destination draws.
  static MarshalAssignment assignmentOf(CoordinatorAssignment a) {
    final thread = a.worker?.agentID ?? a.thread?.id;
    return MarshalAssignment(
      id: a.id,
      projectId: a.projectID,
      projectName: (a.projectName ?? '').trim(),
      state: switch (a.state) {
        CoordinatorAssignmentState.coordinatorAssignmentPending =>
          MarshalAssignmentState.pending,
        CoordinatorAssignmentState.coordinatorAssignmentRunning =>
          MarshalAssignmentState.running,
        CoordinatorAssignmentState.coordinatorAssignmentLinked =>
          MarshalAssignmentState.linked,
        CoordinatorAssignmentState.coordinatorAssignmentFailed =>
          MarshalAssignmentState.failed,
        CoordinatorAssignmentState.coordinatorAssignmentExpired =>
          MarshalAssignmentState.expired,
        CoordinatorAssignmentState.coordinatorAssignmentStopped =>
          MarshalAssignmentState.stopped,
        _ => MarshalAssignmentState.dispatching,
      },
      instruction: (a.instruction ?? '').trim(),
      threadId: thread,
      failure: a.failure,
      reason: a.reason,
      updatedAt: DateTime.tryParse(a.updatedAt),
    );
  }

  /// Reads the open task's assignments again: what a person's retry, and a
  /// screen's re-read, both start from.
  Future<void> refreshAssignments() async {
    final task = _task;
    if (task == null) return;
    await _readAssignmentsFor(task);
  }

  /// Retries one failed or expired assignment, under its own identity, and
  /// keeps the list the daemon answers with.
  ///
  /// Only the assignment that was named is retried: its siblings keep what they
  /// are doing, which is why this takes an id rather than retrying the task.
  Future<void> retryAssignment(String assignmentId) async {
    final retry = _retryAssignment;
    final task = _task;
    if (retry == null || task == null || _retrying != null) return;
    _retrying = assignmentId;
    // The retry's answer is the settled list (task 7), the newest word on the
    // handoff: a read asked before it does not overwrite what it reports.
    final seq = ++_assignmentsEpoch;
    notifyListeners();
    try {
      try {
        final answer = await retry(task.id, assignmentId);
        if (_disposed || seq != _assignmentsEpoch) return;
        _applyAssignmentList(answer);
        _assignmentsFailure = null;
        _assignmentsAt = _now();
      } catch (error) {
        if (_disposed || seq != _assignmentsEpoch) return;
        _assignmentsFailure = describeFailure(error);
      }
    } finally {
      _retrying = null;
      if (!_disposed && seq == _assignmentsEpoch) notifyListeners();
    }
  }

  /// Stops the open task at the requested scope (agent-first Studio, task 8).
  ///
  /// The daemon makes the stop durable before it cancels the task's own workers,
  /// so this is one request and one answer: while it is in flight the screen shows
  /// the stop as pending, and the answer is what settles it. A stopped task
  /// answers the same state again, so a repeat after a reconnect — or a second
  /// press — is harmless.
  Future<void> stopTask() async {
    final stop = _stop;
    final task = _task;
    if (stop == null ||
        task == null ||
        _stopping ||
        task.state == MarshalTaskState.stopped) {
      return;
    }
    _stopping = true;
    _stopFailure = null;
    // The stop's answer is the settled state (task 8), the newest word on this
    // task's handoff: a read asked before it does not overwrite what it left.
    final seq = ++_assignmentsEpoch;
    notifyListeners();
    try {
      try {
        final answer = await stop(task.id);
        if (_disposed || seq != _assignmentsEpoch) return;
        _applyStopResult(answer);
      } catch (error) {
        if (_disposed || seq != _assignmentsEpoch) return;
        _stopFailure = describeFailure(error);
      }
    } finally {
      _stopping = false;
      if (!_disposed && seq == _assignmentsEpoch) notifyListeners();
    }
  }

  /// Keeps one stop answer: the task as the daemon left it, the assignments it
  /// settled, and the result it reported back into the source thread.
  void _applyStopResult(CoordinatorStopResult answer) {
    final stopped = MarshalTask.of(answer.task);
    _task = stopped;
    _list = List.unmodifiable([
      for (final task in _list) task.id == stopped.id ? stopped : task,
    ]);
    _applyAssignmentList(
      CoordinatorAssignmentList(
        taskID: answer.task.id,
        host: answer.task.host,
        assignments: answer.assignments,
        delivery: answer.delivery,
      ),
    );
    _assignmentsFailure = null;
    _assignmentsAt = _now();
  }

  /// Projects the wire answer into what the destination draws, attaching this
  /// host to every link it builds.
  MarshalRead _project(CoordinatorAttention attention) => MarshalRead(
    entries: List.unmodifiable([
      for (final entry in attention.entries)
        MarshalEntry(
          kind:
              entry.kind == CoordinatorAttentionKind.coordinatorAttentionReview
              ? MarshalEntryKind.review
              : MarshalEntryKind.ask,
          text: entry.text,
          age: Duration(seconds: entry.ageSeconds),
          source: MarshalSourceRef(
            projectId: entry.source.projectID,
            kind: entry.source.kind,
            id: entry.source.id,
          ),
          place: _destinationOf(entry.destination),
        ),
    ]),
    sources: List.unmodifiable([
      for (final source in attention.sources)
        MarshalSource(
          freshness: switch (source.state) {
            CoordinatorSourceState.coordinatorSourceFresh =>
              MarshalFreshness.fresh,
            CoordinatorSourceState.coordinatorSourceStale =>
              MarshalFreshness.stale,
            CoordinatorSourceState.coordinatorSourceUnauthorized =>
              MarshalFreshness.unauthorized,
            _ => MarshalFreshness.unavailable,
          },
          reason: (source.reason ?? '').trim().isEmpty
              ? null
              : source.reason!.trim(),
          observedAt: (source.observedAt ?? 0) <= 0
              ? null
              : DateTime.fromMillisecondsSinceEpoch(
                  source.observedAt!,
                  isUtc: true,
                ),
        ),
    ]),
    allClear: attention.allClear,
    partial: attention.partial,
    observedAt:
        DateTime.tryParse(attention.observedAt) ??
        DateTime.fromMillisecondsSinceEpoch(0, isUtc: true),
  );

  /// The place an entry's destination opens, or null when it names nothing
  /// this build can honour.
  ///
  /// The daemon writes `/thread/<id>`, `/inbox` and `/pulls`. A thread is
  /// qualified with the host that served the read, because the task is
  /// host-local and a host-less link would be resolved against whichever host
  /// answers first — the wrong thread when two hosts hold one id. Everything
  /// else is a place this build has no reason to open from an ask, so it is
  /// dropped rather than guessed at (a bare `parsePlace` would answer the
  /// Overview).
  Place? _destinationOf(String? destination) {
    final path = (destination ?? '').trim();
    if (path.isEmpty) return null;
    final place = parsePlace(path);
    return switch (place.kind) {
      PlaceKind.thread =>
        place.id == null
            ? null
            : Place(PlaceKind.thread, host: host, id: place.id),
      PlaceKind.inbox => const Place.inbox(),
      PlaceKind.pulls => const Place(PlaceKind.pulls),
      _ => null,
    };
  }

  @override
  void dispose() {
    _disposed = true;
    for (final view in _views.values) {
      view.dispose();
    }
    _views.clear();
    super.dispose();
  }

  @override
  void notifyListeners() {
    if (_disposed) return;
    super.notifyListeners();
  }
}

/// Projects one task's assignments and delivered result into what a thread's
/// handoff band draws (agent-first Studio, tasks 7-8).
///
/// Both surfaces that draw it read it here — Marshal's own destination and the
/// thread the work came from — so the two can never describe one handoff
/// differently. Every link is built from [host] and a durable id the daemon
/// minted; nothing here guesses a destination.
HandoffView handoffViewOf({
  required String host,
  required MarshalTask task,
  required List<MarshalAssignment> rows,
  String? result,
  DateTime? resultAt,
  DateTime? observedAt,
  bool stale = false,
}) => HandoffView(
  taskId: task.id,
  taskTitle: task.title,
  task: Place.marshal(host: host, id: task.id),
  rows: [
    for (final row in rows)
      HandoffAssignmentRow(
        id: row.id,
        project: row.projectName,
        state: MarshalModel.handoffStateOf(row.state),
        instruction: row.instruction,
        thread: row.threadId == null || row.threadId!.trim().isEmpty
            ? null
            : Place(PlaceKind.thread, host: host, id: row.threadId),
        failure: row.failure,
        reason: row.reason,
        updatedAt: row.updatedAt,
      ),
  ],
  result: result,
  resultAt: resultAt,
  observedAt: observedAt,
  stale: stale,
);

/// The handoff one project thread is the source of, as that thread's own screen
/// draws it (agent-first Studio, tasks 7-8).
///
/// It reads the host's source view rather than a task's: a thread is the source
/// of at most one open task, and the view is what tells it whether it is one at
/// all. A thread nothing was handed off from reads an empty view, which is a
/// valid answer — the screen then draws no handoff rather than an error.
class SourceHandoffModel extends ChangeNotifier {
  SourceHandoffModel({
    required this.host,
    required this.projectId,
    required this.threadId,
    required Future<CoordinatorSourceView> Function(
      String projectId,
      String threadId,
    )
    read,
    Future<CoordinatorAssignmentList> Function(
      String taskId,
      String assignmentId,
    )?
    retry,
    DateTime Function()? now,
  }) : _read = read,
       _retry = retry,
       _now = now ?? DateTime.now;

  /// The model one project thread's client backs.
  factory SourceHandoffModel.forClient(
    BridgeClient client, {
    required String host,
    required String projectId,
    required String threadId,
    DateTime Function()? now,
  }) => SourceHandoffModel(
    host: host,
    projectId: projectId,
    threadId: threadId,
    read: client.coordinatorSourceView,
    retry: client.retryCoordinatorAssignment,
    now: now,
  );

  /// The host whose daemon holds the task: a task is host-local, so every link
  /// this projects is built for this host.
  final String host;

  /// The target project's canonical daemon id, and the thread's own id: both are
  /// what the source-view route is keyed by.
  final String projectId;
  final String threadId;

  final Future<CoordinatorSourceView> Function(
    String projectId,
    String threadId,
  )
  _read;
  final Future<CoordinatorAssignmentList> Function(
    String taskId,
    String assignmentId,
  )?
  _retry;
  final DateTime Function() _now;

  HandoffView? _handoff;
  String? _failure;
  bool _loading = false;
  bool _disposed = false;
  bool _stale = false;

  /// The answer a read or a retry may keep: one step up for each one asked,
  /// and it keeps only while its ask is still the newest, so a late view can
  /// never bury a retry that was made after it.
  int _viewEpoch = 0;

  /// The newest source view this model read. It is kept so a read that fails can
  /// redraw the same handoff marked as possibly behind, rather than losing it.
  CoordinatorSourceView? _view;

  /// The handoff as the thread draws it, or null when this thread handed nothing
  /// off — and until the first read answers, which is a handoff nobody has said
  /// exists.
  HandoffView? get handoff => _handoff;

  /// What the last read answered with, when it failed: the bridge's own words.
  /// The band then says what it draws may be behind rather than disappearing.
  String? get failure => _failure;

  /// Whether a read is in flight.
  bool get loading => _loading;

  /// Reads the source view. The first read is what a thread place starts; a
  /// later one is what a reconnect or the person's own refresh asks for.
  Future<void> start() => _load();

  /// Reads again: what a resumed screen does once its connection is back.
  Future<void> refresh() => _load();

  Future<void> _load() async {
    if (_loading) return;
    _loading = true;
    // The read starting now is the newest word on this handoff; a retry asked
    // while it runs is newer still, and its answer is the one that stays.
    final seq = ++_viewEpoch;
    notifyListeners();
    try {
      try {
        final view = await _read(projectId, threadId);
        if (_disposed || seq != _viewEpoch) return;
        _apply(view);
        _failure = null;
        _stale = false;
      } catch (error) {
        if (_disposed || seq != _viewEpoch) return;
        _failure = describeFailure(error);
        // A read that failed keeps what it had and says it may be behind: a
        // handoff nobody could re-read is not a handoff that ended.
        final last = _view;
        if (last != null) {
          _stale = true;
          _apply(last);
        }
      }
    } finally {
      _loading = false;
      if (!_disposed && seq == _viewEpoch) notifyListeners();
    }
  }

  /// Keeps one source view: the task, its assignments and the result it
  /// reported back into this thread.
  void _apply(CoordinatorSourceView view) {
    _view = view;
    final task = view.task;
    if (task == null) {
      _handoff = null;
      return;
    }
    final delivery = view.delivery;
    _handoff = handoffViewOf(
      host: host,
      task: MarshalTask.of(task),
      rows: [
        for (final assignment in view.assignments)
          MarshalModel.assignmentOf(assignment),
      ],
      result: (delivery?.text ?? '').trim().isEmpty
          ? null
          : delivery!.text.trim(),
      resultAt: delivery == null ? null : DateTime.tryParse(delivery.at),
      observedAt: _now(),
      stale: _stale,
    );
  }

  /// Retries one failed assignment, under its own identity, and keeps the view
  /// the daemon answers with. Only the assignment that was named is retried.
  Future<void> retryAssignment(String assignmentId) async {
    final retry = _retry;
    final current = _handoff;
    if (retry == null || current == null) return;
    // The retry's answer is the newest word on this handoff (task 7): a read
    // asked before it does not overwrite what it reports.
    final seq = ++_viewEpoch;
    try {
      try {
        final answer = await retry(current.taskId, assignmentId);
        if (_disposed || seq != _viewEpoch) return;
        _apply(_sourceViewAfter(answer));
        _failure = null;
        _stale = false;
      } catch (error) {
        if (_disposed || seq != _viewEpoch) return;
        _failure = describeFailure(error);
      }
    } finally {
      if (!_disposed && seq == _viewEpoch) notifyListeners();
    }
  }

  /// The source view a retried assignment list leaves: the task the source
  /// view named — a retry never moves the task, and building one without its
  /// title would rename it to nothing — with the assignments the retry
  /// answered and the delivery it carried.
  CoordinatorSourceView _sourceViewAfter(CoordinatorAssignmentList answer) {
    final last = _view;
    return CoordinatorSourceView(
      host: last?.host ?? host,
      task: last?.task ?? _taskOf(answer),
      assignments: answer.assignments,
      delivery: answer.delivery,
    );
  }

  /// The task a retried assignment list names, when no source read kept one:
  /// the retry answer carries only the id, so this invents nothing else. It
  /// is the fallback of [_sourceViewAfter], which prefers the task the source
  /// view read — where the handoff's title comes from.
  CoordinatorTask _taskOf(CoordinatorAssignmentList answer) => CoordinatorTask(
    id: answer.taskID,
    sessionID: '',
    status: CoordinatorTaskStatus.coordinatorTaskOpen,
    revision: 1,
    projects: const [],
    createdAt: _now().toIso8601String(),
    updatedAt: _now().toIso8601String(),
  );

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }

  @override
  void notifyListeners() {
    if (_disposed) return;
    super.notifyListeners();
  }
}
