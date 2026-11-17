import 'dart:async';

import 'package:flutter_test/flutter_test.dart';

import 'support/fonts.dart';

/// Runs before every test file in this directory. It loads the fonts the
/// app bundles — Geist, Geist Mono and the icon fonts — so widget tests lay
/// text out with the real metrics and the goldens show the real faces,
/// rather than the test font's boxes.
Future<void> testExecutable(FutureOr<void> Function() testMain) async {
  TestWidgetsFlutterBinding.ensureInitialized();
  await loadBundledFonts();
  await testMain();
}
