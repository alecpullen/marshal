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
enum MarshalTaskState { open, stopped }

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
    state: task.status == CoordinatorTaskStatus.coordinatorTaskStopped
        ? MarshalTaskState.stopped
        : MarshalTaskState.open,
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
    String? Function(String key)? read,
    void Function(String key, String value)? save,
    DateTime Function()? now,
  }) : _tasks = tasks,
       _attention = attention,
       _create = create,
       _read = read,
       _save = save,
       _now = now ?? DateTime.now;

  /// The model one host's bridge backs.
  factory MarshalModel.forClient(
    String host,
    BridgeClient client, {
    String? Function(String key)? read,
    void Function(String key, String value)? save,
    DateTime Function()? now,
  }) => MarshalModel(
    host: host,
    tasks: client.coordinatorTasks,
    attention: client.coordinatorAttention,
    create: client.createCoordinatorTask,
    read: read,
    save: save,
    now: now,
  );

  /// A model holding what a fixture hands it, with no route behind it.
  ///
  /// It is the same shape [`PreviewModel.seeded`] and [`StackStore.seeded`]
  /// take: a screen case draws a real state — a task, its answer, its limits —
  /// without a socket, and every route it does not need fails loudly rather
  /// than answering something invented.
  factory MarshalModel.seeded({
    required String host,
    List<MarshalTask> tasks = const [],
    MarshalTask? task,
    MarshalRead? read,
    DateTime? readAt,
    String? draft,
    DateTime Function()? now,
  }) => MarshalModel._seeded(
    host,
    tasks: tasks,
    task: task,
    read: read,
    readAt: readAt,
    draft: draft,
    now: now,
  );

  MarshalModel._seeded(
    this.host, {
    required List<MarshalTask> tasks,
    MarshalTask? task,
    MarshalRead? read,
    DateTime? readAt,
    String? draft,
    DateTime Function()? now,
  }) : _tasks = _noTasks,
       _attention = _noAttention,
       _create = _noCreate,
       _read = null,
       _save = null,
       _now = now ?? DateTime.now,
       _list = List.unmodifiable(tasks),
       _task = task,
       _draft = draft,
       _attentionRead = read,
       _readAt = readAt;

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
  bool _reading = false;
  bool _starting = false;
  int _drafts = 0;
  bool _disposed = false;

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

  /// Whether the attention read is in flight.
  bool get reading => _reading;

  /// Whether a first send is creating a task.
  bool get starting => _starting;

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
      _draft = draft;
      _task = null;
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
    _task = chosen;
    if (chosen == null) {
      _attentionRead = null;
      _readAt = null;
      notifyListeners();
      return;
    }
    notifyListeners();
    await _readAttentionFor(chosen);
  }

  /// Opens one task, remembering it as this host's last, and reads its
  /// attention afresh.
  Future<void> selectTask(String id) async {
    final task = _taskNamed(id);
    if (task == null) return;
    _draft = null;
    _task = task;
    _save?.call(taskKey(host), task.id);
    _attentionRead = null;
    _readAt = null;
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
      final created = MarshalTask.of(result.task);
      if (!_list.any((task) => task.id == created.id)) {
        _list = List.unmodifiable([created, ..._list]);
      }
      _starting = false;
      _draft = null;
      _task = created;
      _save?.call(taskKey(host), created.id);
      _attentionRead = null;
      _readAt = null;
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
    _reading = true;
    notifyListeners();
    try {
      final attention = await _attention(task.id);
      if (_disposed) return;
      _attentionRead = _project(attention);
      _readAt = _now();
      _readFailure = null;
      _readError = null;
    } catch (error) {
      if (_disposed) return;
      _readFailure = describeFailure(error);
      _readError = error;
    }
    _reading = false;
    notifyListeners();
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
