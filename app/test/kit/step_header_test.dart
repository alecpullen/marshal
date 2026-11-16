import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('the ordinal is mono, faint, and the headline is strong', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const StepHeader(number: '2', headline: 'Remove the dead fixtures'),
    );
    final number = styleOf(tester, '2');
    expect(number.fontFamily, Ember.monoFamily);
    expect(number.color, e.faint);
    final headline = styleOf(tester, 'Remove the dead fixtures');
    expect(headline.fontWeight, e.bodyStrong.fontWeight);
  });

  testWidgets('the duration is mono, only when there is one', (tester) async {
    await pumpKit(
      tester,
      const StepHeader(number: '1', headline: 'Mapped', took: '41s'),
    );
    expect(styleOf(tester, '41s').fontFamily, Ember.monoFamily);

    await pumpKit(tester, const StepHeader(number: '1', headline: 'Mapped'));
    expect(find.text('41s'), findsNothing);
  });

  testWidgets('a live step shows the running dot, which names its status', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const StepHeader(
        number: '3',
        headline: 'Waiting for your call',
        live: true,
      ),
    );
    expect(find.byType(Dot), findsOneWidget);
    expect(tester.widget<Dot>(find.byType(Dot)).status, Status.running);
  });

  testWidgets('a foldable step shows a disclosure with its own tooltip', (
    tester,
  ) async {
    await pumpKit(
      tester,
      StepHeader(
        number: '2',
        headline: 'Removed them',
        foldable: true,
        onTap: () {},
      ),
    );
    expect(hasTip(tester, find.byType(Icon), 'Fold this step'), isTrue);

    await pumpKit(
      tester,
      StepHeader(
        number: '2',
        headline: 'Removed them',
        foldable: true,
        folded: true,
        onTap: () {},
      ),
    );
    expect(hasTip(tester, find.byType(Icon), 'Unfold this step'), isTrue);
  });

  testWidgets('tapping a step calls back', (tester) async {
    var taps = 0;
    await pumpKit(
      tester,
      StepHeader(number: '1', headline: 'Mapped', onTap: () => taps++),
    );
    await tester.tap(find.text('Mapped'));
    expect(taps, 1);
  });

  testWidgets('with no tap it is plain text, not a control', (tester) async {
    await pumpKit(tester, const StepHeader(number: '1', headline: 'Mapped'));
    expect(find.byType(GestureDetector), findsNothing);
  });
}
