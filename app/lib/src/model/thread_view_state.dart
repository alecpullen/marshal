import 'package:flutter/foundation.dart';

/// How much of each row the transcript draws (design-system.md §5).
enum Density {
  /// Headlines only: a step's first line, a tool row's name.
  outline,

  /// The default: headlines and their one-line context.
  comfortable,

  /// Everything a row carries: narration, output, arguments.
  full,
}

/// What one thread's screen remembers between rebuilds and between visits:
/// the density, where the browse cursor is, whether the view follows the live
/// end, which nodes the user folded, and the composer's draft.
///
/// It is per thread and holds no transcript: the tree comes from
/// [StackStore], and this holds only what the user chose. Nothing here is
/// derived from a node, so a patch that replaces a node leaves every choice
/// standing — which is why a fold survives the row being rewritten.
class ThreadViewState extends ChangeNotifier {
  ThreadViewState();

  Density _density = Density.comfortable;
  String? _cursor;
  bool _following = true;
  String _draft = '';
  final _folded = <String>{};

  /// How much of each row to draw.
  Density get density => _density;

  /// Switches the density, in the order the spec cycles them: outline,
  /// comfortable, full, outline.
  void cycleDensity() {
    _density = switch (_density) {
      Density.outline => Density.comfortable,
      Density.comfortable => Density.full,
      Density.full => Density.outline,
    };
    notifyListeners();
  }

  /// Sets the density directly, for the settings screen.
  set density(Density value) {
    if (_density == value) return;
    _density = value;
    notifyListeners();
  }

  /// The node the browse cursor is on, or null before the user moves it.
  String? get cursor => _cursor;

  set cursor(String? value) {
    if (_cursor == value) return;
    _cursor = value;
    notifyListeners();
  }

  /// Whether the transcript stays at the live end. Scrolling up clears it and
  /// scrolling back to the end sets it again (decision 6).
  bool get following => _following;

  set following(bool value) {
    if (_following == value) return;
    _following = value;
    notifyListeners();
  }

  /// The composer's text for this thread: kept across rebuilds, and across a
  /// reconnect, because it belongs to the thread rather than to a connection.
  String get draft => _draft;

  set draft(String value) {
    if (_draft == value) return;
    _draft = value;
    notifyListeners();
  }

  /// Whether the user folded [nodeId]'s children away. A node the agent marks
  /// foldable starts expanded; only an explicit choice is remembered.
  bool folded(String nodeId) => _folded.contains(nodeId);

  /// Folds or unfolds [nodeId], remembering the choice for the rest of the
  /// session.
  void toggleFold(String nodeId) {
    if (!_folded.remove(nodeId)) _folded.add(nodeId);
    notifyListeners();
  }
}
