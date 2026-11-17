import 'dart:convert';
import 'dart:io';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/tokens.dart';

/// design-system.md §3: Geist and Geist Mono are bundled with the app and no
/// font is fetched at run time. A text role whose family or weight is not in
/// the bundle would be drawn by a fallback — on the web, one the engine
/// fetches — so every role is checked against what the build ships.
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test('every text role uses a family and weight the app bundles', () async {
    final manifest = jsonDecode(
      await rootBundle.loadString('FontManifest.json'),
    ) as List<dynamic>;
    final bundled = <String, Set<int>>{};
    for (final family in manifest.cast<Map<String, dynamic>>()) {
      final weights = bundled.putIfAbsent(family['family'] as String, () => {});
      for (final font in (family['fonts'] as List).cast<Map>()) {
        weights.add((font['weight'] as int?) ?? 400);
      }
    }

    for (final e in [Ember.dark, Ember.light]) {
      for (final MapEntry(key: role, value: style) in e.roles.entries) {
        final family = style.fontFamily;
        expect(
          bundled.keys,
          contains(family),
          reason: '$role uses $family, which is not bundled',
        );
        expect(
          bundled[family],
          contains(style.fontWeight!.value),
          reason:
              '$role uses $family at ${style.fontWeight!.value}, '
              'which is not bundled',
        );
      }
    }
  });

  test('the font licence ships beside the fonts', () {
    // The SIL Open Font License travels with every copy of the fonts.
    expect(File('assets/fonts/OFL.txt').existsSync(), isTrue);
    expect(
      File('assets/fonts/OFL.txt').readAsStringSync(),
      contains('SIL Open Font License'),
    );
  });
}
