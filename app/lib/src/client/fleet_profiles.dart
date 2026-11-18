import 'fleet_profiles_stub.dart'
    if (dart.library.io) 'fleet_profiles_io.dart'
    as runner;

// The `marshal fleet` commands that edit `fleet.toml` (spec D6, D7). Which
// runner a build has is chosen the way `main.dart` chooses discovery: a
// native build runs the binary, and a browser answers that it cannot add a
// host. The commands themselves live in the lane where `dart:io` is allowed
// (`fleet_profiles_io.dart`), and a token reaches the CLI on its stdin only —
// never in argv and never in the environment (D7).

/// The `marshal fleet` commands that change fleet.toml (spec D6, D7).
abstract interface class FleetProfiles {
  /// Whether this build can run them: false in a browser.
  bool get supported;

  /// Adds the server [name] at [url], trusting the certificates [caFile]
  /// holds when one is given: `marshal fleet add NAME URL [--ca-file PATH]`.
  Future<void> add(String name, Uri url, {String? caFile});

  /// Stores [token] by writing it to `fleet login`'s stdin, never its argv.
  Future<void> login(String name, String token);

  /// Removes the server [name], and the token stored for it.
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
