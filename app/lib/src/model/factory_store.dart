import 'dart:async';

import 'package:flutter/foundation.dart';

import '../client/contract.g.dart';
import '../client/host.dart';
import '../client/transport.dart';
import 'fleet_store.dart' show describeFailure;

/// Every factory work item the app knows, per host (F6 decision 14).
///
/// It is the one reader of the item list and the only poller: a screen
/// projects from here and never fetches an item of its own. The bridge has no
/// stream for items, so the store polls — every five seconds while any item is
/// live, every minute otherwise (F6 plan 1) — and reads the item again after a
/// command answered or a version conflict.
///
/// A failed read keeps the items it has and says why: a host being away is not
/// a claim that its work items are gone.
class FactoryStore extends ChangeNotifier {
  FactoryStore(
    this._hosts, {
    this.activePoll = const Duration(seconds: 5),
    this.idlePoll = const Duration(seconds: 60),
    DateTime Function()? now,
  }) : _now = now ?? DateTime.now {
    _hosts.addListener(_hostsChanged);
    _hostsChanged();
  }

  final HostRegistry _hosts;

  /// How often the list is read while an item is live.
  final Duration activePoll;

  /// How often it is read when every item has finished.
  final Duration idlePoll;

  final DateTime Function() _now;
  final _items = <String, List<FactoryItem>>{};
  final _failure = <String, String>{};
  final _loaded = <String>{};
  final _conflicts = <String, String>{};
  final _generations = <String, int>{};
  Timer? _timer;
  bool _reading = false;
  bool _disposed = false;
  int _requests = 0;

  /// The hosts the app reaches, in discovery order.
  List<String> get hosts =>
      List.unmodifiable([for (final owner in _hosts.hosts) owner.name]);

  /// The items every host reported, or one host's, or one project's.
  ///
  /// The bridge's order is kept: it is the order the factory screen sorts, and
  /// a second sort here could disagree with it.
  List<FactoryItem> items({String? host, String? projectRoot}) {
    final all = [
      for (final name in (host != null ? [host] : hosts))
        ...?_items[name],
    ];
    if (projectRoot == null) return List.unmodifiable(all);
    return List.unmodifiable([
      for (final item in all)
        if (item.projectRoot == projectRoot) item,
    ]);
  }

  /// One item, or null when no host has it.
  FactoryItem? byId(String host, String id) {
    for (final item in _items[host] ?? const <FactoryItem>[]) {
      if (item.id == id) return item;
    }
    return null;
  }

  /// Every agent that is a worker of any item, ever (F6 decision 13).
  ///
  /// A historical worker counts: an agent that implemented one item and was
  /// started again by hand is still the factory's, and listing it twice would
  /// make one agent look like two.
  Set<String> get workerAgentIds {
    final ids = <String>{};
    for (final item in items()) {
      for (final worker in item.workers) {
        if (worker.agentID.isNotEmpty) ids.add(worker.agentID);
      }
    }
    return ids;
  }

  /// Whether [host]'s list has been read at least once.
  bool loaded(String host) => _loaded.contains(host);

  /// Why [host]'s last read failed, in the bridge's words, or null.
  String? failure(String host) => _failure[host];

  /// Whether any item is still working: the store polls faster while one is.
  bool get hasActiveItems => items().any(isActiveItem);

  /// Why [id] on [host] could not be commanded, when it conflicted: the item
  /// changed underneath, so what was on screen is no longer what is there.
  String? conflictOf(String host, String id) => _conflicts[_key(host, id)];

  /// Forgets a conflict once the person has seen it.
  void clearConflict(String host, String id) {
    if (_conflicts.remove(_key(host, id)) == null) return;
    notifyListeners();
  }

  /// Starts polling. It is idempotent, and the first read happens at once so a
  /// screen is not blank for a poll interval.
  void start() {
    _schedule(Duration.zero);
  }

  /// Stops polling. A read in flight finishes and is kept.
  void stop() {
    _timer?.cancel();
    _timer = null;
  }

  /// Reads [host]'s items now, or every host's when [host] is null.
  Future<void> refresh([String? host]) async {
    if (_disposed || _reading) return;
    _reading = true;
    try {
      for (final name in (host != null ? [host] : hosts)) {
        final owner = _ownerOf(name);
        if (owner == null) continue;
        final generation = (_generations[name] ?? 0) + 1;
        _generations[name] = generation;
        try {
          final items = await owner.client.factoryItems();
          if (_disposed || _generations[name] != generation) continue;
          _items[name] = List.unmodifiable(items);
          _loaded.add(name);
          _failure.remove(name);
        } catch (error) {
          if (_disposed || _generations[name] != generation) continue;
          _failure[name] = describeFailure(error);
        }
      }
    } finally {
      _reading = false;
    }
    if (_disposed) return;
    notifyListeners();
  }

  /// Sends one command for [host]'s item and keeps what it answered.
  ///
  /// The command names the version on screen as its expected version and a
  /// fresh request id, so the bridge refuses a decision made against an item
  /// that has moved. A refusal of that kind — 409 — reads the item again and
  /// records the conflict; the caller keeps the action it offered, because the
  /// person can decide again on what is there now. Any other refusal is thrown
  /// for the screen to say in its own words.
  Future<FactoryItem> command(
    String host,
    String id,
    FactoryCommand command,
  ) async {
    final owner = _ownerOf(host);
    if (owner == null) throw StateError('no host named $host');
    final current = byId(host, id);
    final request = FactoryCommand.fromJson({
      ...command.toJson(),
      'requestId': _requestID(),
      'expectedVersion': current?.version ?? command.expectedVersion,
    });
    _conflicts.remove(_key(host, id));
    try {
      final item = await owner.client.factoryCommand(id, request);
      _apply(host, item);
      notifyListeners();
      return item;
    } on BridgeFailure catch (failure) {
      if (failure.status != 409) rethrow;
      final item = await owner.client.factoryItem(id);
      _apply(host, item);
      _conflicts[_key(host, id)] = conflictWords;
      notifyListeners();
      return item;
    }
  }

  /// What a version conflict says (F6 plan 1): not an error the person made.
  static const conflictWords = 'This changed while you were deciding';

  /// Reads one item again, for the screen that opened it: the list route is
  /// small, and the drill-in needs the whole item.
  Future<FactoryItem> read(String host, String id) async {
    final owner = _ownerOf(host);
    if (owner == null) throw StateError('no host named $host');
    final item = await owner.client.factoryItem(id);
    _apply(host, item);
    notifyListeners();
    return item;
  }

  /// Puts [item] in [host]'s list, replacing the copy it supersedes or adding
  /// it when the list has not seen it.
  void _apply(String host, FactoryItem item) {
    final held = _items[host] ?? const <FactoryItem>[];
    final at = held.indexWhere((each) => each.id == item.id);
    final next = at < 0
        ? [...held, item]
        : [
            for (var i = 0; i < held.length; i++) i == at ? item : held[i],
          ];
    _items[host] = List.unmodifiable(next);
    _loaded.add(host);
    _failure.remove(host);
  }

  /// The next poll, timed from now: fast while something is working.
  void _schedule(Duration delay) {
    _timer?.cancel();
    if (_disposed) return;
    _timer = Timer(delay, () async {
      await refresh();
      if (_disposed) return;
      _schedule(hasActiveItems ? activePoll : idlePoll);
    });
  }

  String _requestID() =>
      'app-factory-${_now().microsecondsSinceEpoch}-${_requests++}';

  static String _key(String host, String id) => '$host|$id';

  Host? _ownerOf(String host) {
    for (final owner in _hosts.hosts) {
      if (owner.name == host) return owner;
    }
    return null;
  }

  void _hostsChanged() {
    if (_disposed) return;
    for (final owner in _hosts.hosts) {
      _items.putIfAbsent(owner.name, () => const []);
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

/// Whether an item is still doing something (web/ui/src/lib/studio/model.ts's
/// `isFactoryActive`): a draft, a ready review, a state waiting on a person
/// and a terminal one are not live, and every other state — including one this
/// build does not know — is, because a poller that stops on an unknown state
/// goes stale silently.
bool isActiveItem(FactoryItem item) => switch (item.state) {
  'draft' || 'ready' || 'needs_input' || 'stopped' || 'closed' => false,
  _ => true,
};