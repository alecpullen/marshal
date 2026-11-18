import 'bridge_client.dart';
import 'host.dart';
import 'relayed_transport.dart';
import 'token_store_web.dart';
import 'transport_web.dart';

// The stub is what a platform with no process to run sees. A browser cannot
// ask `marshal daemon status`, so the one host it can reach is the bridge that
// served the page: the same origin, relative URLs, and the token the browser
// already keeps for it (F1's goal, decision 8). Every host that bridge relays
// is reached through it, with a token of its own (P8 D16, D21).
//
// The page's own address is the only thing a browser knows about its bridge,
// so it is read here rather than passed in: `Uri.base` is the document's URL,
// which includes the mount point the build was served from (`/next/` today, `/`
// once the cutover lands). The transport resolves every request against it, so
// the prefix needs no handling of its own.

/// The bridge that served this page, as one host, and every host that bridge
/// relays (P8 D16).
///
/// A page no bridge served — opened from a file, or from a test harness — finds
/// nothing rather than a host that cannot answer.
Future<List<HostSpec>> discoverHosts() async {
  final page = Uri.base;
  if (page.scheme != 'http' && page.scheme != 'https') return const [];
  final name = page.host.isEmpty ? 'studio' : page.host;
  final client = BridgeClient(WebTransport());
  return [
    HostSpec(name: name, kind: HostKind.server, client: client),
    ...await _relayedHosts(name, client),
  ];
}

/// The hosts the bridge that served the page relays, each reached through that
/// bridge and carrying its own token (spec D18, D21).
///
/// A relayed host's `via` is the page's own host's name: the row says where it
/// is reached, and its detail shows whether that relay is answering (spec
/// D23).
///
/// A bridge with no relay answers 404, and any other failure is the bridge not
/// answering at all. Either way the page lists the bridge it was served by and
/// no host behind it, rather than losing the bridge it can reach. A relay that
/// names the page's own host adds no second row for one host.
Future<List<HostSpec>> _relayedHosts(String page, BridgeClient relay) async {
  try {
    final relayed = await relay.relayHosts();
    return [
      for (final host in relayed.hosts)
        if (host.name != page)
          HostSpec(
            name: host.name,
            kind: HostKind.server,
            via: page,
            client: BridgeClient(
              relayedTransport(
                WebTransport(),
                host: host.name,
                token: () async => TokenStoreWeb.forHost(host.name).read(),
              ),
            ),
          ),
    ];
  } catch (_) {
    return const [];
  }
}
