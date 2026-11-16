import 'package:marshal_app/src/ui/gallery/gallery.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// One screen the enforcement tests pump: its ready state with fixture data,
/// and one case for each §7 state it handles.
class ScreenCase {
  const ScreenCase({
    required this.name,
    required this.ready,
    required this.states,
  });

  final String name;

  /// The screen with fixture data and nothing wrong.
  final Widget Function() ready;

  /// The screen in each data state. design-system.md §7 requires loading,
  /// empty, stale, error and unavailable (`states_test.dart`).
  final Map<StateKind, Widget Function()> states;

  /// Every way the screen can be pumped, by name.
  Map<String, Widget Function()> get cases => {
    'ready': ready,
    for (final MapEntry(key: kind, value: build) in states.entries)
      kind.name: build,
  };
}

/// The registry of screens. F0 has one, the gallery; each later phase adds
/// its screens here with fixture data, and `tooltips_test.dart`,
/// `states_test.dart` and `overflow_test.dart` then hold them to the design
/// system without being edited.
final screens = <ScreenCase>[
  ScreenCase(
    name: 'gallery',
    ready: () => const Gallery(),
    states: {
      for (final kind in StateKind.values) kind: () => Gallery(state: kind),
    },
  ),
];

/// The states every screen that shows data must handle (§7). Planned is a
/// row, not a screen state, so it is not required here.
const requiredStates = {
  StateKind.loading,
  StateKind.empty,
  StateKind.stale,
  StateKind.error,
  StateKind.unavailable,
};
