import 'host.dart';

// The stub is what a platform with no process to run sees. A browser cannot
// ask `marshal daemon status`, so it discovers no host: the browser reaches
// the bridge that served the page instead, which F2 wires up.

/// Answers no hosts.
Future<List<HostSpec>> discoverHosts() async => const [];
