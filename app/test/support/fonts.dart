import 'dart:convert';

import 'package:flutter/services.dart';

/// Loads every font in the app's FontManifest.json into the test engine:
/// the families pubspec.yaml bundles and those its packages bring (the
/// Cupertino icons). `flutter test` builds the asset bundle, so the files
/// are read the way the app reads them, not from the network.
Future<void> loadBundledFonts() async {
  final manifest = jsonDecode(
    await rootBundle.loadString('FontManifest.json'),
  ) as List<dynamic>;
  for (final family in manifest.cast<Map<String, dynamic>>()) {
    final loader = FontLoader(family['family'] as String);
    for (final font in (family['fonts'] as List).cast<Map<String, dynamic>>()) {
      loader.addFont(rootBundle.load(font['asset'] as String));
    }
    await loader.load();
  }
}
