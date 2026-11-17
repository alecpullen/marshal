import 'dart:async';

import 'src/client/host.dart';
import 'src/client/host_discovery_stub.dart'
    if (dart.library.io) 'src/client/host_discovery_io.dart'
    as discovery;
import 'src/ui/debug/connection_check.dart';
import 'src/ui/gallery/gallery.dart';
import 'src/ui/kit/kit.dart';

/// The debug entries, neither of them the shipped app's entry (`main.dart`):
///
/// - `flutter run -t lib/gallery_main.dart` (add `-d chrome` or `-d macos`)
///   shows the kit gallery, which F0's web build proof also uses.
/// - `flutter run -t lib/gallery_main.dart --dart-define=connection-check=true`
///   shows F1's connection check: the hosts this machine can reach, their
///   state, when they were last heard from, and how many agents each runs.
void main() {
  if (const bool.fromEnvironment('connection-check')) {
    runApp(const ConnectionCheckApp());
    return;
  }
  runApp(const GalleryApp());
}

/// The gallery under the kit's root, following the system's light or dark
/// until its button chooses one.
class GalleryApp extends StatefulWidget {
  const GalleryApp({super.key});

  @override
  State<GalleryApp> createState() => _GalleryAppState();
}

class _GalleryAppState extends State<GalleryApp> {
  Brightness? _chosen;

  @override
  Widget build(BuildContext context) {
    return EmberApp(
      title: 'Marshal kit gallery',
      brightness: _chosen,
      home: Builder(
        builder: (context) => Gallery(
          onToggleTheme: () => setState(
            () => _chosen = Ember.of(context).isDark
                ? Brightness.light
                : Brightness.dark,
          ),
        ),
      ),
    );
  }
}

/// The connection check on live hosts: it discovers them once, follows each
/// one's fleet stream, and counts its agents.
///
/// Discovery is the native one (the daemon's status document, then the server
/// profiles) on a native build, and the stub that answers nothing in a
/// browser: there is no `marshal` binary to run there.
class ConnectionCheckApp extends StatefulWidget {
  const ConnectionCheckApp({super.key});

  @override
  State<ConnectionCheckApp> createState() => _ConnectionCheckAppState();
}

class _ConnectionCheckAppState extends State<ConnectionCheckApp> {
  HostRegistry? _registry;
  final _counts = <String, int?>{};
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  @override
  void dispose() {
    _registry?.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    if (!_loading) {
      setState(() => _loading = true);
    }
    final registry = HostRegistry(discovery: discovery.discoverHosts)
      ..addListener(_changed);
    try {
      await registry.discover();
    } catch (_) {
      registry.dispose();
      if (mounted) {
        setState(() => _loading = false);
      }
      return;
    }
    for (final host in registry.hosts) {
      host.addListener(_changed);
      try {
        _counts[host.name] = (await host.client.agents()).length;
      } catch (_) {
        // A host that will not list its agents still appears, without a count.
        _counts[host.name] = null;
      }
    }
    if (!mounted) {
      registry.dispose();
      return;
    }
    final previous = _registry;
    setState(() {
      _registry = registry;
      _loading = false;
    });
    previous?.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final hosts = _registry?.hosts ?? const <Host>[];
    return EmberApp(
      title: 'Marshal connection check',
      home: ConnectionCheck(
        hosts: [for (final host in hosts) _row(host)],
        state: _loading ? StateKind.loading : null,
        onRetry: () => unawaited(_load()),
      ),
    );
  }

  HostRow _row(Host host) => HostRow(
    name: host.name,
    kind: host.kind == HostKind.local ? 'local' : 'server',
    reach: switch (host.reach) {
      HostReach.connecting => HostReachView.connecting,
      HostReach.live => HostReachView.live,
      HostReach.stale => HostReachView.stale,
      HostReach.unreachable => HostReachView.unreachable,
      HostReach.unauthorized => HostReachView.unauthorized,
    },
    lastContact: host.lastContact,
    agentCount: _counts[host.name],
  );
}
