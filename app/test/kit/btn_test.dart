import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('plain: outlined, fg label, filled only on hover', (
    tester,
  ) async {
    await pumpKit(tester, Btn('Merge', onTap: () {}));
    var d = decorationUnder(tester, find.byType(Btn));
    expect(d.color, isNull);
    expect(borderColour(d), e.border);
    expect(styleOf(tester, 'Merge').color, e.fg);
    expect(tester.getSize(find.byType(Btn)).height, Sizes.control);

    await hover(tester, find.text('Merge'));
    d = decorationUnder(tester, find.byType(Btn));
    expect(d.color, e.hover);
  });

  testWidgets('primary: accent fill, onAccent label', (tester) async {
    await pumpKit(tester, Btn.primary('Review', onTap: () {}));
    final d = decorationUnder(tester, find.byType(Btn));
    expect(d.color, e.accent);
    expect(styleOf(tester, 'Review').color, e.onAccent);
  });

  testWidgets('primary answers hover with the hover fill laid over it', (
    tester,
  ) async {
    await pumpKit(tester, Btn.primary('Review', onTap: () {}));
    await hover(tester, find.text('Review'));
    final fills = decorationsUnder(
      tester,
      find.byType(Btn),
    ).map((d) => d.color).toList();
    expect(fills, containsAll([e.hover, e.accent]));
  });

  testWidgets('disabled: outlined and muted whatever its kind', (tester) async {
    await pumpKit(tester, const Btn.primary('Review'));
    final d = decorationUnder(tester, find.byType(Btn));
    expect(d.color, isNull);
    expect(borderColour(d), e.border);
    expect(styleOf(tester, 'Review').color, e.muted);
  });

  testWidgets('an icon leads the label', (tester) async {
    await pumpKit(
      tester,
      Btn('Open on forge', icon: Glyphs.arrow_up_right, onTap: () {}),
    );
    final icon = tester.widget<Icon>(find.byType(Icon));
    expect(icon.size, Sizes.iconRow);
    expect(
      tester.getCenter(find.byType(Icon)).dx,
      lessThan(tester.getCenter(find.text('Open on forge')).dx),
    );
  });

  testWidgets('a tap and the keyboard both press it', (tester) async {
    var presses = 0;
    await pumpKit(tester, Btn('Merge', onTap: () => presses++));
    await tester.tap(find.text('Merge'));
    expect(presses, 1);

    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pump();
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pump();
    expect(presses, 2);
  });

  testWidgets('focus shows the accent ring', (tester) async {
    await pumpKit(tester, Btn('Merge', onTap: () {}));
    CustomPaint ring() => tester.widget<CustomPaint>(
      find
          .descendant(of: find.byType(Btn), matching: find.byType(CustomPaint))
          .first,
    );
    expect(ring().foregroundPainter, isNull);
    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pump();
    expect(ring().foregroundPainter, isNotNull);
  });

  testWidgets('on the narrow layout the target is at least 44', (tester) async {
    await pumpKit(tester, Btn('Merge', onTap: () {}), width: 390);
    final size = tester.getSize(find.byType(Btn));
    expect(size.height, greaterThanOrEqualTo(Sizes.touch));
    expect(size.width, greaterThanOrEqualTo(Sizes.touch));
  });

  testWidgets('a long label ellipsizes rather than overflowing', (
    tester,
  ) async {
    await pumpKit(
      tester,
      SizedBox(
        width: 80,
        child: Btn('Fix with a worker and tell it more', onTap: () {}),
      ),
    );
    expect(tester.takeException(), isNull);
  });
}
