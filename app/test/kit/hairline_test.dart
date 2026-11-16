import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  testWidgets('horizontal: one pixel high, in border, across', (tester) async {
    await pumpKit(tester, const SizedBox(width: 200, child: Hairline()));
    expect(tester.getSize(find.byType(Hairline)), const Size(200, 1));
    expect(
      tester.widget<ColoredBox>(find.byType(ColoredBox).last).color,
      tokens().border,
    );
  });

  testWidgets('vertical: one pixel wide, down', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(height: 120, child: Hairline.vertical()),
    );
    expect(tester.getSize(find.byType(Hairline)), const Size(1, 120));
  });

  testWidgets('follows the theme', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 10, child: Hairline()),
      brightness: Brightness.dark,
    );
    expect(
      tester.widget<ColoredBox>(find.byType(ColoredBox).last).color,
      Ember.dark.border,
    );
  });
}
