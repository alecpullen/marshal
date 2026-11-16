import 'package:flutter/foundation.dart';

import '../client/transport.dart';
import 'fleet_store.dart' show describeFailure;

/// Where an edit has got to (F7 decision 3).
///
/// `clean` is a detail with nothing to save; `dirty` is one with an edit the
/// bridge has not seen; `saving` is one whose save is in flight; `failed` is
/// one the bridge refused, kept so the person can correct it; `stale` is one
/// the bridge refused because the item changed underneath, which is not an
/// error the person made.
enum SaveState { clean, dirty, saving, failed, stale }

/// A model holding an edit that could be lost, which is what the guard the
/// shell consults asks about (design-system.md §9, F7 decision 3).
abstract interface class UnsavedEditsSource implements Listenable {
  /// Whether the model holds an edit the bridge has not accepted.
  bool get hasUnsavedEdits;

  /// Throws the edit away, as "Discard the edit" does.
  void discardEdits();
}

/// One section of Automations, Library or Settings: a list of rows, and a
/// detail a person edits (F7 decision 2).
///
/// Save, stale-save and unsaved-edit behaviour is written once, here, so a
/// section only says how to read its list and how to write one item. Every
/// later section extends this class rather than repeating the rules.
abstract class SectionModel<T> extends ChangeNotifier
    implements UnsavedEditsSource {
  List<T> _items = const [];
  bool _loading = false;
  String? _error;
  T? _editing;
  SaveState _saveState = SaveState.clean;
  String? _saveError;

  /// Reads the section's list from the bridge.
  Future<List<T>> fetch();

  /// The key that says two values are the same item.
  Object keyOf(T item);

  /// Whether [item] is one the section does not hold yet: a save creates it
  /// rather than replacing one.
  bool isNew(T item);

  /// Writes [item] to the bridge and answers what the bridge stored, which
  /// for a create carries the id it assigned.
  Future<T> persist(T item);

  /// The rows, in the order the bridge sent them.
  List<T> get items => _items;

  /// The first read is in flight. A reload keeps the rows it has.
  bool get loading => _loading;

  /// Why the last read failed, in the bridge's own words.
  String? get error => _error;

  /// The item whose detail is open, or null when none is.
  T? get editing => _editing;

  /// The open detail is a new item, which a save creates.
  bool get isCreating {
    final current = _editing;
    return current != null && isNew(current);
  }

  SaveState get saveState => _saveState;

  /// Why the last save failed, in the bridge's own words.
  String? get saveError => _saveError;

  @override
  bool get hasUnsavedEdits => _editing != null && _saveState != SaveState.clean;

  /// Reads the list, showing loading while the first read is in flight and
  /// the bridge's refusal in place when it fails. A reload keeps the rows it
  /// already has until the answer replaces them.
  Future<void> load() async {
    if (_loading) return;
    _loading = true;
    _error = null;
    notifyListeners();
    try {
      _items = List.unmodifiable(await fetch());
    } on BridgeFailure catch (failure) {
      _error = describeFailure(failure);
    } catch (error) {
      _error = describeFailure(error);
    } finally {
      _loading = false;
      notifyListeners();
    }
  }

  /// Opens [item]'s detail. An item the list does not hold is one being
  /// created, and a save creates it.
  void edit(T item) {
    _editing = item;
    _saveState = SaveState.clean;
    _saveError = null;
    notifyListeners();
  }

  /// Replaces the open edit with [next]; the detail now has something to
  /// save.
  void change(T next) {
    if (_editing == null) return;
    _editing = next;
    _saveState = SaveState.dirty;
    _saveError = null;
    notifyListeners();
  }

  /// Saves the open edit. It shows saving while the write is in flight; a
  /// refusal keeps the edit and says why, and a stale one offers a reload on
  /// top of it (F7 decision 3).
  Future<void> save() async {
    final next = _editing;
    if (next == null || _saveState == SaveState.saving) return;
    _saveState = SaveState.saving;
    _saveError = null;
    notifyListeners();
    final T stored;
    try {
      stored = await persist(next);
    } on BridgeFailure catch (failure) {
      _refuse(failure.status, describeFailure(failure));
      return;
    } catch (error) {
      _refuse(null, describeFailure(error));
      return;
    }
    store(stored);
    _editing = null;
    _saveState = SaveState.clean;
    _saveError = null;
    notifyListeners();
  }

  /// Closes the detail, throwing away what was typed.
  void cancel() {
    if (_editing == null && _saveState == SaveState.clean) return;
    _editing = null;
    _saveState = SaveState.clean;
    _saveError = null;
    notifyListeners();
  }

  @override
  void discardEdits() => cancel();

  /// After the bridge refused an edit as stale: reads the list again and
  /// keeps the edit, so it can be saved against what is there now.
  Future<void> reloadAfterStale() async {
    final edit = _editing;
    _saveState = SaveState.clean;
    _saveError = null;
    await load();
    if (edit == null) return;
    _editing = edit;
    _saveState = SaveState.dirty;
    notifyListeners();
  }

  /// Drops [item], as a delete the bridge confirmed does.
  void removed(T item) {
    final key = keyOf(item);
    _items = List.unmodifiable([
      for (final held in _items)
        if (keyOf(held) != key) held,
    ]);
    final current = _editing;
    if (current != null && keyOf(current) == key) {
      _editing = null;
      _saveState = SaveState.clean;
      _saveError = null;
    }
    notifyListeners();
  }

  /// Saves the refusal: a conflict is stale, which keeps the edit and offers
  /// a reload; anything else is a failure the person corrects.
  void _refuse(int? status, String message) {
    _saveState = status == 409 || status == 412
        ? SaveState.stale
        : SaveState.failed;
    _saveError = message;
    notifyListeners();
  }

  /// Puts [stored] in the list: over the item it replaces, or at the end when
  /// it is new. A save does this with what the bridge answered; a section
  /// action that creates an item — copying a recipe — does it with the copy.
  void store(T stored) {
    final key = keyOf(stored);
    var replaced = false;
    final next = <T>[];
    for (final held in _items) {
      if (keyOf(held) == key) {
        next.add(stored);
        replaced = true;
      } else {
        next.add(held);
      }
    }
    if (!replaced) next.add(stored);
    _items = List.unmodifiable(next);
    notifyListeners();
  }

  /// Replaces every row at once, for a section action that changes what the
  /// rows hold rather than one item in them: a bot whose held reviews changed
  /// rewrites its own row.
  @protected
  void replaceItems(List<T> next) {
    _items = List.unmodifiable(next);
    notifyListeners();
  }
}
