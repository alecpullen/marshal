import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  Future<void> pump(
    WidgetTester tester, {
    bool selected = false,
    VoidCallback? onTap,
    ThreadKind kind = ThreadKind.workItem,
  }) => pumpKit(
    tester,
    SizedBox(
      width: 236,
      child: ThreadRow(
        status: Status.needsYou,
        title: 'Reliable session recovery',
        kind: kind,
        selected: selected,
        onTap: onTap,
      ),
    ),
  );

  testWidgets('dot, title, kind icon named by its tooltip', (tester) async {
    for (final kind in ThreadKind.values) {
      await pump(tester, kind: kind);
      expect(tester.widget<Dot>(find.byType(Dot)).status, Status.needsYou);
      expect(find.byIcon(kind.icon), findsOneWidget);
      expect(hasTip(tester, find.byIcon(kind.icon), kind.word), isTrue);
      expect(
        tester.widget<Icon>(find.byIcon(kind.icon)).color,
        e.muted,
        reason: 'kinds are never told apart by colour',
      );
    }
    expect(tester.getSize(find.byType(ThreadRow)).height, Sizes.row);
  });

  testWidgets("a session row's kind has its tooltip", (tester) async {
    await pump(tester, kind: ThreadKind.session);
    expect(find.byIcon(ThreadKind.session.icon), findsOneWidget);
    expect(
      hasTip(tester, find.byIcon(ThreadKind.session.icon), 'Session'),
      isTrue,
    );
  });

  testWidgets('selected fills with selected; hover with hover', (tester) async {
    await pump(tester, selected: true, onTap: () {});
    expect(decorationUnder(tester, find.byType(ThreadRow)).color, e.selected);

    await pump(tester, onTap: () {});
    expect(decorationUnder(tester, find.byType(ThreadRow)).color, isNull);
    await hover(tester, find.text('Reliable session recovery'));
    expect(decorationUnder(tester, find.byType(ThreadRow)).color, e.hover);
  });

  testWidgets('a tap opens it; a long title ellipsizes', (tester) async {
    var opened = 0;
    await pump(tester, onTap: () => opened++);
    await tester.tap(find.byType(ThreadRow));
    expect(opened, 1);

    await pumpKit(
      tester,
      const SizedBox(
        width: 120,
        child: ThreadRow(
          status: Status.idle,
          title: 'A thread whose title runs on for a long while',
          kind: ThreadKind.agent,
        ),
      ),
    );
    expect(tester.takeException(), isNull);
  });
}
