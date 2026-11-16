import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

import 'support/source.dart';

/// The boundary rule, enforced rather than remembered. `lib/src/` has four
/// lanes, and each test below holds one edge between them:
///
/// - `daemon/` and `menu/` — the tray. It learns everything from
///   `marshal daemon status --json` and the commands in its menu, through
///   `daemon/cli.dart`, its one file with `dart:io`.
/// - `client/` — the only code that opens a socket or makes a request: a
///   native transport in files ending `_io.dart` and a browser one in files
///   ending `_web.dart`, behind one interface.
/// - `model/` — pure Dart stores and view models, so every rule in them is a
///   plain unit test: no Flutter, no `dart:io`, no browser.
/// - `ui/` — widgets. It reads `model/` and never touches `client/`, and
///   only its kit and its tokens may reach for Material or Cupertino.
///
/// A rule that drifts here is how the layers quietly merge, so a failure is
/// fixed by moving the code, never by widening an exemption.
void main() {
  const cli = 'lib/src/daemon/cli.dart';
  const client = 'lib/src/client';
  const model = 'lib/src/model';
  const ui = 'lib/src/ui';

  /// The tray's files as they were when the rule was written. They may keep
  /// importing what the tray needs; new files get no such allowance.
  const tray = [
    'lib/main.dart',
    'lib/src/daemon/cli.dart',
    'lib/src/daemon/status.dart',
    'lib/src/menu/host.dart',
    'lib/src/menu/model.dart',
    'lib/src/menu/render.dart',
  ];

  final lib = dartFilesUnder('lib');

  bool isIo(String uri) => uri == 'dart:io';

  bool isBrowser(String uri) =>
      const [
        'dart:html',
        'dart:indexed_db',
        'dart:js',
        'dart:js_interop',
        'dart:js_interop_unsafe',
        'dart:js_util',
        'dart:svg',
        'dart:web_audio',
        'dart:web_gl',
      ].contains(uri) ||
      uri.startsWith('package:web/') ||
      uri.startsWith('package:js/');

  bool isMaterial(String uri) =>
      uri.startsWith('package:material_ui/') ||
      uri == 'package:flutter/material.dart';

  bool isCupertino(String uri) =>
      uri.startsWith('package:cupertino_ui/') ||
      uri.startsWith('package:cupertino_icons/') ||
      uri == 'package:flutter/cupertino.dart';

  bool isFlutter(String uri) =>
      uri.startsWith('package:flutter/') ||
      uri.startsWith('package:flutter_') ||
      uri == 'dart:ui' ||
      uri == 'dart:ui_web' ||
      isMaterial(uri) ||
      isCupertino(uri);

  /// Each file under `lib/` that names a URI [forbidden] says it may not,
  /// as `path: uri` lines for the failure message.
  List<String> offenders(
    bool Function(SourceFile file) applies,
    bool Function(SourceFile file, String uri) forbidden,
  ) => [
    for (final file in lib)
      if (applies(file))
        for (final uri in file.directiveUris)
          if (forbidden(file, uri)) '${file.path}: $uri',
  ];

  test('dart:io only in daemon/cli.dart and client/*_io.dart', () {
    final found = offenders(
      (file) =>
          file.path != cli &&
          !(file.isUnder(client) && file.name.endsWith('_io.dart')),
      (_, uri) => isIo(uri),
    );
    expect(
      found,
      isEmpty,
      reason:
          'dart:io belongs in $cli and in $client/*_io.dart only:\n'
          '${found.join('\n')}',
    );
  });

  test('browser APIs only in client/*_web.dart', () {
    final found = offenders(
      (file) => !(file.isUnder(client) && file.name.endsWith('_web.dart')),
      (_, uri) => isBrowser(uri),
    );
    expect(
      found,
      isEmpty,
      reason:
          'browser APIs belong in $client/*_web.dart only:\n'
          '${found.join('\n')}',
    );
  });

  test('model/ imports no Flutter, no dart:io and no browser API', () {
    final found = offenders(
      (file) => file.isUnder(model),
      (_, uri) => isFlutter(uri) || isIo(uri) || isBrowser(uri),
    );
    expect(
      found,
      isEmpty,
      reason:
          '$model is pure Dart, tested without Flutter:\n'
          '${found.join('\n')}',
    );
  });

  test('ui/ never imports client/', () {
    final found = offenders((file) => file.isUnder(ui), (file, uri) {
      final target = file.resolve(uri);
      return target != null &&
          (target == client || target.startsWith('$client/'));
    });
    expect(
      found,
      isEmpty,
      reason:
          '$ui reads model/ and never touches $client:\n'
          '${found.join('\n')}',
    );
  });

  test(
    'Material and Cupertino only in ui/kit/, ui/tokens.dart and the tray',
    () {
      final found = offenders(
        (file) =>
            !file.isUnder('$ui/kit') &&
            file.path != '$ui/tokens.dart' &&
            !tray.contains(file.path),
        (_, uri) => isMaterial(uri) || isCupertino(uri),
      );
      expect(
        found,
        isEmpty,
        reason:
            'screens build from the kit; Material and Cupertino stay in '
            '$ui/kit/ and $ui/tokens.dart:\n${found.join('\n')}',
      );
    },
  );

  test("nothing imports the SDK's material.dart or cupertino.dart", () {
    // Flutter 3.50 deprecates both in favour of the material_ui and
    // cupertino_ui packages; the app is on the packages everywhere.
    final found = [
      for (final file in [...lib, ...dartFilesUnder('test')])
        for (final uri in file.directiveUris)
          if (uri == 'package:flutter/material.dart' ||
              uri == 'package:flutter/cupertino.dart')
            '${file.path}: $uri',
    ];
    expect(found, isEmpty, reason: found.join('\n'));
  });

  test('every exemption points at something that exists', () {
    // Without this, renaming or deleting what a rule exempts would turn the
    // rule into a no-op that still passes.
    for (final path in tray) {
      expect(
        File(path).existsSync(),
        isTrue,
        reason: '$path is named by the tray exemption; it has moved',
      );
    }
    for (final dir in [client, model]) {
      expect(
        Directory(dir).existsSync(),
        isTrue,
        reason: '$dir is a layer the rule names; it has moved',
      );
    }
    expect(
      File('$ui/tokens.dart').existsSync(),
      isTrue,
      reason: '$ui/tokens.dart is exempt from the Material rule; it has moved',
    );
  });
}
