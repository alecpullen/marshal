import 'dart:convert';
import 'dart:io';

import '../daemon/cli.dart';
import 'fleet_profiles.dart';

/// _enoent is errno 2, no such file or directory: what a spawn completes with
/// when the executable it was given is not there, on every platform this app
/// runs on.
const _enoent = 2;

/// The runner a native build has: each command is `marshal fleet …`, run
/// through the tray's [DaemonCli] so the binary is the one the app ships (F1
/// decision 4). The app edits `fleet.toml` no other way, and a token travels
/// on `fleet login`'s stdin alone (D7).
FleetProfiles fleetProfiles({String? binary}) =>
    _ProcessFleetProfiles(DaemonCli(binary: binary));

class _ProcessFleetProfiles implements FleetProfiles {
  _ProcessFleetProfiles(this.cli);

  final DaemonCli cli;

  @override
  bool get supported => true;

  @override
  Future<void> add(String name, Uri url, {String? caFile}) => _run(cli.binary, [
    'fleet',
    'add',
    name,
    url.toString(),
    if (caFile != null) ...['--ca-file', caFile],
  ]);

  @override
  Future<void> remove(String name) =>
      _run(cli.binary, ['fleet', 'remove', name]);

  /// login writes the token and a newline to the process's stdin, closes it,
  /// and waits: the token is in no argument and in no environment variable
  /// (D7). Its stderr is read while it runs, because a pipe nobody drains
  /// would leave the process waiting on its own output.
  @override
  Future<void> login(String name, String token) async {
    final process = await _start(cli.binary, ['fleet', 'login', name]);
    final stderrText = process.stderr.transform(utf8.decoder).join();
    process.stdin.write('$token\n');
    await process.stdin.close();
    final code = await process.exitCode;
    final err = await stderrText;
    if (code != 0) throw FleetProfileFailure(_failure(err, code));
  }
}

/// Runs the binary with [args] to completion and fails as decision 4 says.
Future<void> _run(String binary, List<String> args) async {
  final ProcessResult result;
  try {
    result = await Process.run(binary, args);
  } on ProcessException catch (error) {
    if (error.errorCode == _enoent) throw const MarshalMissingException();
    rethrow;
  }
  if (result.exitCode != 0) {
    throw FleetProfileFailure(
      _failure(result.stderr as String, result.exitCode),
    );
  }
}

/// Starts the binary with [args], naming a missing binary as
/// [MarshalMissingException] the way `host_discovery_io.dart` does: an absent
/// `marshal` is its own state, while any other spawn failure keeps the raw
/// error.
Future<Process> _start(String binary, List<String> args) async {
  try {
    return await Process.start(binary, args);
  } on ProcessException catch (error) {
    if (error.errorCode == _enoent) throw const MarshalMissingException();
    rethrow;
  }
}

/// A failure's message: the last non-empty line it wrote to stderr, or the
/// exit code when it wrote nothing (decision 4).
String _failure(String stderr, int code) {
  final lines = const LineSplitter()
      .convert(stderr)
      .where((line) => line.trim().isNotEmpty);
  return lines.isEmpty ? 'marshal exited $code' : lines.last.trim();
}
