import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('the prompt reads at the reading role, right-aligned', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 500,
        child: TurnPrompt(text: 'Clean up the fixtures'),
      ),
    );
    final style = styleOf(tester, 'Clean up the fixtures');
    expect(style.fontSize, e.reading.fontSize);
    expect(style.height, e.reading.height);
    final align = tester.widget<Align>(
      find.descendant(
        of: find.byType(TurnPrompt),
        matching: find.byType(Align),
      ),
    );
    expect(align.alignment, Alignment.centerRight);
  });

  testWidgets('it is outlined by the one border and has no fill', (
    tester,
  ) async {
    await pumpKit(tester, const TurnPrompt(text: 'Hello'));
    final d = decorationUnder(tester, find.byType(TurnPrompt));
    expect(d.color, isNull);
    expect(borderColour(d), e.border);
    expect(d.borderRadius, BorderRadius.circular(Radii.control));
  });

  testWidgets('two lines stay two lines', (tester) async {
    await pumpKit(tester, const TurnPrompt(text: 'one\ntwo'));
    expect(find.text('one\ntwo'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });
}
