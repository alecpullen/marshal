import 'package:flutter/widgets.dart';
import 'package:material_ui/material_ui.dart' show MaterialApp, ThemeMode;

import '../tokens.dart';

/// The root a Studio window, the gallery and every widget test put the kit
/// under. It supplies what the kit's components rely on and nothing a
/// screen would style itself with: the tokens for light or dark (the
/// system's unless [brightness] chooses), the one background, the default
/// text role, an overlay for tooltips, menus and sheets, the default
/// keyboard shortcuts, and the localizations the text input needs.
///
/// It is Material underneath only because `Field` is; screens never see
/// that.
class EmberApp extends StatelessWidget {
  const EmberApp({
    super.key,
    required this.home,
    this.brightness,
    this.title = 'Marshal',
  });

  final Widget home;

  /// Light or dark as the user chose it; null follows the system.
  final Brightness? brightness;

  final String title;

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: title,
      debugShowCheckedModeBanner: false,
      theme: Ember.light.material,
      darkTheme: Ember.dark.material,
      themeMode: switch (brightness) {
        null => ThemeMode.system,
        Brightness.dark => ThemeMode.dark,
        Brightness.light => ThemeMode.light,
      },
      builder: (context, child) => EmberTheme(
        brightness: brightness,
        child: Builder(
          builder: (context) {
            final e = Ember.of(context);
            return ColoredBox(
              color: e.bg,
              child: DefaultTextStyle(style: e.body, child: child!),
            );
          },
        ),
      ),
      home: home,
    );
  }
}
