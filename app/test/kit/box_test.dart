import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';
import 'package:marshal_app/src/ui/kit/press.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('each tone has its border colour, no fill and no shadow', (
    tester,
  ) async {
    for (final (tone, colour) in [
      (BoxTone.plain, e.border),
      (BoxTone.attention, e.attention),
      (BoxTone.failing, e.failing),
    ]) {
      await pumpKit(tester, Box(tone: tone, child: const Text('x')));
      final d = decorationUnder(tester, find.byType(Box));
      expect(borderColour(d), colour, reason: '$tone');
      expect(d.color, isNull, reason: '$tone has no fill');
      expect(d.boxShadow, isNull, reason: '$tone has no shadow');
      expect(d.borderRadius, BorderRadius.circular(Radii.panel));
    }
  });

  testWidgets('Box pads by 16; Box.flush does not', (tester) async {
    await pumpKit(tester, const Box(child: SizedBox(width: 10, height: 10)));
    // 16 of padding and 1 of border on each side.
    expect(tester.getSize(find.byType(Box)), const Size(44, 44));

    await pumpKit(
      tester,
      const Box.flush(child: SizedBox(width: 10, height: 10)),
    );
    expect(tester.getSize(find.byType(Box)), const Size(12, 12));
  });

  testWidgets('a clickable box fills on hover and takes a tap', (tester) async {
    var taps = 0;
    await pumpKit(tester, Box(onTap: () => taps++, child: const Text('open')));
    expect(decorationUnder(tester, find.byType(Box)).color, isNull);

    await hover(tester, find.text('open'));
    expect(decorationUnder(tester, find.byType(Box)).color, e.hover);

    await tester.tap(find.text('open'));
    expect(taps, 1);
  });

  testWidgets('a box without onTap is not a button', (tester) async {
    await pumpKit(tester, const Box(child: Text('fact')));
    expect(find.byType(Press), findsNothing);
  });
}
