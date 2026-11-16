import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();
  const checks = [
    GateCheck('Build', CheckState.passed, detail: '4.1s'),
    GateCheck('Tests', CheckState.failed, detail: '412 passed'),
    GateCheck('Usability', CheckState.running, detail: 'running'),
    GateCheck('Release config', CheckState.pending),
  ];

  testWidgets('one row per check, in order, coloured by state', (tester) async {
    await pumpKit(tester, const GateList(checks));
    final icons = tester
        .widgetList<Icon>(
          find.descendant(
            of: find.byType(GateList),
            matching: find.byType(Icon),
          ),
        )
        .toList();
    expect(icons.map((i) => i.icon), [
      CheckState.passed.icon,
      CheckState.failed.icon,
      CheckState.running.icon,
      CheckState.pending.icon,
    ]);
    expect(icons.map((i) => i.color), [e.ok, e.err, e.fg, e.border]);

    final tops = [
      for (final check in checks) tester.getTopLeft(find.text(check.name)).dy,
    ];
    expect(tops, orderedEquals([...tops]..sort()));
  });

  testWidgets('each row names its check and its state', (tester) async {
    await pumpKit(tester, const GateList(checks));
    final tips = tester
        .widgetList<Tip>(find.byType(Tip))
        .map((tip) => tip.message)
        .toList();
    expect(tips, [
      'Build · passed',
      'Tests · failed',
      'Usability · running',
      'Release config · pending',
    ]);
  });

  testWidgets('a detail is mono, and a check without one draws none', (
    tester,
  ) async {
    await pumpKit(tester, const GateList(checks));
    expect(styleOf(tester, '4.1s').fontFamily, Ember.monoFamily);
    expect(find.text('running'), findsOneWidget);

    await pumpKit(
      tester,
      const GateList([GateCheck('Ran 2m ago', CheckState.passed)]),
    );
    expect(
      tester
          .widgetList<Text>(
            find.descendant(
              of: find.byType(GateList),
              matching: find.byType(Text),
            ),
          )
          .length,
      1,
    );
  });

  testWidgets('a long name truncates rather than overflowing', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 260,
        child: GateList([
          GateCheck(
            'build (linux, cgo) with the whole test suite and the release config',
            CheckState.passed,
            detail: '2m 11s',
          ),
        ]),
      ),
    );
    expect(tester.takeException(), isNull);
  });
}
