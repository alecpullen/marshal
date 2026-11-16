import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  testWidgets('one line in fg, 32 high, across its parent', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 240,
        child: Spark([0.2, 0.9, 1.4, 3.1, 5.9], semanticLabel: 'Spend today'),
      ),
    );
    expect(tester.getSize(find.byType(Spark)), const Size(240, 32));
    expect(
      find.byType(Spark),
      paints..path(color: tokens().fg, style: PaintingStyle.stroke),
    );
  });

  testWidgets('fewer than two values draw nothing and do not throw', (
    tester,
  ) async {
    for (final values in [
      <double>[],
      [3.0],
    ]) {
      await pumpKit(
        tester,
        SizedBox(
          width: 100,
          child: Spark(values, semanticLabel: 'Spend today'),
        ),
      );
      expect(tester.takeException(), isNull);
      expect(find.byType(Spark), isNot(paints..path()));
    }
  });

  testWidgets('a flat series draws a flat line', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 100,
        child: Spark([2, 2, 2], semanticLabel: 'Spend today'),
      ),
    );
    expect(tester.takeException(), isNull);
    expect(find.byType(Spark), paints..path());
  });

  testWidgets('it is named for assistive technology', (tester) async {
    final handle = tester.ensureSemantics();
    await pumpKit(
      tester,
      const SizedBox(
        width: 100,
        child: Spark([1, 2], semanticLabel: 'Spend today'),
      ),
    );
    expect(find.bySemanticsLabel('Spend today'), findsOneWidget);
    handle.dispose();
  });
}
