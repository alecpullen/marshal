import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

/// The shell's keys (f9-parity decision 6): its own, above the place, so `⌘N`
/// and `⌘1/⌘2/⌘3` work wherever a person is rather than only inside a thread.
///
/// The intents are declared here and the mapping is one table, so a test can
/// check the keys without pumping a widget, and the tooltip that explains a key
/// and the behaviour it describes cannot disagree.
class NewWorkIntent extends Intent {
  const NewWorkIntent();
}

class GoOverviewIntent extends Intent {
  const GoOverviewIntent();
}

class GoInboxIntent extends Intent {
  const GoInboxIntent();
}

class GoPullsIntent extends Intent {
  const GoPullsIntent();
}

/// `⌘K`: the keyboard goes to the sidebar's search, wherever the person is —
/// including inside another text field, so a search is one key away from a
/// half-typed draft.
class FocusSearchIntent extends Intent {
  const FocusSearchIntent();
}

/// The shell's shortcuts (f9-parity.md decision 6): ⌘N, ⌘1/⌘2/⌘3 for Overview,
/// Needs you and Pull requests, and ⌘K for the sidebar's search.
///
/// `GoInboxIntent` is still the name of ⌘2's intent and `PlaceKind.inbox` is
/// still the name of the place it opens: the rename is visible words only,
/// and a destination does not move because its row was renamed.
final Map<ShortcutActivator, Intent> shellShortcuts = {
  const SingleActivator(LogicalKeyboardKey.keyN, meta: true):
      const NewWorkIntent(),
  const SingleActivator(LogicalKeyboardKey.digit1, meta: true):
      const GoOverviewIntent(),
  const SingleActivator(LogicalKeyboardKey.digit2, meta: true):
      const GoInboxIntent(),
  const SingleActivator(LogicalKeyboardKey.digit3, meta: true):
      const GoPullsIntent(),
  const SingleActivator(LogicalKeyboardKey.keyK, meta: true):
      const FocusSearchIntent(),
};
