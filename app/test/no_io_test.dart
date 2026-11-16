import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

/// The boundary rule, enforced rather than remembered: `dart:io` lives in
/// `lib/src/daemon/cli.dart` and nowhere else under `lib/`.
///
/// That is what keeps the status models and the menu model pure Dart, so
/// `flutter test` runs them with no daemon, no display and no plugin, and a
/// later split of the app into its own repository is a directory move rather
/// than an untangling. A second `dart:io` import — a `File(logPath)` read here,
/// an `exit` there — would quietly undo both.
void main() {
  const allowed = 'lib/src/daemon/cli.dart';

  test('only cli.dart imports dart:io', () {
    final lib = Directory('lib');
    expect(lib.existsSync(), isTrue,
        reason: 'run this from the app directory: lib/ was not found');

    final offenders = <String>[];
    for (final entity in lib.listSync(recursive: true)) {
      if (entity is! File || !entity.path.endsWith('.dart')) continue;
      final path = entity.path.replaceAll(r'\', '/');
      if (path == allowed) continue;
      if (entity.readAsLinesSync().any((l) => l.contains('dart:io'))) {
        offenders.add(path);
      }
    }

    expect(
      offenders,
      isEmpty,
      reason: 'dart:io belongs only in $allowed; these files import it: '
          '${offenders.join(', ')}',
    );
  });

  test('cli.dart is where the exemption points', () {
    // Without this, renaming or deleting the one file the rule exempts would
    // turn the test above into a no-op that still passes.
    expect(File(allowed).existsSync(), isTrue,
        reason: '$allowed is the one file allowed dart:io; it has moved');
  });
}
