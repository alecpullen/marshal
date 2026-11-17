import 'bridge_client.dart';
import 'host.dart';
import 'transport_web.dart';

// The stub is what a platform with no process to run sees. A browser cannot
// ask `marshal daemon status`, so the one host it can reach is the bridge that
// served the page: the same origin, relative URLs, and the token the browser
// already keeps for it (F1's goal, decision 8).
//
// The page's own address is the only thing a browser knows about its bridge,
// so it is read here rather than passed in: `Uri.base` is the document's URL,
// which includes the mount point the build was served from (`/next/` today, `/`
// once the cutover lands). The transport resolves every request against it, so
// the prefix needs no handling of its own.

/// The bridge that served this page, as one host.
///
/// A page no bridge served — opened from a file, or from a test harness — finds
/// nothing rather than a host that cannot answer.
Future<List<HostSpec>> discoverHosts() async {
  final page = Uri.base;
  if (page.scheme != 'http' && page.scheme != 'https') return const [];
  return [
    HostSpec(
      name: page.host.isEmpty ? 'studio' : page.host,
      kind: HostKind.server,
      client: BridgeClient(WebTransport()),
    ),
  ];
}
