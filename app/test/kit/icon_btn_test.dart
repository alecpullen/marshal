import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('its tooltip names it and appears on hover', (tester) async {
    await pumpKit(
      tester,
      IconBtn(Glyphs.plus, tooltip: 'New agent', onTap: () {}),
    );
    expect(hasTip(tester, find.byType(Icon), 'New agent'), isTrue);
    expect(find.text('New agent'), findsNothing);

    await hover(tester, find.byType(IconBtn));
    await tester.pump(const Duration(seconds: 1));
    expect(find.text('New agent'), findsOneWidget);
  });

  testWidgets('its tooltip is its name for assistive technology', (
    tester,
  ) async {
    final handle = tester.ensureSemantics();
    await pumpKit(
      tester,
      IconBtn(Glyphs.plus, tooltip: 'New agent', onTap: () {}),
    );
    expect(find.bySemanticsLabel(RegExp('New agent')), findsWidgets);
    handle.dispose();
  });

  testWidgets('28 square, a 13 icon, muted until hovered', (tester) async {
    await pumpKit(
      tester,
      IconBtn(Glyphs.plus, tooltip: 'New agent', onTap: () {}),
    );
    expect(tester.getSize(find.byType(IconBtn)), const Size(28, 28));
    Icon icon() => tester.widget<Icon>(find.byType(Icon));
    expect(icon().size, Sizes.iconRow);
    expect(icon().color, e.muted);

    await hover(tester, find.byType(IconBtn));
    expect(icon().color, e.fg);
    expect(decorationUnder(tester, find.byType(IconBtn)).color, e.hover);
  });

  testWidgets('tap presses it; without onTap it is disabled', (tester) async {
    var taps = 0;
    await pumpKit(
      tester,
      IconBtn(Glyphs.plus, tooltip: 'New agent', onTap: () => taps++),
    );
    await tester.tap(find.byType(IconBtn));
    expect(taps, 1);

    await pumpKit(tester, const IconBtn(Glyphs.plus, tooltip: 'New agent'));
    expect(tester.widget<Icon>(find.byType(Icon)).color, e.faint);
  });

  testWidgets('on the narrow layout the target is at least 44', (tester) async {
    await pumpKit(
      tester,
      IconBtn(Glyphs.plus, tooltip: 'New agent', onTap: () {}),
      width: 390,
    );
    expect(tester.getSize(find.byType(IconBtn)), const Size(44, 44));
  });
}
