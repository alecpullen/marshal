import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  testWidgets('its age carries the exact time in a tip', (tester) async {
    await pumpKit(
      tester,
      const ActivityRow(
        who: 'The implementer',
        text: 'started round 1',
        age: '8m',
        ageTip: '2026-11-17 11:52 UTC',
      ),
    );
    expect(find.text('8m'), findsOneWidget);
    expect(
      hasTip(tester, find.text('8m'), '2026-11-17 11:52 UTC'),
      isTrue,
      reason: 'the exact time belongs behind the relative one',
    );
  });

  testWidgets('without an exact time it draws no tip', (tester) async {
    await pumpKit(
      tester,
      const ActivityRow(
        who: 'You',
        text: 'revision 1 was approved',
        age: '12m',
      ),
    );
    expect(find.text('12m'), findsOneWidget);
    expect(
      find.ancestor(of: find.text('12m'), matching: find.byType(Tip)),
      findsNothing,
    );
  });
}
