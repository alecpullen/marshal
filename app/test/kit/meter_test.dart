import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  Color fill(WidgetTester tester) => tester
      .widget<ColoredBox>(
        find.descendant(
          of: find.byType(FractionallySizedBox),
          matching: find.byType(ColoredBox),
        ),
      )
      .color;

  testWidgets('the bar is muted, as wide as the fraction', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 200,
        child: Meter(label: 'studio-box', value: '6 of 8 slots', fraction: .75),
      ),
    );
    expect(fill(tester), e.muted);
    expect(
      tester
          .widget<FractionallySizedBox>(find.byType(FractionallySizedBox))
          .widthFactor,
      .75,
    );
    expect(styleOf(tester, '6 of 8 slots').fontFamily, Ember.monoFamily);
  });

  testWidgets('a fault is err, in the bar and the value', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 200,
        child: Meter.fault(
          label: 'vps-syd',
          value: 'unreachable 4m',
          fraction: 0,
        ),
      ),
    );
    expect(fill(tester), e.err);
    expect(styleOf(tester, 'unreachable 4m').color, e.err);
  });

  testWidgets('the bar has a tooltip; the fraction is clamped', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 200,
        child: Meter(label: 'Disk', value: '130 of 120 GB', fraction: 1.2),
      ),
    );
    expect(
      tester
          .widget<FractionallySizedBox>(find.byType(FractionallySizedBox))
          .widthFactor,
      1,
    );
    expect(
      hasTip(tester, find.byType(FractionallySizedBox), '100% used'),
      isTrue,
    );
  });
}
