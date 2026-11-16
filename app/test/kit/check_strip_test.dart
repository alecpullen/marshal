import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();
  const checks = [
    Check('build', CheckState.passed),
    Check('lint', CheckState.failed),
    Check('test', CheckState.running),
    Check('e2e', CheckState.pending),
  ];

  testWidgets('one block per check, in order, coloured by state', (
    tester,
  ) async {
    await pumpKit(tester, const CheckStrip(checks));
    final colours = [
      for (final d in decorationsUnder(tester, find.byType(CheckStrip)))
        d.color,
    ];
    expect(colours, [e.ok, e.err, e.fg, e.border]);

    final xs = [
      for (final c in tester.widgetList<Container>(
        find.descendant(
          of: find.byType(CheckStrip),
          matching: find.byType(Container),
        ),
      ))
        c,
    ].map((c) => tester.getTopLeft(find.byWidget(c)).dx).toList();
    expect(xs, orderedEquals([...xs]..sort()));
  });

  testWidgets('each block names its check and state', (tester) async {
    await pumpKit(tester, const CheckStrip(checks));
    final tips = tester
        .widgetList<Tip>(find.byType(Tip))
        .map((t) => t.message)
        .toList();
    expect(tips, [
      'build · passed',
      'lint · failed',
      'test · running',
      'e2e · pending',
    ]);
  });

  testWidgets('a long strip wraps rather than overflowing', (tester) async {
    await pumpKit(
      tester,
      SizedBox(
        width: 40,
        child: CheckStrip([
          for (var i = 0; i < 12; i++) Check('c$i', CheckState.passed),
        ]),
      ),
    );
    expect(tester.takeException(), isNull);
  });
}
