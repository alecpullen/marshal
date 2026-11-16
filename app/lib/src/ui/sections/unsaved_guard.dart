import '../../model/section_model.dart';
import '../kit/kit.dart';

/// The one guard the shell consults before it leaves a place with unsaved
/// edits (design-system.md §9, F7 decision 3).
///
/// A place registers the section models it is editing; the shell asks once,
/// in one sheet, whichever of them holds the edit. Saying "Discard the edit"
/// throws it away, because the person chose to leave it behind.
class UnsavedGuard extends ChangeNotifier {
  final _sources = <UnsavedEditsSource>[];

  /// Has [source] watched: its unsaved edit is the guard's to ask about.
  void watch(UnsavedEditsSource source) {
    if (_sources.contains(source)) return;
    _sources.add(source);
    source.addListener(notifyListeners);
    notifyListeners();
  }

  /// Stops watching [source], as a place that is gone does.
  void unwatch(UnsavedEditsSource source) {
    if (!_sources.remove(source)) return;
    source.removeListener(notifyListeners);
    notifyListeners();
  }

  /// Whether anything the guard watches holds an unsaved edit.
  bool get hasUnsavedEdits => _sources.any((source) => source.hasUnsavedEdits);

  /// Asks, in one sheet, whether to leave; true when the person says yes.
  ///
  /// Dismissing the sheet is not an answer, so it keeps the person where
  /// they are: leaving without saving is a choice, never an accident.
  Future<bool> confirm(BuildContext context) async {
    if (!hasUnsavedEdits) return true;
    final leave = await Sheet.show<bool>(
      context,
      Sheet(
        title: 'Leave without saving?',
        child: const Text(
          'The edit you have not saved is lost. Nothing else changes.',
        ),
        actions: [
          Btn.primary(
            'Keep editing',
            onTap: () => Navigator.of(context).pop(false),
          ),
          Btn('Discard the edit', onTap: () => Navigator.of(context).pop(true)),
        ],
      ),
    );
    if (leave != true) return false;
    discardAll();
    return true;
  }

  /// Throws away every watched edit, as leaving without saving does.
  void discardAll() {
    for (final source in List.of(_sources)) {
      source.discardEdits();
    }
  }

  @override
  void dispose() {
    for (final source in _sources) {
      source.removeListener(notifyListeners);
    }
    _sources.clear();
    super.dispose();
  }
}
