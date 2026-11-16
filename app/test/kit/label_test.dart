import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('uppercase, in the label role, muted', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: Label('Needs you')),
    );
    expect(find.text('NEEDS YOU'), findsOneWidget);
    final style = styleOf(tester, 'NEEDS YOU');
    expect(style.fontSize, e.label.fontSize);
    expect(style.fontWeight, e.label.fontWeight);
    expect(style.letterSpacing, e.label.letterSpacing);
    expect(style.color, e.muted);
  });

  testWidgets('with a count, in mono', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: Label('Needs you', count: 6)),
    );
    expect(styleOf(tester, '6').fontFamily, Ember.monoFamily);
  });

  testWidgets('with one trailing link that opens', (tester) async {
    var opened = 0;
    await pumpKit(
      tester,
      SizedBox(
        width: 400,
        child: Label(
          'Needs you',
          count: 6,
          link: LabelLink('Open inbox', onTap: () => opened++),
        ),
      ),
    );
    expect(
      tester.getCenter(find.text('Open inbox')).dx,
      greaterThan(tester.getCenter(find.text('NEEDS YOU')).dx),
    );
    await tester.tap(find.text('Open inbox'));
    expect(opened, 1);

    await hover(tester, find.text('Open inbox'));
    expect(styleOf(tester, 'Open inbox').color, e.fg);
  });
}
