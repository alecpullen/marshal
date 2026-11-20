import 'bridge_client.dart';
import 'contract.g.dart';
import 'transport.dart';

// The hosts commands (spec D6, D7) are the daemon's, not this app's: the
// daemon runs `marshal fleet` on its own machine (one-daemon D15, plan 7), so
// the app runs no process for it and edits no `fleet.toml` itself. A browser
// reaches its daemon the same way a native build does, so both builds take
// the one implementation below and there is no conditional import.

/// The hosts commands, run by the daemon [client] reaches.
abstract interface class FleetProfiles {
  /// Whether this build can run them: it can, in both builds.
  bool get supported;

  /// Adds the host [name], reached over SSH at [destination].
  Future<void> add(String name, String destination);

  /// Removes the host [name] and its table.
  Future<void> remove(String name);
}

/// A command failed: the daemon's own words (decision 4).
class FleetProfileFailure implements Exception {
  const FleetProfileFailure(this.message);

  final String message;

  @override
  String toString() => message;
}

/// The hosts commands, run by the daemon [client] reaches.
FleetProfiles fleetProfiles(BridgeClient client) =>
    _DaemonFleetProfiles(client);

/// The commands as the daemon's routes report them: an add is
/// `POST /api/hosts` and a remove is `DELETE /api/hosts/{name}`.
class _DaemonFleetProfiles implements FleetProfiles {
  const _DaemonFleetProfiles(this.client);

  final BridgeClient client;

  @override
  bool get supported => true;

  @override
  Future<void> add(String name, String destination) =>
      _run(() => client.addHost(HostAdd(name: name, ssh: destination)));

  @override
  Future<void> remove(String name) => _run(() => client.removeHost(name));

  /// One command, whose refusal becomes the [FleetProfileFailure] a form
  /// shows: the bridge's message is the daemon's words about why it would not,
  /// so nothing above this file reads a status or imports the transport.
  Future<void> _run(Future<HostList> Function() command) async {
    try {
      await command();
    } on BridgeFailure catch (failure) {
      throw FleetProfileFailure(failure.message);
    }
  }
}
