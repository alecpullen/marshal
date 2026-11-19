import 'package:flutter/foundation.dart' show Brightness;
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/model/general_model.dart';
import 'package:marshal_app/src/model/thread_view_state.dart' show Density;

/// The preferences General sets: what a device's saved names open as, and what
/// a change writes back to that device.
void main() {
  test('saved choices open as the preferences', () {
    final preferences = GeneralPreferences(appearance: 'dark', density: 'full');

    expect(preferences.appearance, Appearance.dark);
    expect(preferences.brightness, Brightness.dark);
    expect(preferences.appearanceWords, 'Dark');
    expect(preferences.density, Density.full);
    expect(preferences.densityWords, 'Full');
  });

  test('an unknown saved name is ignored', () {
    // A name another version wrote, or a file edited by hand: the preference
    // opens on the default rather than failing.
    final preferences = GeneralPreferences(
      appearance: 'sepia',
      density: 'cosy',
    );

    expect(preferences.appearance, Appearance.system);
    expect(preferences.brightness, isNull);
    expect(preferences.density, Density.comfortable);
  });

  test('a change is saved', () {
    final saved = <String, String>{};
    final preferences = GeneralPreferences(
      save: (key, value) => saved[key] = value,
    );

    preferences.setAppearance(Appearance.dark);
    expect(saved, {'appearance': 'dark'});

    preferences.setDensity(Density.outline);
    expect(saved, {'appearance': 'dark', 'density': 'outline'});

    // Choosing what is already chosen changes nothing, so nothing is written.
    preferences.setAppearance(Appearance.dark);
    expect(saved, {'appearance': 'dark', 'density': 'outline'});
  });
}
