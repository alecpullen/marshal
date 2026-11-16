import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('output is mono on the code fill', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: OutputBlock(text: 'ok  0.12s')),
    );
    expect(styleOf(tester, 'ok  0.12s').fontFamily, Ember.monoFamily);
    final d = decorationUnder(tester, find.byType(OutputBlock));
    expect(d.color, e.codeBg);
  });

  testWidgets('a capped block says so in words', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 400,
        child: OutputBlock(text: '…', truncated: true),
      ),
    );
    expect(find.text('Output truncated'), findsOneWidget);
    expect(styleOf(tester, 'Output truncated').color, e.muted);
  });

  testWidgets('an uncapped block says nothing extra', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: OutputBlock(text: 'ok')),
    );
    expect(find.text('Output truncated'), findsNothing);
  });

  testWidgets('newlines are kept', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(width: 400, child: OutputBlock(text: 'one\ntwo')),
    );
    expect(find.text('one\ntwo'), findsOneWidget);
  });
}
