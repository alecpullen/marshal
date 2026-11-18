import 'dart:async';

import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/host.dart';
import 'inbox_model.dart';
import 'watches_model.dart';

/// Every host's tripped watches, blocked requests and held drafts (spec D4).
///
/// The bridge has no stream for any of the three, so the store reads them: once
/// when a host is added, then every [interval]. Each route is read through its
/// own host's client, and a failure is kept apart — a watches read that fails
/// leaves that host's last tripped watches in place and still hands over the
/// blocks and drafts the other two routes answered, and a hand-over that fails
/// leaves the inbox holding what it last read.
///
/// A host that never answered contributes nothing: it appears in no row and no
/// ask until its first read succeeds.
class SignalsStore extends ChangeNotifier {
  SignalsStore(
    this._hosts, {
    required this.inbox,
    required this.now,
    this.interval = const Duration(seconds: 15),
  }) {
    _hosts.addListener(_hostsChanged);
    _hostsChanged();
  }

  final HostRegistry _hosts;

  /// The inbox each host's blocks and drafts are handed to.
  final InboxModel inbox;

  /// What a trip's age is measured against.
  final DateTime Function() now;

  /// How often every host is read again.
  final Duration interval;

  /// The last watch list each host reported, by host name. A failed read keeps
  /// the last list rather than dropping that host's rows.
  final _watches = <String, List<WatchInfo>>{};

  /// The hosts a read is in flight for, and the hosts a refresh was asked for
  /// meanwhile: that refresh runs once, after the read in flight completes.
  final _inFlight = <String>{};
  final _again = <String>{};

  /// The hosts already followed, so a name the registry reports again is not
  /// read a second time.
  final _known = <String>{};

  Timer? _timer;
  bool _started = false;
  bool _disposed = false;

  /// Every host's tripped watches, newest first.
  ///
  /// Because a [TrippedWatch] carries no timestamp, the raw rows are merged
  /// across hosts and sorted before they become rows.
  List<TrippedWatch> get tripped {
    final rows = <(String host, WatchInfo watch)>[];
    for (final entry in _watches.entries) {
      for (final watch in entry.value) {
        if (watchTripped(watch)) rows.add((entry.key, watch));
      }
    }
    rows.sort((a, b) {
      final byTime = (b.$2.lastFiredAt ?? 0).compareTo(a.$2.lastFiredAt ?? 0);
      return byTime != 0 ? byTime : _sortKey(a).compareTo(_sortKey(b));
    });
    final now = this.now();
    return List.unmodifiable([
      for (final row in rows) trippedWatchOf(row.$2, host: row.$1, now: now),
    ]);
  }

  static String _sortKey((String host, WatchInfo watch) row) =>
      '${row.$1}|${row.$2.agentID}|${row.$2.id}';

  /// Whether more than one host is known, so a row names its host.
  bool get manyHosts => _hosts.hosts.length > 1;

  /// Starts reading every host at once, then again every [interval]. It is
  /// idempotent: a second call replaces the timer rather than adding one.
  void start() {
    _started = true;
    _schedule(Duration.zero);
  }

  /// Stops the timer. A read in flight finishes and is kept.
  void stop() {
    _timer?.cancel();
    _timer = null;
  }

  /// Re-reads [host] now, or once after the read in flight.
  Future<void> refresh(String host) async {
    if (_disposed) return;
    if (_inFlight.contains(host)) {
      _again.add(host);
      return;
    }
    _inFlight.add(host);
    try {
      await _read(host);
    } finally {
      _inFlight.remove(host);
    }
    if (_disposed) return;
    if (_again.remove(host)) {
      await refresh(host);
    }
  }

  /// Reads [host]'s three routes. Each keeps its own failure: a route that
  /// fails leaves that host's last value in place rather than handing an empty
  /// one over.
  Future<void> _read(String host) async {
    final client = _clientOf(host);
    if (client == null) return;

    try {
      final watches = await client.watches();
      _watches[host] = List.unmodifiable(watches);
    } catch (_) {
      // Keep the last tripped watches this host reported.
    }

    try {
      final blocks = await client.networkPending();
      inbox.showPendingBlocks(host, blocks);
    } catch (_) {
      // Keep the inbox's last hand-over for this host.
    }

    try {
      final drafts = await client.reviewDrafts(status: 'draft');
      inbox.showHeldDrafts(host, drafts);
    } catch (_) {
      // Keep the inbox's last hand-over for this host.
    }

    if (_disposed) return;
    notifyListeners();
  }

  /// The next read, timed from now.
  void _schedule(Duration delay) {
    _timer?.cancel();
    if (_disposed) return;
    _timer = Timer(delay, () {
      if (_disposed) return;
      // The cycle is timed from this tick, not from the reads it starts: one
      // host's slow bridge must not hold every other host's next read back.
      // The per-host guard in [refresh] already keeps a host from overlapping
      // itself, so a read still in flight is coalesced rather than doubled.
      unawaited(_readEveryHost());
      _schedule(interval);
    });
  }

  /// Reads every host at once, each through its own coalescing [refresh].
  Future<void> _readEveryHost() async {
    await Future.wait([for (final owner in _hosts.hosts) refresh(owner.name)]);
  }

  BridgeClient? _clientOf(String host) {
    for (final owner in _hosts.hosts) {
      if (owner.name == host) return owner.client;
    }
    return null;
  }

  /// Follows the registry: a host the app learns of while it is reading is read
  /// at once, and every host is read again on the interval.
  void _hostsChanged() {
    if (_disposed) return;
    for (final owner in _hosts.hosts) {
      if (!_known.add(owner.name)) continue;
      if (_started) unawaited(refresh(owner.name));
    }
  }

  @override
  void dispose() {
    _disposed = true;
    stop();
    _hosts.removeListener(_hostsChanged);
    super.dispose();
  }
}
