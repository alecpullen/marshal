import 'package:flutter/foundation.dart';

import 'bridge_client.dart';
import 'event_stream.dart';

/// Which bridge a host is (F1 decision 8).
enum HostKind { local, server }

/// What the app can reach of one host. `stale` is a host that is talking less
/// often than it should; `unreachable` is one nothing answers on; and
/// `unauthorized` is one the operator's proxy refused — a signed-out page,
/// which is terminal (one-daemon D13) rather than a state to retry out of.
enum HostReach { connecting, live, stale, unreachable, unauthorized }

/// Builds the fleet stream a host follows. It is a parameter so a test can
/// hand a host a clock and a scripted connection.
typedef FleetFor = EventStream Function(BridgeClient client);

/// One host discovery found: its name, its kind, and the client that reaches
/// it.
class HostSpec {
  const HostSpec({
    required this.name,
    required this.kind,
    required this.client,
    this.via,
    this.destination,
  });

  final String name;
  final HostKind kind;
  final BridgeClient client;

  /// The relay's host name this host is reached through, or null.
  final String? via;

  /// The SSH destination this host is reached over, or null.
  final String? destination;
}

/// Finds the hosts this build can reach.
typedef HostDiscovery = Future<List<HostSpec>> Function();

/// One host: a name, a kind, the client that reaches it, and what the app can
/// reach of it right now.
///
/// Its reach follows its fleet stream: a live stream is a live host, silence
/// is `stale`, a request the proxy refused is `unauthorized`, and a stream that
/// is reconnecting to a host that has answered before is `unreachable` — with
/// the time of its last contact kept, so a screen can say when it was last seen
/// (design-system.md §7).
class Host extends ChangeNotifier {
  Host({
    required this.name,
    required this.kind,
    required this.client,
    required this.fleet,
    this.via,
    this.destination,
  });

  final String name;
  final HostKind kind;
  final BridgeClient client;

  /// The relay's host name this host is reached through, or null.
  final String? via;

  /// The SSH destination this host is reached over, or null.
  final String? destination;

  /// The fleet stream this host follows.
  final EventStream fleet;

  /// What the app can reach of this host now.
  HostReach get reach => _reach;
  HostReach _reach = HostReach.connecting;

  /// When the host last said anything, or null when it never has.
  DateTime? get lastContact => _lastContact;
  DateTime? _lastContact;

  /// Starts following the host's stream.
  void start() {
    fleet.state.addListener(_follow);
    fleet.start();
    _follow();
  }

  /// Stops following it: nothing more is retried for this host.
  void stop() {
    fleet.state.removeListener(_follow);
    fleet.stop();
  }

  void _follow() {
    final contact = fleet.lastEventAt;
    if (contact != null) {
      _lastContact = contact;
    }
    final next = switch (fleet.state.value) {
      StreamState.live => HostReach.live,
      StreamState.stale => HostReach.stale,
      StreamState.stopped => HostReach.unauthorized,
      // Reconnecting: the host answers nothing at this instant. Before its
      // first contact that is still the first attempt; after one it has
      // become unreachable.
      StreamState.connecting =>
        _lastContact == null ? HostReach.connecting : HostReach.unreachable,
    };
    if (next == _reach) return;
    _reach = next;
    notifyListeners();
  }

  @override
  void dispose() {
    stop();
    super.dispose();
  }

  @override
  String toString() => 'Host($name, ${kind.name}, ${reach.name})';
}

/// Every host the app knows, discovered rather than assumed.
///
/// A host that cannot be reached is kept: it is marked unreachable with its
/// last contact, never dropped for being down (decision 8).
class HostRegistry extends ChangeNotifier {
  HostRegistry({required this.discovery, FleetFor? fleetFor})
    : _fleetFor = fleetFor ?? _fleet;

  /// Finds the hosts to show. Read-only data, so a test supplies its own.
  final HostDiscovery discovery;
  final FleetFor _fleetFor;
  final _hosts = <Host>[];

  /// Every host found so far, in discovery order.
  List<Host> get hosts => List.unmodifiable(_hosts);

  /// Finds the hosts again, and starts the ones that are new. A host already
  /// known is left alone: its stream is the fresher truth, and a host being
  /// down is not a reason to forget it.
  Future<void> discover() async {
    final specs = await discovery();
    var added = false;
    for (final spec in specs) {
      final known = _hosts.any(
        (host) => host.name == spec.name && host.kind == spec.kind,
      );
      if (known) continue;
      final host = Host(
        name: spec.name,
        kind: spec.kind,
        client: spec.client,
        fleet: _fleetFor(spec.client),
        via: spec.via,
        destination: spec.destination,
      );
      host.start();
      _hosts.add(host);
      added = true;
    }
    if (added) notifyListeners();
  }

  /// Stops, disposes and drops [name], and notifies (spec D9).
  ///
  /// It is the one way a host leaves the fleet: discovery never forgets one,
  /// however long it stays down (decision 8), so a host whose profile was
  /// removed is dropped here and nowhere else. A name this fleet does not list
  /// does nothing.
  void forget(String name) {
    final at = _hosts.indexWhere((host) => host.name == name);
    if (at < 0) return;
    final host = _hosts.removeAt(at);
    host.dispose();
    notifyListeners();
  }

  /// Stops [name]'s stream and starts it again, after its token changed.
  ///
  /// A refused token is terminal (decision 6): the stream is stopped rather
  /// than retried, so a host that has just been signed in to is asked to try
  /// once more instead of waited on. A name this fleet does not list does
  /// nothing.
  void retry(String name) {
    for (final host in _hosts) {
      if (host.name != name) continue;
      host.stop();
      host.start();
      return;
    }
  }

  @override
  void dispose() {
    for (final host in _hosts) {
      host.dispose();
    }
    super.dispose();
  }
}

/// The production fleet stream: the host's own.
EventStream _fleet(BridgeClient client) => client.fleetEvents();
