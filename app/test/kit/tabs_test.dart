import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();
  const items = [
    TabItem('All', count: 9),
    TabItem('Blocking', count: 4),
    TabItem('To look at', count: 2),
    TabItem('Ready', count: 3),
  ];

  Color underline(WidgetTester tester, String label) {
    final d = decorationUnder(
      tester,
      find.ancestor(of: find.text(label), matching: find.byType(Container)),
    );
    return (d.border! as Border).bottom.color;
  }

  testWidgets('the current tab is underlined in accent, the rest are not', (
    tester,
  ) async {
    await pumpKit(tester, Tabs(tabs: items, index: 1, onChanged: (_) {}));
    expect(underline(tester, 'Blocking'), e.accent);
    expect(underline(tester, 'All'), e.none);
    expect(styleOf(tester, 'Blocking').fontWeight, e.bodyStrong.fontWeight);
    expect(styleOf(tester, 'All').color, e.muted);
  });

  testWidgets('counts sit beside their tab in mono', (tester) async {
    await pumpKit(tester, Tabs(tabs: items, index: 0, onChanged: (_) {}));
    expect(styleOf(tester, '9').fontFamily, Ember.monoFamily);
  });

  testWidgets('a tap chooses a tab', (tester) async {
    int? chosen;
    await pumpKit(
      tester,
      Tabs(tabs: items, index: 0, onChanged: (i) => chosen = i),
    );
    await tester.tap(find.text('Ready'));
    expect(chosen, 3);
  });

  testWidgets('a hovered tab brightens', (tester) async {
    await pumpKit(tester, Tabs(tabs: items, index: 0, onChanged: (_) {}));
    await hover(tester, find.text('Ready'));
    expect(styleOf(tester, 'Ready').color, e.fg);
  });

  testWidgets('tabs that do not fit wrap instead of scrolling sideways', (
    tester,
  ) async {
    await pumpKit(
      tester,
      SizedBox(
        width: 160,
        child: Tabs(tabs: items, index: 0, onChanged: (_) {}),
      ),
    );
    expect(tester.takeException(), isNull);
    expect(find.byType(Scrollable), findsNothing);
    expect(
      tester.getTopLeft(find.text('Ready')).dy,
      greaterThan(tester.getTopLeft(find.text('All')).dy),
    );
  });
}
