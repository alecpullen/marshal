import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/transport.dart';
import 'package:marshal_app/src/model/section_model.dart';

/// One row of the test section.
class _Item {
  const _Item(this.id, this.name);

  final String id;
  final String name;

  _Item withName(String name) => _Item(id, name);

  @override
  String toString() => '_Item($id, $name)';
}

/// A section over [_Item]s whose writes a test scripts.
class _Section extends SectionModel<_Item> {
  _Section(this.rows);

  List<_Item> rows;

  /// The write this section makes; the default stores what it is given.
  Future<_Item> Function(_Item item) write = (item) async => item;

  /// The read this section makes; the default answers [rows].
  Future<List<_Item>> Function()? read;

  /// How many writes the bridge saw.
  final writes = <_Item>[];

  /// How many reads the bridge saw.
  int reads = 0;

  @override
  Future<List<_Item>> fetch() {
    reads++;
    final scripted = read;
    return scripted != null ? scripted() : Future.value(rows);
  }

  @override
  Object keyOf(_Item item) => item.id;

  @override
  bool isNew(_Item item) => !rows.any((held) => held.id == item.id);

  @override
  Future<_Item> persist(_Item item) {
    writes.add(item);
    return write(item);
  }
}

void main() {
  test('a first read fills the rows', () async {
    final model = _Section([const _Item('a', 'nightly')]);
    expect(model.loading, isFalse);
    final loading = model.load();
    expect(model.loading, isTrue);
    await loading;
    expect(model.loading, isFalse);
    expect(model.items.map((item) => item.id), ['a']);
    expect(model.error, isNull);
  });

  test('a read the bridge refuses says why in its own words', () async {
    final model = _Section([]);
    model.read = () async =>
        throw BridgeFailure(500, '{"error":"the daemon is not running"}');
    await model.load();
    expect(model.error, 'the daemon is not running');
    expect(model.items, isEmpty);
  });

  test('an edit is unsaved until the bridge takes it', () async {
    final model = _Section([const _Item('a', 'nightly')]);
    await model.load();
    model.edit(model.items.first);
    expect(model.hasUnsavedEdits, isFalse);
    model.change(model.items.first.withName('nightly gate'));
    expect(model.saveState, SaveState.dirty);
    expect(model.hasUnsavedEdits, isTrue);
    model.cancel();
    expect(model.editing, isNull);
    expect(model.hasUnsavedEdits, isFalse);
  });

  test('a save stores what the bridge answered', () async {
    final model = _Section([const _Item('a', 'nightly')]);
    await model.load();
    model.edit(model.items.first);
    model.change(const _Item('a', 'nightly gate'));
    await model.save();
    expect(model.saveState, SaveState.clean);
    expect(model.editing, isNull);
    expect(model.items.single.name, 'nightly gate');
    expect(model.writes.single.name, 'nightly gate');
  });

  test('a save of a new item adds it', () async {
    final model = _Section([]);
    await model.load();
    model.edit(const _Item('b', 'weekly'));
    expect(model.isCreating, isTrue);
    model.change(const _Item('b', 'weekly review'));
    await model.save();
    expect(model.items.single.id, 'b');
    expect(model.isCreating, isFalse);
  });

  test('a refusal keeps the edit, in the bridge’s words', () async {
    final model = _Section([const _Item('a', 'nightly')]);
    model.write = (_) async =>
        throw BridgeFailure(500, '{"error":"a schedule needs a recipe"}');
    await model.load();
    model.edit(model.items.first);
    model.change(const _Item('a', 'nightly gate'));
    await model.save();
    expect(model.saveState, SaveState.failed);
    expect(model.saveError, 'a schedule needs a recipe');
    expect(model.editing?.name, 'nightly gate');
    expect(model.hasUnsavedEdits, isTrue);
  });

  test('a stale refusal keeps the edit and offers a reload', () async {
    final model = _Section([const _Item('a', 'nightly')]);
    model.write = (_) async => throw BridgeFailure(409, 'it changed');
    await model.load();
    model.edit(model.items.first);
    model.change(const _Item('a', 'nightly gate'));
    await model.save();
    expect(model.saveState, SaveState.stale);
    expect(model.saveError, 'it changed');
    expect(model.editing?.name, 'nightly gate');

    // What the bridge holds now.
    model.rows = [
      const _Item('a', 'nightly gate (renamed)'),
      const _Item('b', 'weekly'),
    ];
    await model.reloadAfterStale();
    expect(model.items.length, 2);
    expect(model.saveState, SaveState.dirty);
    expect(model.editing?.name, 'nightly gate');
  });

  test('a delete drops the row and closes its detail', () async {
    final model = _Section([const _Item('a', 'nightly')]);
    await model.load();
    model.edit(model.items.first);
    model.removed(model.items.first);
    expect(model.items, isEmpty);
    expect(model.editing, isNull);
  });

  test('a section watched by a guard reports its edit', () {
    final model = _Section([const _Item('a', 'nightly')]);
    var notified = 0;
    model.addListener(() => notified++);
    model.edit(const _Item('a', 'nightly'));
    model.change(const _Item('a', 'weekly'));
    expect(notified, greaterThan(0));
    model.discardEdits();
    expect(model.hasUnsavedEdits, isFalse);
  });
}
