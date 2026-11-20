import 'fleet_profiles_stub.dart'
    if (dart.library.io) 'fleet_profiles_io.dart'
    as runner;

// The `marshal fleet` commands that edit `fleet.toml` (spec D6, D7). Which
// runner a build has is chosen the way `main.dart` chooses discovery: a
// native build runs the binary, and a browser answers that it cannot add a
// host. The commands themselves live in the lane where `dart:io` is allowed
// (`fleet_profiles_io.dart`).

/// The `marshal fleet` commands that change fleet.toml (spec D6, D7).
abstract interface class FleetProfiles {
  /// Whether this build can run them: false in a browser.
  bool get supported;

  /// Adds the host [name], reached over SSH at [destination]:
  /// `marshal fleet add NAME DEST`.
  Future<void> add(String name, String destination);

  /// Removes the host [name] and its table.
  Future<void> remove(String name);
}

/// A command failed: its last line of stderr (decision 4).
class FleetProfileFailure implements Exception {
  const FleetProfileFailure(this.message);

  final String message;

  @override
  String toString() => message;
}

/// The runner this build has.
FleetProfiles fleetProfiles({String? binary}) =>
    runner.fleetProfiles(binary: binary);
