import 'src/menu/desktop_stub.dart'
    if (dart.library.io) 'src/menu/desktop.dart';
import 'src/ui/kit/kit.dart';

/// The one entry point (F2 decision 2).
///
/// The Studio is a Flutter app on every platform, so it is always started.
/// On a desktop the window it draws in starts hidden and the menu-bar item
/// opens it; in a browser the page is the window, and no tray code is
/// compiled in at all ([startTray] is the conditional import above).
Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(const StudioRoot());
  await startTray();
}

/// The Studio's window content.
///
/// F2's shell — the sidebar, the Overview and the Inbox — arrives with the
/// next step of this phase; until it does, the window says where it is rather
/// than pretending to be a Studio (design-system.md §7).
class StudioRoot extends StatelessWidget {
  const StudioRoot({super.key});

  @override
  Widget build(BuildContext context) {
    return EmberApp(
      home: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: Layout.sidebar),
          child: const Padding(
            padding: EdgeInsets.all(Space.s24),
            child: StateMessage.planned(
              name: 'The Studio',
              note:
                  'The sidebar and the places arrive with the next step of '
                  'this phase',
            ),
          ),
        ),
      ),
    );
  }
}
