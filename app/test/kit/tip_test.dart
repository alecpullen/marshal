import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  testWidgets('shows after a short hover, drawn in the tokens', (tester) async {
    await pumpKit(
      tester,
      const Tip(message: 'Asked at 14:02', child: Text('12m')),
    );
    await hover(tester, find.text('12m'));
    expect(find.text('Asked at 14:02'), findsNothing);

    await tester.pump(const Duration(milliseconds: 600));
    expect(find.text('Asked at 14:02'), findsOneWidget);
    final e = tokens();
    expect(styleOf(tester, 'Asked at 14:02').color, e.fg);
    final bubble = decorationUnder(
      tester,
      find
          .ancestor(
            of: find.text('Asked at 14:02'),
            matching: find.byType(ConstrainedBox),
          )
          .first,
    );
    expect(bubble.color, e.bg);
    expect(borderColour(bubble), e.border);
  });

  testWidgets('it does not animate in', (tester) async {
    await pumpKit(
      tester,
      const Tip(message: 'Asked at 14:02', child: Text('12m')),
    );
    await hover(tester, find.text('12m'));
    await tester.pump(const Duration(milliseconds: 600));
    expect(tester.hasRunningAnimations, isFalse);
  });

  testWidgets('it is the message for assistive technology', (tester) async {
    final handle = tester.ensureSemantics();
    await pumpKit(
      tester,
      const Tip(message: 'Asked at 14:02', child: Text('12m')),
    );
    expect(
      tester.getSemantics(find.text('12m')),
      matchesSemantics(tooltip: 'Asked at 14:02', label: '12m'),
    );
    handle.dispose();
  });
}
