import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  Future<void> pumpPage(
    WidgetTester tester, {
    double width = 1440,
    PageWidth pageWidth = PageWidth.full,
    List<Widget>? actions,
  }) async {
    tester.view.physicalSize = Size(width, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      EmberApp(
        brightness: Brightness.light,
        home: PageBody(
          title: 'Overview',
          subtitle: '8 threads across 3 projects',
          width: pageWidth,
          actions:
              actions ??
              [Btn.primary('New', onTap: () {}), Btn('Refresh', onTap: () {})],
          children: const [SizedBox(height: 40, child: Placeholder())],
        ),
      ),
    );
  }

  testWidgets('title in heading, subtitle muted, padded 24 and 20', (
    tester,
  ) async {
    await pumpPage(tester, width: 1000);
    expect(styleOf(tester, 'Overview').fontSize, e.heading.fontSize);
    expect(styleOf(tester, '8 threads across 3 projects').color, e.muted);
    final title = tester.getTopLeft(find.text('Overview'));
    expect(title.dy, Layout.pageTop);
    expect(title.dx, Layout.pageSide);
  });

  testWidgets('content stops at its maximum on a wide window', (tester) async {
    await pumpPage(tester);
    expect(tester.getSize(find.byType(Placeholder)).width, Layout.page);

    await pumpPage(tester, pageWidth: PageWidth.list);
    expect(tester.getSize(find.byType(Placeholder)).width, Layout.pageList);
  });

  testWidgets('actions sit beside the title, and below it when narrow', (
    tester,
  ) async {
    await pumpPage(tester, width: 1000);
    expect(
      tester.getTopLeft(find.text('New')).dy,
      lessThan(tester.getBottomLeft(find.text('Overview')).dy),
    );

    await pumpPage(tester, width: 390);
    expect(
      tester.getTopLeft(find.text('New')).dy,
      greaterThan(tester.getBottomLeft(find.text('Overview')).dy),
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('a second primary action, or one not first, is refused', (
    tester,
  ) async {
    await pumpPage(
      tester,
      actions: [
        Btn('Refresh', onTap: () {}),
        Btn.primary('New', onTap: () {}),
      ],
    );
    expect(tester.takeException(), isA<AssertionError>());
  });
}
