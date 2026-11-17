import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/gallery/gallery.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import 'support/source.dart';

/// design-system.md §11: a kit component missing from the gallery, or a
/// gallery golden that differs in light or dark, fails here.
///
/// **The goldens are pinned to Linux.** They are drawn with the bundled
/// Geist fonts, and the test engine rasterizes a font through the host's
/// text stack — CoreText on macOS, FreeType on Linux — so the same build
/// does not give the same pixels on both (f0-foundations.md, open
/// question). They are generated and compared on Linux, which is the host
/// CI's `app` job runs on (F1, task 1). Elsewhere they are skipped and say
/// why. Regenerate with `flutter test --update-goldens
/// test/gallery_test.dart` on Linux.
void main() {
  final goldenHost = Platform.isLinux;
  const goldenSkip =
      'gallery goldens are pinned to Linux: macOS rasterizes Geist '
      'differently (see the comment at the top of this file)';

  test('every public kit widget is named in the gallery source', () {
    // The cheap half of the check, read from source so a component added to
    // the kit without a specimen fails even before anything is pumped.
    final gallery = File('lib/src/ui/gallery/gallery.dart').readAsStringSync();
    final missing = [
      for (final name in kitWidgets())
        if (!RegExp('\\b$name\\b').hasMatch(gallery) &&
            !_rootPieces.contains(name))
          name,
    ];
    expect(missing, isEmpty, reason: 'not in the gallery: $missing');
  });

  testWidgets('every public kit widget is drawn in the gallery', (
    tester,
  ) async {
    await _pumpGallery(tester, Brightness.light);
    final drawn = {
      for (final widget in tester.allWidgets) widget.runtimeType.toString(),
    };
    final missing = [
      for (final name in kitWidgets())
        if (!drawn.contains(name)) name,
    ];
    expect(missing, isEmpty, reason: 'not drawn by the gallery: $missing');
  });

  for (final (name, brightness) in [
    ('light', Brightness.light),
    ('dark', Brightness.dark),
  ]) {
    testWidgets('the gallery matches its $name golden', (tester) async {
      await _pumpGallery(tester, brightness, fullHeight: true);
      await expectLater(
        find.byKey(_shot),
        matchesGoldenFile('goldens/gallery_$name.png'),
      );
    }, skip: !goldenHost ? true : null);

    testWidgets('an open menu and sheet match their $name golden', (
      tester,
    ) async {
      await _pumpGallery(tester, brightness);
      await tester.scrollUntilVisible(
        find.text('All hosts'),
        400,
        scrollable: find.byType(Scrollable).first,
      );
      await tester.tap(find.text('All hosts'));
      await tester.pump();
      await tester.pump();
      await expectLater(
        find.byKey(_shot),
        matchesGoldenFile('goldens/gallery_menu_$name.png'),
      );

      await tester.tapAt(const Offset(1000, 790));
      await tester.pump();
      await tester.scrollUntilVisible(
        find.text('Open the sheet'),
        -400,
        scrollable: find.byType(Scrollable).first,
      );
      await tester.tap(find.text('Open the sheet'));
      await tester.pump();
      await expectLater(
        find.byKey(_shot),
        matchesGoldenFile('goldens/gallery_sheet_$name.png'),
      );
    }, skip: !goldenHost ? true : null);
  }

  if (!goldenHost) {
    test('gallery goldens', () {}, skip: goldenSkip);
  }
}

const _shot = ValueKey('gallery');

/// Supporting pieces the gallery shows by being built on them rather than
/// naming them: it is pumped under `EmberApp`, and `Press` is inside every
/// control.
const _rootPieces = {'EmberApp', 'Press'};

/// Every public widget class the kit declares, read from its source.
List<String> kitWidgets() {
  final declaration = RegExp(
    r'class\s+([A-Z]\w*)\s+extends\s+(?:StatelessWidget|StatefulWidget)\b',
  );
  return [
    for (final file in dartFilesUnder('lib/src/ui/kit'))
      for (final m in declaration.allMatches(file.code)) m.group(1)!,
  ]..sort();
}

/// Pumps the gallery at 1024 wide, with motion reduced so the running dot
/// holds still; with [fullHeight] the window is as tall as the gallery.
Future<void> _pumpGallery(
  WidgetTester tester,
  Brightness brightness, {
  bool fullHeight = false,
}) async {
  tester.view.devicePixelRatio = 1;
  tester.view.physicalSize = const Size(1024, 800);
  tester.platformDispatcher.accessibilityFeaturesTestValue =
      const FakeAccessibilityFeatures(disableAnimations: true);
  addTearDown(tester.view.reset);
  addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);

  await tester.pumpWidget(
    RepaintBoundary(
      key: _shot,
      child: EmberApp(brightness: brightness, home: const Gallery()),
    ),
  );
  if (fullHeight) {
    final page = tester.state<ScrollableState>(find.byType(Scrollable).first);
    tester.view.physicalSize = Size(
      1024,
      page.position.maxScrollExtent + page.position.viewportDimension,
    );
    await tester.pump();
  }
}
