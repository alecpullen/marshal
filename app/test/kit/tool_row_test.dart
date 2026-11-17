import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('verb, target and result are mono', (tester) async {
    await pumpKit(
      tester,
      const ToolRow(
        verb: 'read',
        target: 'internal/acp/stack.go',
        result: '148 lines',
      ),
    );
    expect(styleOf(tester, 'read').fontFamily, Ember.monoFamily);
    expect(
      styleOf(tester, 'internal/acp/stack.go').fontFamily,
      Ember.monoFamily,
    );
    expect(styleOf(tester, '148 lines').fontFamily, Ember.monoFamily);
  });

  testWidgets('only a failed result is err', (tester) async {
    await pumpKit(
      tester,
      const ToolRow(verb: 'shell', target: 'scripts/test.sh', result: 'ok'),
    );
    expect(styleOf(tester, 'ok').color, e.muted);

    await pumpKit(
      tester,
      const ToolRow(
        verb: 'shell',
        target: 'scripts/test.sh',
        result: 'exit 1',
        outcome: ToolOutcome.failed,
      ),
    );
    expect(styleOf(tester, 'exit 1').color, e.err);
  });

  testWidgets('a selected row is filled with the selected token', (
    tester,
  ) async {
    await pumpKit(
      tester,
      ToolRow(
        verb: 'edit',
        target: 'app/lib/src/ui/thread/transcript.dart',
        selected: true,
        onTap: () {},
      ),
    );
    final d = decorationUnder(tester, find.byType(ToolRow));
    expect(d.color, e.selected);
  });

  testWidgets('tapping a row calls back; a plain row is not a control', (
    tester,
  ) async {
    var taps = 0;
    await pumpKit(
      tester,
      ToolRow(
        verb: 'read',
        target: 'a.go',
        result: '1 line',
        onTap: () => taps++,
      ),
    );
    await tester.tap(find.text('a.go'));
    expect(taps, 1);

    await pumpKit(tester, const ToolRow(verb: 'read', target: 'a.go'));
    expect(find.byType(GestureDetector), findsNothing);
  });

  testWidgets('a long target ellipsizes rather than overflowing', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 140,
        child: ToolRow(
          verb: 'read',
          target: 'a/very/long/path/that/does/not/fit/anywhere.go',
        ),
      ),
    );
    expect(tester.takeException(), isNull);
  });
}
