import '../daemon/cli.dart';
import '../daemon/status.dart';
import 'bridge_client.dart';
import 'host.dart';
import 'transport.dart';

/// The name the local daemon's host is known by.
const localHostName = 'local';

/// Finds the hosts a native app can reach: the local daemon, when its status
/// document names a socket, and then each host that daemon keeps a tunnel to,
/// dialled over the tunnel's own socket.
///
/// The daemon is asked through the tray's [DaemonCli], so the app learns every
/// socket from the document rather than computing a path (F1 decision 4, one
/// daemon D9). The hosts are that daemon's tunnels: the app runs no `fleet`
/// command, and holds no token of its own.
Future<List<HostSpec>> discoverHosts({DaemonCli? cli}) async {
  final command = cli ?? DaemonCli();
  final status = await _status(command);
  // A daemon that did not answer, or one whose document names no socket, is
  // no way to reach anything: the hosts are that daemon's tunnels.
  final daemon = status?.daemon;
  if (status == null || !status.answering || daemon == null) return const [];
  if (daemon.socket.isEmpty) return const [];

  return [
    HostSpec(
      name: localHostName,
      kind: HostKind.local,
      client: BridgeClient(unixTransport(daemon.socket)),
    ),
    // A host with no socket has no tunnel to dial, so it is not listed.
    for (final host in daemon.hosts)
      if (host.socket.isNotEmpty)
        HostSpec(
          name: host.name,
          kind: HostKind.server,
          destination: host.ssh,
          // A preview of work on this host is opened through the loopback
          // port the daemon forwards to the host's preview listener, so the
          // request names that way there; a host serving no previews names
          // none, as the local host does not.
          client: BridgeClient(
            unixTransport(host.socket),
            previewOrigin: host.previewPort > 0
                ? 'http://localhost:${host.previewPort}'
                : null,
          ),
        ),
  ];
}

/// The daemon's status document, or null when there is no document to read: a
/// build with no marshal binary, a daemon that said nothing, or a document
/// this app does not speak reaches no host rather than failing the fleet.
Future<DaemonStatus?> _status(DaemonCli cli) async {
  try {
    return await cli.status();
  } on MarshalMissingException {
    return null;
  } on DaemonException {
    return null;
  } on FormatException {
    return null;
  }
}
