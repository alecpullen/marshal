import 'dart:convert';
import 'dart:io';

import '../daemon/cli.dart';
import 'bridge_client.dart';
import 'host.dart';
import 'transport.dart';

/// The name the local daemon's host is known by.
const localHostName = 'local';

/// Finds the hosts a native app can reach: the local daemon, when its status
/// document names a socket, and then each server profile `fleet.toml` holds.
///
/// The daemon is asked through the tray's [DaemonCli], so the app learns the
/// socket from the document rather than computing the path (F1 decision 4). A
/// server's token is resolved by `marshal fleet token NAME` when a request
/// needs it, so the app keeps no token of its own (decision 5).
Future<List<HostSpec>> discoverHosts({DaemonCli? cli}) async {
  final command = cli ?? DaemonCli();
  final hosts = <HostSpec>[];
  final local = await _localHost(command);
  if (local != null) hosts.add(local);
  hosts.addAll(await _serverHosts(command));
  return hosts;
}

/// The local daemon's host, or null when no daemon answered: without a socket
/// in the document there is nothing to dial.
Future<HostSpec?> _localHost(DaemonCli cli) async {
  try {
    final status = await cli.status();
    final socket = status.daemon?.socket ?? '';
    if (!status.answering || socket.isEmpty) return null;
    return HostSpec(
      name: localHostName,
      kind: HostKind.local,
      client: BridgeClient(unixTransport(socket)),
    );
  } on MarshalMissingException {
    // A build with no marshal binary reaches no host; the rest of discovery
    // still runs.
    return null;
  } on DaemonException {
    return null;
  } on FormatException {
    // A document this app does not speak is not a host.
    return null;
  }
}

/// One server profile's host, from `marshal fleet servers --json`.
Future<List<HostSpec>> _serverHosts(DaemonCli cli) async {
  final result = await _run(cli.binary, const ['fleet', 'servers', '--json']);
  if (result == null) return const [];
  final List<dynamic> servers;
  try {
    final doc = jsonDecode(result) as Map<String, dynamic>;
    servers = (doc['servers'] as List<dynamic>?) ?? const [];
  } on FormatException {
    return const [];
  } on TypeError {
    return const [];
  }

  final hosts = <HostSpec>[];
  for (final value in servers) {
    if (value is! Map<String, dynamic>) continue;
    final name = value['name'] as String? ?? '';
    final url = value['url'] as String? ?? '';
    if (name.isEmpty || url.isEmpty) continue;
    hosts.add(
      HostSpec(
        name: name,
        kind: HostKind.server,
        client: BridgeClient(
          serverTransport(
            Uri.parse(url),
            token: () => _fleetToken(cli, name),
            caFile: value['caFile'] as String?,
          ),
        ),
      ),
    );
  }
  return hosts;
}

/// A server's token, from `marshal fleet token NAME`: the subcommand exists
/// because the app reads and stores no token itself.
Future<String> _fleetToken(DaemonCli cli, String name) async {
  final result = await _run(cli.binary, ['fleet', 'token', name]);
  if (result == null) {
    throw DaemonException('marshal fleet token $name failed');
  }
  return result.trim();
}

/// Runs the marshal binary and answers its stdout, or null when it failed,
/// naming a missing binary as [MarshalMissingException] the way the tray does.
Future<String?> _run(String binary, List<String> args) async {
  final ProcessResult result;
  try {
    result = await Process.run(binary, args);
  } on ProcessException catch (error) {
    if (error.errorCode == 2) throw const MarshalMissingException();
    rethrow;
  }
  if (result.exitCode != 0) return null;
  return result.stdout as String;
}
