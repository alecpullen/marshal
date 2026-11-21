import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/event_stream.dart';
import '../client/host.dart';
import '../client/transport.dart';
import 'age.dart';

/// The one writer of the fleet's state (F2 decision 6).
///
/// Per host it holds what the bridge said: the agents, the projects, the
/// needs-you queue and the budget report, from the snapshot and then from the
/// host's fleet stream. Views read it here and nowhere else; the screens take
/// it as data and never fetch (design-system.md §5).
///
/// A host that is not live keeps its last data. The store exposes the host's
/// reach and its last contact beside it, so a view can mark the rows stale
/// with their age instead of presenting them as current (§7).
class FleetStore extends ChangeNotifier {
  FleetStore(this._registry, {DateTime Function()? now})
    : _now = now ?? DateTime.now {
    _registry.addListener(_hostsChanged);
    _hostsChanged();
  }

  final HostRegistry _registry;
  final DateTime Function() _now;
  final _states = <String, _HostState>{};
  final _wiring = <String, _HostWiring>{};
  bool _disposed = false;

  /// The clock a relative age is read against. Injected so a test (and
  /// `buildNavModel`'s "earlier" rule) can pin it.
  DateTime get now => _now();

  /// The hosts discovery found, in discovery order, with what the app can
  /// reach of each: what the host switcher lists.
  List<HostView> get hosts => List.unmodifiable([
    for (final owner in _registry.hosts)
      HostView(
        name: owner.name,
        kind: owner.kind,
        reach: owner.reach,
        lastContact: owner.lastContact,
        via: owner.via,
        destination: owner.destination,
      ),
  ]);

  /// The agents every host reports, or one host's.
  List<AgentStatus> agents({String? host}) => List.unmodifiable([
    for (final name in _names(host)) ..._state(name).agents,
  ]);

  /// The projects every host knows, or one host's.
  List<ProjectStatus> projects({String? host}) => List.unmodifiable([
    for (final name in _names(host)) ..._state(name).projects,
  ]);

  /// The needs-you queue, oldest first, across every host or on one.
  ///
  /// It is the bridge's own order: the queue is what the bridge sorts, and a
  /// second sort here could disagree with the fleet screen.
  List<InboxItem> inbox({String? host}) => List.unmodifiable([
    for (final name in _names(host)) ..._state(name).queue,
  ]);

  /// Today's spend against the caps on [host], or null before it answered.
  BudgetReport? budgets(String host) => _state(host).budgets;

  /// The sessions every host's index lists, or one host's: each row with the
  /// host it came from and the root it lives under, as the bridge sorted
  /// them.
  List<HostedSession> sessions({String? host}) => List.unmodifiable([
    for (final name in _names(host))
      for (final group in _state(name).sessions)
        for (final row in group.sessions) HostedSession(name, group.root, row),
  ]);

  /// Finds one session by its host and id, or null when no index holds one.
  HostedSession? session(String host, String sessionId) {
    for (final hosted in sessions(host: host)) {
      if (hosted.row.sessionID == sessionId) return hosted;
    }
    return null;
  }

  /// Why [host]'s listing of [root] failed, or null when it listed or the
  /// index holds no group for [root] yet.
  String? sessionsError(String host, String root) {
    for (final group in _state(host).sessions) {
      if (group.root == root) return group.error;
    }
    return null;
  }

  /// What the app can reach of [host] now. A host nothing discovered is
  /// still connecting: nothing has been heard from it yet.
  HostReach reach(String host) => _ownerOf(host)?.reach ?? HostReach.connecting;

  /// When [host] last said anything, or null when it never has.
  DateTime? lastContact(String host) => _ownerOf(host)?.lastContact;

  /// What went wrong the last time [host]'s snapshot was read, or null when
  /// it was read. The rows are kept either way: a failed read is not a reason
  /// to empty them, and it is not a claim that the host is gone.
  String? failure(String host) => _state(host).failure;

  /// The newest coordinator delta [host] sent, or null when it has sent none:
  /// one coordinated task's assignments and the result it reported back
  /// (agent-first Studio, task 7).
  CoordinatorDelta? coordinator(String host) => _state(host).coordinator;

  /// Reads one host's snapshot: its agents, projects, inbox and budgets,
  /// and, beside the budgets, the session index.
  ///
  /// The four are read together so the rows and the queue describe the same
  /// moment. The index is a second, independent read ([_sessions]), so an
  /// older bridge's refusal of it never fails the snapshot.
  ///
  /// A snapshot takes time to arrive and the stream does not wait for it: a
  /// delta that landed while the read was in flight is newer than any answer
  /// to it, so what it changed is kept (see [_agentsFrom]).
  Future<void> refresh(String host) async {
    final owner = _ownerOf(host);
    if (owner == null) return;
    final state = _state(host);
    final generation = ++state.generation;
    final agentsRead = ++state.agentsReads;
    final sessionsRead = ++state.sessionsReads;
    final since = state.deltaSeq;
    try {
      final agents = await owner.client.agents();
      final projects = await owner.client.projects();
      final inbox = await owner.client.inbox();
      final budgets = await _budgets(owner.client, state);
      final sessions = await _sessions(owner.client, state);
      if (_disposed || generation != state.generation) return;
      state
        ..projects = _projectsFrom(projects, state, since)
        ..queue = _queueFrom(inbox.queue, state, since)
        ..budgets = budgets
        ..failure = null;
      // The rows only while no read of them was asked for after this one: a
      // delta that names an unknown agent re-reads them itself (see
      // [_applyInbox]), and that later answer is the newer list. This
      // snapshot, asked for before the delta, must not put the older one back
      // over it — every other route here still applies, since it answers for
      // a different list.
      if (agentsRead == state.agentsReads) {
        state.agents = _agentsFrom(agents, state, since);
      }
      // The same rule for the index: a root read again by a delta while this
      // snapshot was in flight answers for its group afterwards, so this
      // answer — asked for before that read — must not put the older groups
      // back over it.
      if (sessionsRead == state.sessionsReads) {
        state.sessions = _sessionsFrom(sessions, state, null);
      }
      notifyListeners();
    } catch (error) {
      if (_disposed || generation != state.generation) return;
      state.failure = describeFailure(error);
      notifyListeners();
    }
  }

  /// The agents a snapshot leaves in place: every row it carried, except the
  /// ones a delta has changed since the read was asked for, and the ones in a
  /// project that was removed after it.
  static List<AgentStatus> _agentsFrom(
    List<AgentStatus> snapshot,
    _HostState state,
    int since,
  ) {
    final current = {for (final agent in state.agents) agent.id: agent};
    final removed = state.removedAfter(since);
    return List.unmodifiable([
      for (final agent in snapshot)
        if ((state.agentSeq[agent.id] ?? 0) > since &&
            current[agent.id] != null)
          current[agent.id]!
        else if (!removed.contains(agent.project))
          agent,
    ]);
  }

  /// The projects a snapshot leaves in place: its own, unless one was removed
  /// while the read was in flight.
  static List<ProjectStatus> _projectsFrom(
    List<ProjectStatus> snapshot,
    _HostState state,
    int since,
  ) => state.removedSeq > since ? state.projects : List.unmodifiable(snapshot);

  /// The queue a snapshot leaves in place, unless the bridge sent a newer one
  /// while the read was in flight.
  static List<InboxItem> _queueFrom(
    List<InboxItem> snapshot,
    _HostState state,
    int since,
  ) => state.queueSeq > since ? state.queue : List.unmodifiable(snapshot);

  /// The budget report, or the one already held: a bridge that does not know
  /// the route (a server profile of an older build) must not fail the whole
  /// snapshot, and an older reading is better than none.
  Future<BudgetReport?> _budgets(BridgeClient client, _HostState state) async {
    try {
      return await client.budgets();
    } catch (_) {
      return state.budgets;
    }
  }

  /// The session index's groups, or what the store already holds: a bridge
  /// that does not serve the route at all — its 404 and 501, an older build
  /// (a server profile behind this one) — leaves the groups empty, and any
  /// other failure keeps the ones held, as [_budgets] keeps its report.
  /// Neither fails the snapshot.
  Future<List<ProjectSessions>> _sessions(
    BridgeClient client,
    _HostState state,
  ) async {
    try {
      return (await client.sessionIndex()).projects;
    } on BridgeFailure catch (failure) {
      if (failure.status == 404 || failure.status == 501) return const [];
      return state.sessions;
    } catch (_) {
      return state.sessions;
    }
  }

  /// The host's state, created empty on first use.
  _HostState _state(String host) => _states.putIfAbsent(host, _HostState.new);

  /// The host discovery found, or null for a name nothing reports.
  Host? _ownerOf(String host) {
    for (final owner in _registry.hosts) {
      if (owner.name == host) return owner;
    }
    return null;
  }

  /// The hosts a read covers, in discovery order.
  Iterable<String> _names(String? host) =>
      host != null ? [host] : [for (final owner in _registry.hosts) owner.name];

  /// The registry changed: a host it discovered has its state held and its
  /// stream followed, and a host it has forgotten — its profile removed, its
  /// daemon uninstalled — takes everything held for it with it, exactly as the
  /// registry drops the host itself (spec D9).
  void _hostsChanged() {
    var changed = false;
    final listed = {for (final owner in _registry.hosts) owner.name};
    for (final name in _states.keys.toList(growable: false)) {
      if (listed.contains(name)) continue;
      // The wiring goes first: a delta the bridge still sends for a host that
      // is gone must not reach the store, or it would re-create the state this
      // drops, and rows nothing can update again would come back.
      _wiring.remove(name)?.dispose();
      _states.remove(name);
      changed = true;
    }
    for (final owner in _registry.hosts) {
      if (!_states.containsKey(owner.name)) {
        _states[owner.name] = _HostState();
        changed = true;
      }
      _follow(owner);
    }
    if (changed) notifyListeners();
  }

  /// Follows one host's fleet stream: its deltas, its replay gaps, and the
  /// moment its connection comes back.
  ///
  /// The snapshot is read when the store starts following the host, and again
  /// whenever the stream reaches it after a break: a client that has just
  /// reconnected cannot know what it missed, and the replay gap says outright
  /// that it cannot be replayed (F1 decision 7).
  void _follow(Host owner) {
    if (_wiring.containsKey(owner.name)) return;
    final stream = owner.fleet;
    final wiring = _HostWiring(
      events: stream.events.listen((event) {
        _applyDelta(owner.name, event);
      }),
      gaps: stream.gaps.listen((_) {
        unawaited(refresh(owner.name));
      }),
      state: stream.state,
    );
    wiring.listen = () => _followState(owner.name, stream.state.value);
    _wiring[owner.name] = wiring;
    wiring.state.addListener(wiring.listen!);
    unawaited(refresh(owner.name));
  }

  /// A host's stream moved: a live stream the store was not following is a
  /// host that has just come back, so its rows are read again.
  void _followState(String host, StreamState now) {
    final wiring = _wiring[host];
    if (wiring == null) return;
    final before = wiring.streamState;
    wiring.streamState = now;
    if (now == StreamState.live && before != StreamState.live) {
      unawaited(refresh(host));
    }
  }

  /// Applies one event from [host]'s stream.
  ///
  /// A delta whose kind this build does not know, and a frame that does not
  /// parse, are ignored: a newer bridge may send more than this build
  /// understands, and that is not a reason to empty the screen.
  void _applyDelta(String host, StreamEvent event) {
    final Object? decoded;
    try {
      decoded = jsonDecode(event.data);
    } catch (_) {
      return;
    }
    if (decoded is! Map<String, dynamic>) return;
    final kind = decoded['kind'];
    if (kind is! String) return;
    final state = _state(host);
    try {
      switch (kind) {
        case 'inbox':
          _applyInbox(host, state, InboxDelta.fromJson(decoded));
        case 'budget':
          // A budget delta says which way the spend moved, not the day's
          // whole report: read the report again, as the Svelte store's views
          // do, rather than inventing the caps from one delta.
          unawaited(_refreshBudgets(host));
        case 'project_removed':
          _applyProjectRemoved(state, ProjectRemovedDelta.fromJson(decoded));
        case 'coordinator':
          // A coordinated task's assignments and the result it reported back
          // (agent-first Studio, task 7). They are not agent rows: the newest
          // delta is kept for the surfaces that draw a handoff, which read the
          // task's snapshot and follow this for live changes.
          state
            ..coordinator = CoordinatorDelta.fromJson(decoded)
            ..coordinatorSeq += 1;
        case 'sessions':
          // A project's sessions changed and the delta carries no rows: the
          // index is the account of them, so its root is read again rather
          // than patched from a frame that names only that.
          unawaited(
            refreshSessions(
              host,
              root: SessionsDelta.fromJson(decoded).project,
            ),
          );
        default:
          _applyFleetDelta(host, state, FleetDelta.fromJson(decoded));
      }
    } catch (_) {
      // A frame this build cannot read is not a state change.
      return;
    }
    notifyListeners();
  }

  /// The in-turn deltas: what one agent's session reports as it works.
  void _applyFleetDelta(String host, _HostState state, FleetDelta delta) {
    final index = state.agents.indexWhere(
      (agent) => agent.id == delta.sessionID,
    );
    if (index < 0) return;
    final agent = state.agents[index];
    final AgentStatus next;
    switch (delta.kind) {
      case DeltaKind.deltaActivity:
        next = _patched(agent, {'activity': delta.activity});
      case DeltaKind.deltaMode:
        final access = delta.access;
        if (access == null) return;
        next = _patched(agent, {'access': access.toJson()});
      case DeltaKind.deltaTelemetry:
        next = _patched(agent, {
          if (delta.contextPct != null) 'contextPct': delta.contextPct,
          if (delta.changedFiles != null) 'changedFiles': delta.changedFiles,
        });
      case DeltaKind.deltaHold:
        next = _patched(agent, {'held': delta.held ?? false});
      case DeltaKind.deltaPending:
        final kind = delta.pendingKind;
        if (kind == null) return;
        next = _patched(agent, {
          'status': kind == PendingKind.pendingApproval
              ? AgentState.agentAwaitingApproval.wire
              : AgentState.agentAwaitingQuestion.wire,
        });
        // The delta carries only the kind: the request itself — the command
        // or the question — is the snapshot's to give (F1's contract, F2
        // decision 7), so read the rows again and let the snapshot answer.
        unawaited(_refreshAgents(host, from: {agent.id}));
      case DeltaKind.deltaGate:
      case DeltaKind.deltaRun:
        // The gate record and the run detail have no place in `AgentStatus`
        // yet: the thread's gate and run screens arrive in F3. Accepted and
        // set aside, never guessed at.
        return;
      case DeltaKind.deltaWatch:
      case DeltaKind.deltaBudget:
      case DeltaKind.deltaProjectRemoved:
      case DeltaKind.deltaInbox:
      case DeltaKind.deltaSessions:
      case DeltaKind.deltaCoordinator:
        // Handled elsewhere, or a kind this build does not know. A project's
        // sessions changed (P7 D9): its index is read in `_applyDelta`, and
        // no agent row changes here. A coordinator delta is recorded in
        // `_applyDelta` for the handoff surfaces, never in this agent list.
        return;
      case DeltaKind.deltaPull:
        // Pull requests are not agent rows: `PullsStore` follows the same
        // stream for them, and this store has nothing to say about one.
        return;
      case DeltaKind.unknown:
        return;
    }
    state
      ..deltaSeq += 1
      ..agents = List.unmodifiable([
        for (var i = 0; i < state.agents.length; i++)
          i == index ? next : state.agents[i],
      ]);
    state.agentSeq[agent.id] = state.deltaSeq;
  }

  /// The bridge's whole queue, which it recomputes and re-sends as it moves.
  ///
  /// An inbox that names an agent the rows do not have is newer than the
  /// snapshot behind them — the bridge spawned it mid-session — so the agents
  /// are read again. Without that the ask's row never arrives, and
  /// `InboxModel` cannot render a queue entry whose agent it does not know:
  /// the work waits invisibly. It is `web/ui/src/lib/fleet.ts`'s
  /// `refetchAgentsFor`, kept to one read at a time here (see
  /// [_refreshAgents]), which coalesces a burst of deltas rather than
  /// throttling by the clock.
  void _applyInbox(String host, _HostState state, InboxDelta delta) {
    state
      ..deltaSeq += 1
      ..queueSeq = state.deltaSeq
      ..queue = List.unmodifiable(delta.inbox.queue);
    final known = {for (final agent in state.agents) agent.id};
    final named = [
      ...delta.inbox.groups.needsYou,
      ...delta.inbox.groups.running,
      ...delta.inbox.groups.ready,
      ...delta.inbox.groups.earlier,
      for (final item in delta.inbox.queue)
        if (item.kind == InboxItemKind.inboxItemAgent) item.agentID,
    ];
    if (named.any((id) => !known.contains(id))) {
      unawaited(_refreshAgents(host));
    }
  }

  /// A project left the daemon: it and every agent in it are gone from the
  /// view, rather than left behind as rows that can never update again.
  void _applyProjectRemoved(_HostState state, ProjectRemovedDelta delta) {
    state
      ..deltaSeq += 1
      ..removedSeq = state.deltaSeq
      ..removedProjects.add(delta.project)
      ..projects = List.unmodifiable([
        for (final project in state.projects)
          if (project.root != delta.project) project,
      ])
      ..agents = List.unmodifiable([
        for (final agent in state.agents)
          if (agent.project != delta.project) agent,
      ]);
  }

  /// One agent, with some of its fields replaced. The generated contract
  /// round-trips through JSON, so a field this build does not know survives
  /// the patch (an unknown enum keeps its wire text). A null in [changes]
  /// means "nothing new said", so the field keeps its value.
  static AgentStatus _patched(
    AgentStatus agent,
    Map<String, Object?> changes,
  ) => AgentStatus.fromJson({
    ...agent.toJson(),
    for (final MapEntry(:key, :value) in changes.entries) key: ?value,
  });

  /// Reads one host's agents again, without touching the projects or the
  /// queue.
  ///
  /// [from] names the agents the snapshot must answer for whatever the stream
  /// said meanwhile: a pending delta carries only the kind, so the snapshot is
  /// the authority for the request itself.
  ///
  /// One read runs at a time per host. A request that arrives while one is in
  /// flight is not dropped: its [from] ids are carried into a follow-up read
  /// when the first finishes, so an agent that parks during a read still gets
  /// its request, and a burst of deltas costs at most two reads.
  Future<void> _refreshAgents(
    String host, {
    Set<String> from = const {},
  }) async {
    final owner = _ownerOf(host);
    if (owner == null) return;
    final state = _state(host);
    if (state.agentsRefetching) {
      state
        ..agentsRefetchFrom.addAll(from)
        ..agentsRefetchPending = true;
      return;
    }
    state.agentsRefetching = true;
    // What this read must answer for, plus whatever a coalesced request adds
    // before the next pass.
    var answerFor = from;
    try {
      do {
        state.agentsRefetchPending = false;
        answerFor = {...answerFor, ...state.agentsRefetchFrom};
        state.agentsRefetchFrom.clear();
        await _readAgents(owner, state, answerFor);
        if (_disposed) return;
      } while (state.agentsRefetchPending);
    } finally {
      state
        ..agentsRefetching = false
        ..agentsRefetchPending = false
        ..agentsRefetchFrom.clear();
    }
  }

  /// One pass of [_refreshAgents]: the snapshot, with the rows a delta has
  /// changed since the read was asked for kept in place.
  Future<void> _readAgents(
    Host owner,
    _HostState state,
    Set<String> from,
  ) async {
    final since = state.deltaSeq;
    final read = ++state.agentsReads;
    try {
      final agents = await owner.client.agents();
      if (_disposed) return;
      // A read of the rows asked for after this one owns the list: this
      // answer is the older one, so it is dropped rather than put back over
      // it (a snapshot refresh can be in flight while the delta re-reads).
      if (read != state.agentsReads) return;
      final current = {for (final agent in state.agents) agent.id: agent};
      final removed = state.removedAfter(since);
      state.agents = List.unmodifiable([
        for (final agent in agents)
          if (!from.contains(agent.id) &&
              (state.agentSeq[agent.id] ?? 0) > since &&
              current[agent.id] != null)
            current[agent.id]!
          else if (!removed.contains(agent.project))
            agent,
      ]);
      notifyListeners();
    } catch (_) {
      // A failed re-read leaves the rows as they were; the next delta or the
      // next reconnect reads them again.
    }
  }

  /// Reads one host's session index again, without touching the other rows.
  ///
  /// [root] narrows the read to one project's group — what a `sessions` delta
  /// names — and a read with no root replaces them all.
  ///
  /// One read runs at a time per host. A request that arrives while one is in
  /// flight is not dropped: its root is carried into a follow-up read when
  /// the first finishes, so a burst of deltas costs at most two reads. The
  /// follow-up accumulates roots, not ids, and a null among them — a read of
  /// them all — means it reads them all.
  Future<void> refreshSessions(String host, {String? root}) async {
    final owner = _ownerOf(host);
    if (owner == null) return;
    final state = _state(host);
    if (state.sessionsRefetching) {
      state
        ..sessionsRefetchRoots.add(root)
        ..sessionsRefetchPending = true;
      return;
    }
    state.sessionsRefetching = true;
    // What this read must answer for, plus whatever a coalesced request adds
    // before the next pass.
    var answerFor = <String?>{root};
    try {
      do {
        state.sessionsRefetchPending = false;
        answerFor = {...answerFor, ...state.sessionsRefetchRoots};
        state.sessionsRefetchRoots.clear();
        await _readSessions(owner, state, answerFor);
        if (_disposed) return;
      } while (state.sessionsRefetchPending);
    } finally {
      state
        ..sessionsRefetching = false
        ..sessionsRefetchPending = false
        ..sessionsRefetchRoots.clear();
    }
  }

  /// One pass of [refreshSessions]: one read, one root — or all of them —
  /// applied at once.
  Future<void> _readSessions(
    Host owner,
    _HostState state,
    Set<String?> roots,
  ) async {
    final read = ++state.sessionsReads;
    try {
      // A single root is the rooted read a delta asked for; several
      // coalesced roots, or a read of them all in the set, answer for every
      // project, which is this route's other mode.
      final root = roots.contains(null) || roots.length > 1
          ? null
          : roots.single;
      final index = await owner.client.sessionIndex(root: root);
      if (_disposed || read != state.sessionsReads) return;
      state.sessions = _sessionsFrom(index.projects, state, root);
      notifyListeners();
    } catch (_) {
      // A failed re-read leaves the groups as they were; the next delta or
      // the next reconnect reads them again.
    }
  }

  /// The groups one index answer leaves in place. A rooted read answers for
  /// its root only: that root's group is replaced — or added when the store
  /// holds none yet, or removed when the answer has none for it — and the
  /// other roots' groups are kept as they were. A read with no root replaces
  /// them all.
  static List<ProjectSessions> _sessionsFrom(
    List<ProjectSessions> answer,
    _HostState state,
    String? root,
  ) {
    if (root == null) return List.unmodifiable(answer);
    ProjectSessions? answered;
    for (final group in answer) {
      if (group.root == root) answered = group;
    }
    return List.unmodifiable([
      for (final held in state.sessions)
        if (held.root != root) held else ?answered,
      if (answered != null && !state.sessions.any((held) => held.root == root))
        answered,
    ]);
  }

  /// Reads one host's budget report again.
  ///
  /// A delta says which way the spend moved, and the report is read rather
  /// than invented from it. The answer is only the current spend if nothing
  /// newer was read while it was in flight: a slow `GET /api/budgets` that
  /// lands after a snapshot (or after a later budget read) would put older
  /// numbers back, so it is discarded.
  Future<void> _refreshBudgets(String host) async {
    final owner = _ownerOf(host);
    if (owner == null) return;
    final state = _state(host);
    final generation = state.generation;
    final read = ++state.budgetReads;
    final report = await _budgets(owner.client, state);
    if (_disposed || generation != state.generation) return;
    if (read != state.budgetReads) return;
    if (identical(report, state.budgets)) return;
    state.budgets = report;
    notifyListeners();
  }

  @override
  void dispose() {
    _disposed = true;
    _registry.removeListener(_hostsChanged);
    for (final wiring in _wiring.values) {
      wiring.dispose();
    }
    _wiring.clear();
    super.dispose();
  }
}

/// What one host's bridge has said. Every field starts empty: a host known
/// but not yet read has no rows, which a view shows as loading rather than as
/// an empty fleet.
class _HostState {
  List<AgentStatus> agents = const [];
  List<ProjectStatus> projects = const [];
  List<InboxItem> queue = const [];

  /// The host's session groups, empty until read: every project's sessions
  /// as the index sorted them, held until a later read replaces them.
  List<ProjectSessions> sessions = const [];

  BudgetReport? budgets;
  String? failure;

  /// The newest coordinator delta [host] sent: one task's assignments and the
  /// result it reported back (agent-first Studio, task 7). Null until the host
  /// sends one, and kept as the last one seen — the handoff surfaces read the
  /// task's snapshot and follow this for live changes.
  CoordinatorDelta? coordinator;

  /// Bumped by each coordinator delta, so a surface can tell a newer one from
  /// the same one seen again.
  int coordinatorSeq = 0;

  /// Bumped by each read, so a slow answer cannot overwrite a newer one.
  int generation = 0;

  /// Bumped by each budget read, so a slower earlier read cannot overwrite a
  /// newer report.
  int budgetReads = 0;

  /// Bumped by each read of the agent rows. Two reads feed that one list — the
  /// snapshot refresh and a delta's re-read — so the answer asked for last is
  /// the one that stands, however late an earlier answer arrives.
  int agentsReads = 0;

  /// Bumped by each read of the session index. Two reads feed that one list —
  /// the snapshot's and a delta's rooted re-read — so the answer asked for
  /// last is the one that stands, however late an earlier answer arrives.
  int sessionsReads = 0;

  /// A pending delta's re-read is in flight.
  bool agentsRefetching = false;

  /// Another re-read was asked for while one was in flight, and runs when it
  /// finishes.
  bool agentsRefetchPending = false;

  /// The agents the next pass must answer for: a coalesced request's ids.
  final agentsRefetchFrom = <String>{};

  /// A sessions delta's re-read is in flight.
  bool sessionsRefetching = false;

  /// Another re-read was asked for while one was in flight, and runs when it
  /// finishes.
  bool sessionsRefetchPending = false;

  /// The roots the next pass must answer for: a coalesced request's, with a
  /// null among them naming all of them.
  final sessionsRefetchRoots = <String?>{};

  /// Bumped by every delta applied to this host: the clock a snapshot is
  /// compared against, so an answer asked for before a delta cannot put back
  /// what the delta changed.
  int deltaSeq = 0;

  /// When each agent last changed by a delta.
  final agentSeq = <String, int>{};

  /// When the queue last changed by a delta.
  int queueSeq = 0;

  /// The projects removed by a delta, and when the last one was.
  final removedProjects = <String>{};
  int removedSeq = 0;

  /// The projects a delta removed after [since].
  Set<String> removedAfter(int since) =>
      removedSeq > since ? removedProjects : const {};
}

/// The listeners one host's stream has in the store.
class _HostWiring {
  _HostWiring({required this.events, required this.gaps, required this.state})
    : streamState = state.value;

  final StreamSubscription<StreamEvent> events;
  final StreamSubscription<void> gaps;
  final ValueListenable<StreamState> state;

  /// The store's listener on [state], kept so it can be taken off again.
  VoidCallback? listen;

  /// The stream's state as of the last notification.
  StreamState streamState;

  void dispose() {
    events.cancel();
    gaps.cancel();
    final listen = this.listen;
    if (listen == null) return;
    state.removeListener(listen);
  }
}

/// What went wrong, in plain words (design-system.md §7).
///
/// The bridge answers `{"error": "…"}`; a screen says the words, not the JSON
/// they are wrapped in. A [BridgeFailure] keeps its own message, and any other
/// error its `toString()` — except the two a decode raises, [TypeError] and
/// [FormatException]: those say nothing a reader can act on, and usually mean
/// this Studio read the bridge's answer as a shape it does not have. They
/// become one sentence saying so.
String describeFailure(Object error) {
  if (error is TypeError || error is FormatException) {
    return 'The bridge sent an answer this Studio cannot read. The bridge '
        'and the Studio may be different versions.';
  }
  if (error is! BridgeFailure) return error.toString();
  final message = error.message;
  try {
    final decoded = jsonDecode(message);
    if (decoded is Map<String, dynamic>) {
      final detail = decoded['error'];
      if (detail is String && detail.trim().isNotEmpty) return detail.trim();
    }
  } catch (_) {
    // Not JSON: the body is the message.
  }
  return message;
}

/// One session row and where it lives.
@immutable
class HostedSession {
  const HostedSession(this.host, this.root, this.row);
  final String host;
  final String root;
  final SessionRow row;
}

/// A host's tunnel as the local daemon last reported it.
@immutable
class HostTunnel {
  const HostTunnel({required this.state, this.lastError = '', this.since});

  /// `connecting`, `live` or `down`.
  final String state;

  /// What the ssh that carries the tunnel last said, or ''.
  final String lastError;

  /// When [state] last changed, or null when nothing reported one.
  final DateTime? since;
}

/// One host as a view lists it: its name, which kind of bridge it is, and
/// what the app can reach of it.
///
/// It copies the few fields a view draws rather than exposing the client's
/// [Host], so a widget never holds the object that owns the stream.
@immutable
class HostView {
  const HostView({
    required this.name,
    required this.kind,
    required this.reach,
    required this.lastContact,
    this.via,
    this.destination,
    this.tunnel,
  });

  final String name;

  /// A local daemon, or a server profile.
  final HostKind kind;

  /// The daemon this host is reached through, or null.
  final String? via;

  /// The SSH destination this host is reached over, or null.
  final String? destination;

  /// The SSH tunnel the local daemon keeps for this host, or null when
  /// nothing reported one.
  final HostTunnel? tunnel;

  /// Live, stale, unreachable or unauthorized.
  final HostReach reach;

  /// When it was last heard from, or null when it never has been.
  final DateTime? lastContact;

  /// This host with [tunnel] in place of the one it has, every other field
  /// unchanged.
  HostView withTunnel(HostTunnel? tunnel) => HostView(
    name: name,
    kind: kind,
    reach: reach,
    lastContact: lastContact,
    via: via,
    destination: destination,
    tunnel: tunnel,
  );

  /// Whether the host is answering now.
  bool get live => reach.name == 'live';

  /// Whether the operator's proxy refused the request for it. A refusal is not
  /// a silence: a screen names the host and says the page is signed out.
  bool get refused => reach.name == 'unauthorized';

  /// The reach in the design system's words, with its age when the host is
  /// not answering: "live", "stale 4m", "unreachable 4m", "signed out".
  /// A screen shows this rather than colouring a dot it cannot explain.
  ///
  /// A tunnel is checked first: it says what the ssh that carries this host
  /// is doing, which is why the host is unreachable when it is down, and the
  /// age is the tunnel's own since it changed state.
  String reachWords(DateTime now) => switch (tunnel) {
    HostTunnel(state: 'down') => 'down ${_tunnelAge(now)}',
    HostTunnel(state: 'connecting') => 'connecting',
    _ => _reachWords(now),
  };

  /// How long the tunnel has been in its state, measured from its `since`.
  String _tunnelAge(DateTime now) =>
      relativeAge(tunnel?.since ?? lastContact ?? now, now);

  String _reachWords(DateTime now) => switch (reach.name) {
    'live' => 'live',
    'stale' => 'stale ${relativeAge(lastContact ?? now, now)}',
    'unreachable' => 'unreachable ${relativeAge(lastContact ?? now, now)}',
    'unauthorized' => 'signed out',
    _ => 'connecting',
  };

  @override
  String toString() => 'HostView($name, ${reach.name})';
}
