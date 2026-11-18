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

/// The shell's shortcuts (f9-parity.md decision 6): ⌘N, and ⌘1/⌘2/⌘3 for
/// Overview, Inbox and Pull requests.
final Map<ShortcutActivator, Intent> shellShortcuts = {
  const SingleActivator(LogicalKeyboardKey.keyN, meta: true):
      const NewWorkIntent(),
  const SingleActivator(LogicalKeyboardKey.digit1, meta: true):
      const GoOverviewIntent(),
  const SingleActivator(LogicalKeyboardKey.digit2, meta: true):
      const GoInboxIntent(),
  const SingleActivator(LogicalKeyboardKey.digit3, meta: true):
      const GoPullsIntent(),
};
