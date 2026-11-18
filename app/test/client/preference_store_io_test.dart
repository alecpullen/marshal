import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/client/preference_store_io.dart';

/// The desktop's preference store in a temporary directory — never the real
/// config — so a test can write, reopen and read as a later launch would.
void main() {
  late Directory dir;

  setUp(() async {
    dir = await Directory.systemTemp.createTemp('marshal-preferences-');
    addTearDown(() => dir.delete(recursive: true));
  });

  test('a written preference is read back after reopening', () {
    IoPreferenceStore(dir.path)
      ..write('appearance', 'dark')
      ..write('density', 'full');

    final reopened = IoPreferenceStore(dir.path);
    expect(reopened.read('appearance'), 'dark');
    expect(reopened.read('density'), 'full');
  });

  test('a malformed file reads as empty', () {
    File('${dir.path}/studio.json').writeAsStringSync('{"appearance": ');

    final store = IoPreferenceStore(dir.path);
    expect(store.read('appearance'), isNull);
    expect(store.read('density'), isNull);

    // A write replaces what could not be read rather than adding to it.
    store.write('appearance', 'light');
    expect(IoPreferenceStore(dir.path).read('appearance'), 'light');
  });

  test('the directory is created on first write', () {
    final config = '${dir.path}/marshal';
    expect(Directory(config).existsSync(), isFalse);

    IoPreferenceStore(config).write('appearance', 'dark');

    expect(Directory(config).existsSync(), isTrue);
    expect(jsonDecode(File('$config/studio.json').readAsStringSync()), {
      'appearance': 'dark',
    });
  });
}
