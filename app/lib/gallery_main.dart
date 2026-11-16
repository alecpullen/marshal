import 'src/ui/gallery/gallery.dart';
import 'src/ui/kit/kit.dart';

/// The debug entry to the kit gallery: `flutter run -t lib/gallery_main.dart`
/// (add `-d chrome` or `-d macos`). The shipped app's entry is `main.dart`,
/// which never shows the gallery; this one exists for looking at the kit and
/// for the web build proof in f0-foundations.md.
void main() => runApp(const GalleryApp());

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
