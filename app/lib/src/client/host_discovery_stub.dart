import 'bridge_client.dart';
import 'host.dart';
import 'relayed_transport.dart';
import 'transport_web.dart';

// The stub is what a platform with no process to run sees. A browser cannot
// ask `marshal daemon status`, so the one host it can reach is the daemon that
// served the page: the same origin, relative URLs, and no credential of its
// own — the operator's proxy admits the page (F1's goal, decision 8). Every
// host that daemon keeps a tunnel to is reached through it (one-daemon D10).
//
// The page's own address is the only thing a browser knows about its daemon,
// so it is read here rather than passed in: `Uri.base` is the document's URL,
// which includes the mount point the build was served from. The transport
// resolves every request against it, so the prefix needs no handling of its
// own.

/// The daemon that served this page, as one host, and every host it keeps a
/// tunnel to (one-daemon D10).
///
/// A page no daemon served — opened from a file, or from a test harness — finds
/// nothing rather than a host that cannot answer.
Future<List<HostSpec>> discoverHosts() async {
  final page = Uri.base;
  if (page.scheme != 'http' && page.scheme != 'https') return const [];
  final name = page.host.isEmpty ? 'studio' : page.host;
  final client = BridgeClient(WebTransport());
  return [
    HostSpec(name: name, kind: HostKind.server, client: client),
    ...await _daemonHosts(name, client),
  ];
}

/// The hosts the daemon that served the page keeps a tunnel to, each reached
/// through that daemon (one-daemon D10).
///
/// A host's `via` is the page's own host's name: the row says where it is
/// reached, and its destination is the SSH target the daemon opened the tunnel
/// to.
///
/// A daemon that reports no hosts answers a list with none in it, and any other
/// failure is the daemon not answering at all. Either way the page lists the
/// daemon it was served by and no host behind it, rather than losing the daemon
/// it can reach. A tunnel that names the page's own host adds no second row for
/// one host.
Future<List<HostSpec>> _daemonHosts(String page, BridgeClient daemon) async {
  try {
    final reported = await daemon.hosts();
    return [
      for (final host in reported.hosts)
        if (host.name != page)
          HostSpec(
            name: host.name,
            kind: HostKind.server,
            via: page,
            destination: host.ssh,
            client: BridgeClient(
              relayedTransport(WebTransport(), host: host.name),
            ),
          ),
    ];
  } catch (_) {
    return const [];
  }
}
