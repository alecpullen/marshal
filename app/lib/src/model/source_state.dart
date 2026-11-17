import 'package:flutter/foundation.dart' show immutable;

import '../client/host.dart' show HostReach;
import 'age.dart' show relativeAge;
import 'factory_store.dart' show FactoryStore;
import 'fleet_store.dart' show FleetStore;

/// Which feed a freshness row reports.
enum SourceKind {
  /// The fleet's stream: agents, its queue, and the pull requests in it.
  fleet,

  /// The factory's work items, polled over their own route.
  factory;

  /// The name a person reads for this source.
  String get label => switch (this) {
    SourceKind.fleet => 'Fleet',
    SourceKind.factory => 'Factory',
  };
}

/// One source's freshness for one host: when it last answered, why its last
/// read failed, and whether what is held is a last trustworthy snapshot.
@immutable
class SourceState {
  const SourceState({
    required this.kind,
    required this.host,
    this.reach,
    this.lastRead,
    this.failure,
    this.loaded = false,
  });

  final SourceKind kind;
  final String host;

  /// What the fleet's stream can reach of [host]; null for a source read over
  /// HTTP that has no stream.
  final HostReach? reach;

  /// When this source last answered for [host], or null when it never has.
  final DateTime? lastRead;

  /// Why the last read failed, in the bridge's words; null when it worked.
  final String? failure;

  /// Whether anything was ever read for [host].
  final bool loaded;

  /// The last read worked.
  bool get healthy => loaded && failure == null;

  /// The last read failed and an earlier snapshot is what is held: its
  /// entries stay, labelled with their age.
  bool get stale => loaded && failure != null;

  /// Nothing was ever read, so there is no snapshot: the source says it is
  /// unavailable rather than empty.
  bool get unavailable => !loaded && failure != null;

  /// The source is still making its first attempt.
  bool get connecting => !loaded && failure == null;

  /// What this source's row says, in the design system's words (§8): what is
  /// held, how old it is, and what did not arrive.
  String words(DateTime now) {
    final age = lastRead == null ? 'unknown' : relativeAge(lastRead!, now);
    return switch (this) {
      _ when healthy => '${kind.label}: read $age ago',
      _ when stale =>
        '${kind.label}: $age old, and the last read failed — $failure',
      _ when unavailable =>
        '${kind.label}: unavailable — ${failure ?? 'no answer yet'}',
      _ => '${kind.label}: connecting',
    };
  }

  @override
  String toString() =>
      'SourceState($host/${kind.name}, '
      '${healthy
          ? 'healthy'
          : stale
          ? 'stale'
          : unavailable
          ? 'unavailable'
          : 'connecting'})';
}

/// Every source's freshness, in a stable order: the store's host order, then
/// the fleet before the factory, so the rows do not move between reads.
///
/// A source that is not wired (`factory` null) contributes no row rather than
/// an empty one: it has not failed, it is not there.
List<SourceState> sourceStates(FleetStore store, {FactoryStore? factory}) {
  final states = <SourceState>[];
  for (final host in store.hosts) {
    final contact = host.lastContact;
    states.add(
      SourceState(
        kind: SourceKind.fleet,
        host: host.name,
        reach: host.reach,
        lastRead: contact,
        failure: store.failure(host.name),
        // A host that has answered at least once holds a snapshot, even when
        // that snapshot names no agent.
        loaded: contact != null,
      ),
    );
    if (factory == null) continue;
    states.add(
      SourceState(
        kind: SourceKind.factory,
        host: host.name,
        lastRead: factory.lastReadAt(host.name),
        failure: factory.failure(host.name),
        loaded: factory.loaded(host.name),
      ),
    );
  }
  return List.unmodifiable(states);
}
