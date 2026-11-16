import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('a running row shows the running dot and both lines', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 400,
        child: LiveRow(headline: 'Running the tests', detail: 'ok  0.12s'),
      ),
    );
    expect(find.byType(Dot), findsOneWidget);
    expect(tester.widget<Dot>(find.byType(Dot)).status, Status.running);
    expect(
      styleOf(tester, 'Running the tests').fontWeight,
      e.bodyStrong.fontWeight,
    );
    expect(styleOf(tester, 'ok  0.12s').color, e.muted);
  });

  testWidgets('mono details are output, not prose', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 400,
        child: LiveRow(headline: 'Running', detail: 'line one', mono: true),
      ),
    );
    expect(styleOf(tester, 'line one').fontFamily, Ember.monoFamily);
  });

  testWidgets('a failed row says failed, and its dot does too', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 400,
        child: LiveRow(headline: 'The command failed', failed: true),
      ),
    );
    expect(tester.widget<Dot>(find.byType(Dot)).status, Status.failed);
  });

  testWidgets('a row with no detail is one line', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: LiveRow(headline: 'Thinking')),
    );
    expect(find.byType(Text), findsOneWidget);
  });
}
