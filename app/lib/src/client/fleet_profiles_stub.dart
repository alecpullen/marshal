import 'fleet_profiles.dart';

// The stub is what a browser build sees: a page has no process to run, so
// every command refuses rather than pretending `fleet.toml` was edited. The
// Hosts section keeps its row planned until plan 4 (D22); a command that
// reaches this runner anyway says why rather than failing obscurely.

const _reason = 'A browser cannot add a host';

/// The runner a browser has: nothing runs, and every command says so.
FleetProfiles fleetProfiles({String? binary}) => const _BrowserFleetProfiles();

class _BrowserFleetProfiles implements FleetProfiles {
  const _BrowserFleetProfiles();

  @override
  bool get supported => false;

  @override
  Future<void> add(String name, String destination) =>
      throw const FleetProfileFailure(_reason);

  @override
  Future<void> remove(String name) => throw const FleetProfileFailure(_reason);
}
