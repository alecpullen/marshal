import 'package:flutter/foundation.dart';

import '../client/bridge_client.dart';
import '../client/contract.g.dart';
import '../client/host.dart';
import '../client/transport.dart';

/// The one writer of the fleet's state (F2 decision 6).
///
/// Per host it holds what the bridge said: the agents, the projects, the
/// needs-you queue and the budget report. Views read it here and nowhere
/// else; the screens take it as data and never fetch (design-system.md §5).
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

  /// What the app can reach of [host] now. A host nothing discovered is
  /// still connecting: nothing has been heard from it yet.
  HostReach reach(String host) => _ownerOf(host)?.reach ?? HostReach.connecting;

  /// When [host] last said anything, or null when it never has.
  DateTime? lastContact(String host) => _ownerOf(host)?.lastContact;

  /// What went wrong the last time [host]'s snapshot was read, or null when
  /// it was read. The rows are kept either way: a failed read is not a reason
  /// to empty them, and it is not a claim that the host is gone.
  String? failure(String host) => _state(host).failure;

  /// Reads one host's snapshot: its agents, projects, inbox and budgets.
  ///
  /// The four are read together so the rows and the queue describe the same
  /// moment. A failed read leaves the last data in place and records why.
  Future<void> refresh(String host) async {
    final owner = _ownerOf(host);
    if (owner == null) return;
    final state = _state(host);
    final generation = ++state.generation;
    try {
      final agents = await owner.client.agents();
      final projects = await owner.client.projects();
      final inbox = await owner.client.inbox();
      final budgets = await _budgets(owner.client, state);
      if (_disposed || generation != state.generation) return;
      state
        ..agents = List.unmodifiable(agents)
        ..projects = List.unmodifiable(projects)
        ..queue = List.unmodifiable(inbox.queue)
        ..budgets = budgets
        ..failure = null;
      notifyListeners();
    } catch (error) {
      if (_disposed || generation != state.generation) return;
      state.failure = _describe(error);
      notifyListeners();
    }
  }

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

  /// The failure in words: the bridge's own when it sent some.
  static String _describe(Object error) {
    if (error is BridgeFailure) return error.message;
    return error.toString();
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

  /// A host was discovered: start holding its state. A host already known is
  /// left alone, exactly as the registry leaves it.
  void _hostsChanged() {
    var added = false;
    for (final owner in _registry.hosts) {
      if (_states.containsKey(owner.name)) continue;
      _states[owner.name] = _HostState();
      added = true;
    }
    if (added) notifyListeners();
  }

  @override
  void dispose() {
    _disposed = true;
    _registry.removeListener(_hostsChanged);
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
  BudgetReport? budgets;
  String? failure;

  /// Bumped by each read, so a slow answer cannot overwrite a newer one.
  int generation = 0;
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
  });

  final String name;

  /// A local daemon, or a server profile.
  final HostKind kind;

  /// Live, stale, unreachable or unauthorized.
  final HostReach reach;

  /// When it was last heard from, or null when it never has been.
  final DateTime? lastContact;

  @override
  String toString() => 'HostView($name, ${reach.name})';
}
