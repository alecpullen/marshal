import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  Widget rows(List<RowItem> items) => SizedBox(width: 600, child: Rows(items));

  testWidgets(
    'name, description and value in one flush box, hairlines between',
    (tester) async {
      await pumpKit(
        tester,
        rows(const [
          RowItem(name: 'Hosts', description: 'Where work runs', value: '3'),
          RowItem(name: 'Network', description: 'What agents may reach'),
          RowItem(name: 'Secrets', description: 'Kept out of containers'),
        ]),
      );
      expect(find.byType(Box), findsOneWidget);
      expect(find.byType(Hairline), findsNWidgets(2));
      expect(styleOf(tester, 'Hosts').fontWeight, e.bodyStrong.fontWeight);
      expect(styleOf(tester, 'Where work runs').color, e.muted);
      expect(styleOf(tester, '3').color, e.muted);
    },
  );

  testWidgets('a row that opens has a chevron and takes a tap', (tester) async {
    var opened = 0;
    await pumpKit(
      tester,
      rows([
        RowItem(
          name: 'Hosts',
          description: 'Where work runs',
          onTap: () => opened++,
        ),
        const RowItem(name: 'Usage', description: 'Cost and tokens'),
      ]),
    );
    expect(find.byIcon(Glyphs.chevron_right), findsOneWidget);
    await tester.tap(find.text('Hosts'));
    expect(opened, 1);

    await hover(tester, find.text('Hosts'));
    final fill = tester
        .widgetList<Container>(
          find.ancestor(
            of: find.text('Hosts'),
            matching: find.byType(Container),
          ),
        )
        .map((c) => c.color)
        .whereType<Color>();
    expect(fill, contains(e.hover));
  });

  testWidgets('a planned row is the planned state: disabled, saying so', (
    tester,
  ) async {
    await pumpKit(
      tester,
      rows(const [
        RowItem(name: 'This Mac', description: 'Desktop notifications'),
        RowItem.planned(name: 'Signal', description: 'Not available yet'),
      ]),
    );
    expect(find.byType(StateMessage), findsOneWidget);
    expect(find.text('Planned'), findsOneWidget);
    expect(styleOf(tester, 'Signal').color, e.muted);
  });
}
