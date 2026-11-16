import 'src/client/host.dart';
import 'src/client/host_discovery_stub.dart'
    if (dart.library.io) 'src/client/host_discovery_io.dart';
import 'src/menu/desktop_stub.dart'
    if (dart.library.io) 'src/menu/desktop.dart';
import 'src/model/fleet_store.dart';
import 'src/model/inbox_model.dart';
import 'src/ui/kit/kit.dart';
import 'src/ui/shell/shell.dart';

/// The one entry point (F2 decision 2).
///
/// The Studio is a Flutter app on every platform, so it is always started, on
/// the fleet the platform can reach: the local daemon and every server profile
/// of a desktop, and the bridge that served the page in a browser. On a
/// desktop the window starts hidden and the menu-bar item opens it; in a
/// browser the page is the window, and [startTray] is the stub.
Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  final hosts = HostRegistry(discovery: discoverHosts);
  final store = FleetStore(hosts);
  final inbox = InboxModel(
    store,
    client: (name) =>
        hosts.hosts.firstWhere((host) => host.name == name).client,
  );
  runApp(
    EmberApp(home: Studio(store: store, inbox: inbox, read: hosts.discover)),
  );
  await startTray();
}
