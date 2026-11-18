import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';
import '../client/event_stream.dart';
import '../client/host.dart';
import 'fleet_store.dart';
import 'thread_ref.dart';

/// Every pull request the app knows, per host (F5 decision 4).
///
/// It reads each host's index once — `GET /api/pulls` — and then follows that
/// host's fleet stream for `pull` deltas, so a card moves without a refresh.
/// It is the one writer of pull state: a record the bridge answered a dispatch
/// with is applied here too ([apply]), never held beside it.
///
/// A read that lands after a delta does not put the delta's change back: the
/// read is merged with what changed while it was in flight, exactly as
/// [FleetStore] merges a snapshot with its deltas.
class PullsStore extends ChangeNotifier {
  PullsStore(this._hosts, this._fleet) {
    _hosts.addListener(_hostsChanged);
    // The fleet's agents are what a record's thread is read from, so a view
    // that says "the agent is running" must follow the fleet as well as the
    // pull index.
    _fleet.addListener(_fleetChanged);
    _hostsChanged();
  }

  final HostRegistry _hosts;
  final FleetStore _fleet;

  void _fleetChanged() {
    if (_disposed) return;
    notifyListeners();
  }

  final _pulls = <String, _HostPulls>{};
  final _wiring = <String, _StreamWiring>{};
  bool _disposed = false;

  /// The hosts the app reaches, in discovery order: what [all] covers when it
  /// is asked for every host.
  List<String> get hosts =>
      List.unmodifiable([for (final owner in _hosts.hosts) owner.name]);

  /// The records every host reported, or one host's, in id order.
  List<PullRecord> all({String? host}) {
    final records = [
      for (final name in (host != null ? [host] : hosts))
        ...?_pulls[name]?.records.values,
    ]..sort((a, b) => a.id.compareTo(b.id));
    return List.unmodifiable(records);
  }

  /// One record, or null when no host has it.
  PullRecord? byId(String host, String id) => _pulls[host]?.records[id];

  /// The pull requests that came from [thread], newest first.
  ///
  /// An agent's thread is named by the record's origin agent; a work item's by
  /// the factory item it was recorded for (F6). A plan run's records name the
  /// agent that ran it.
  List<PullRecord> forThread(ThreadRef thread) {
    final records =
        [
          for (final record in all(host: thread.host))
            if (_belongsTo(record, thread)) record,
        ]..sort((a, b) {
          final byTime = b.updatedAt.compareTo(a.updatedAt);
          return byTime != 0 ? byTime : a.id.compareTo(b.id);
        });
    return List.unmodifiable(records);
  }

  /// Whether [record] came from [thread].
  static bool _belongsTo(PullRecord record, ThreadRef thread) {
    final id = switch (thread.kind) {
      ThreadKind.agent || ThreadKind.planRun => record.agentID,
      ThreadKind.workItem => record.factoryItem,
      ThreadKind.session => null,
    };
    return id != null && id.isNotEmpty && id == thread.id;
  }

  /// The thread [record] came from, when the fleet still lists it.
  ///
  /// A factory item's record names its item, and F6 is the phase that gives
  /// work items threads; until then it links to nothing rather than to a
  /// guess.
  ThreadRef? threadOf(String host, PullRecord record) {
    final id = record.agentID;
    if (id == null || id.isEmpty) return null;
    for (final agent in _fleet.agents(host: host)) {
      if (agent.id == id) return threadFor(host, agent);
    }
    return null;
  }

  /// Reads one host's index.
  ///
  /// A failed read leaves the records in place: the next delta or the next
  /// reconnect reads them again, and a host being away is not a claim that its
  /// pull requests are gone.
  Future<void> refresh(String host) async {
    final owner = _ownerOf(host);
    if (owner == null) return;
    final state = _state(host);
    final generation = ++state.generation;
    final since = state.deltaSeq;
    try {
      final records = await owner.client.pulls();
      if (_disposed || generation != state.generation) return;
      state.records = _merge(records, state, since);
      notifyListeners();
    } catch (_) {
      // Not a state change: what is held stays held.
    }
  }

  /// The records a read leaves in place: its own, except where a delta landed
  /// while the read was in flight, and except what a delta removed after the
  /// read was asked for.
  static Map<String, PullRecord> _merge(
    List<PullRecord> snapshot,
    _HostPulls state,
    int since,
  ) {
    final changed = <String, PullRecord>{};
    for (final record in snapshot) {
      if ((state.removedAt[record.id] ?? 0) > since) continue;
      final local = state.records[record.id];
      final touched = (state.changedAt[record.id] ?? 0) > since;
      changed[record.id] = touched && local != null ? local : record;
    }
    for (final MapEntry(key: id, value: record) in state.records.entries) {
      if (changed.containsKey(id)) continue;
      if ((state.changedAt[id] ?? 0) > since) changed[id] = record;
    }
    return Map.unmodifiable(changed);
  }

  /// Records one record the bridge answered a dispatch with, as the stream
  /// would have: a dispatch that was accepted shows at once.
  void apply(String host, PullRecord record) {
    if (_disposed) return;
    final state = _state(host);
    state
      ..deltaSeq += 1
      ..records = Map.unmodifiable({...state.records, record.id: record})
      ..changedAt[record.id] = state.deltaSeq
      ..removedAt.remove(record.id);
    notifyListeners();
  }

  /// Follows one host: its index now, its deltas as they arrive, and its index
  /// again whenever the stream says it may have missed something.
  void _follow(Host owner) {
    if (_wiring.containsKey(owner.name)) return;
    final stream = owner.fleet;
    final wiring = _StreamWiring(
      events: stream.events.listen((event) => _applyDelta(owner.name, event)),
      gaps: stream.gaps.listen((_) => unawaited(refresh(owner.name))),
      state: stream.state,
    );
    wiring.listen = () => _followState(owner.name, stream.state.value);
    _wiring[owner.name] = wiring;
    wiring.state.addListener(wiring.listen!);
    unawaited(refresh(owner.name));
  }

  /// A host's stream moved: a live one the store was not following is a host
  /// that has just come back, so its index is read again. A client that has
  /// just reconnected cannot know what it missed.
  void _followState(String host, StreamState now) {
    final wiring = _wiring[host];
    if (wiring == null) return;
    final before = wiring.streamState;
    wiring.streamState = now;
    if (now == StreamState.live && before != StreamState.live) {
      unawaited(refresh(host));
    }
  }

  /// Applies one event from [host]'s stream, when it is a `pull` delta.
  ///
  /// A frame this build cannot read, and a kind it does not know, are ignored:
  /// a newer bridge may send more than this build understands.
  void _applyDelta(String host, StreamEvent event) {
    final Object? decoded;
    try {
      decoded = jsonDecode(event.data);
    } catch (_) {
      return;
    }
    if (decoded is! Map<String, dynamic>) return;
    if (decoded['kind'] != DeltaKind.deltaPull.wire) return;
    final PullDelta delta;
    try {
      delta = PullDelta.fromJson(decoded);
    } catch (_) {
      return;
    }
    final state = _state(host);
    state.deltaSeq += 1;
    final seq = state.deltaSeq;
    final removed = delta.removed;
    if (removed != null && removed.isNotEmpty) {
      state
        ..records = Map.unmodifiable({...state.records}..remove(removed))
        ..changedAt.remove(removed)
        ..removedAt[removed] = seq;
    }
    final record = delta.pull;
    if (record != null) {
      state
        ..records = Map.unmodifiable({...state.records, record.id: record})
        ..changedAt[record.id] = seq
        ..removedAt.remove(record.id);
    }
    notifyListeners();
  }

  /// The registry changed: a host it discovered has its records held and its
  /// stream followed, and a host it has forgotten takes them with it (spec D9).
  void _hostsChanged() {
    var changed = false;
    final listed = {for (final owner in _hosts.hosts) owner.name};
    for (final name in _pulls.keys.toList(growable: false)) {
      if (listed.contains(name)) continue;
      // The wiring goes with the state, so nothing a gone host's bridge still
      // sends can put its records back.
      _wiring.remove(name)?.dispose();
      _pulls.remove(name);
      changed = true;
    }
    for (final owner in _hosts.hosts) {
      _state(owner.name);
      _follow(owner);
    }
    if (changed) notifyListeners();
  }

  /// One host's records, created empty on first use.
  _HostPulls _state(String host) => _pulls.putIfAbsent(host, _HostPulls.new);

  /// The host discovery found, or null for a name nothing reports.
  Host? _ownerOf(String host) {
    for (final owner in _hosts.hosts) {
      if (owner.name == host) return owner;
    }
    return null;
  }

  @override
  void dispose() {
    _disposed = true;
    _hosts.removeListener(_hostsChanged);
    _fleet.removeListener(_fleetChanged);
    for (final wiring in _wiring.values) {
      wiring.dispose();
    }
    _wiring.clear();
    super.dispose();
  }
}

/// What one host has said about its pull requests.
class _HostPulls {
  /// The records by id.
  Map<String, PullRecord> records = const {};

  /// Bumped by each read, so a slow answer cannot overwrite a newer one.
  int generation = 0;

  /// Bumped by every delta applied, and the seq each record changed at: what a
  /// read in flight is compared against.
  int deltaSeq = 0;
  final changedAt = <String, int>{};

  /// When each record was last removed by a delta, so a read that started
  /// before the removal cannot bring it back.
  final removedAt = <String, int>{};
}

/// The listeners one host's stream has in the store.
class _StreamWiring {
  _StreamWiring({required this.events, required this.gaps, required this.state})
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
