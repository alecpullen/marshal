import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  Sheet sheet({List<Widget>? actions}) => Sheet(
    title: 'Start work',
    actions:
        actions ??
        [
          Btn.primary('Start agent', onTap: () {}),
          Btn('Keep editing', onTap: () {}),
        ],
    child: const Text('What do you want done?'),
  );

  Future<void> open(
    WidgetTester tester, {
    double width = 1200,
    Sheet? content,
  }) async {
    await pumpKit(
      tester,
      Builder(
        builder: (context) => Btn(
          'Open',
          onTap: () => Sheet.show<void>(context, content ?? sheet()),
        ),
      ),
      width: width,
      height: 800,
    );
    await tester.tap(find.text('Open'));
    await tester.pump();
  }

  testWidgets('dims what is behind it with the scrim', (tester) async {
    await open(tester);
    final barrier = tester.widget<ModalBarrier>(find.byType(ModalBarrier).last);
    expect(barrier.color, tokens().scrim);
    expect(barrier.color!.a, greaterThan(0));
  });

  testWidgets('a dialog in the middle of a wide window', (tester) async {
    await open(tester);
    expect(find.text('Start work'), findsOneWidget);
    expect(find.text('What do you want done?'), findsOneWidget);
    final title = tokens().title;
    expect(styleOf(tester, 'Start work').fontSize, title.fontSize);
    final centre = tester.getCenter(find.byType(Sheet));
    expect(centre.dx, closeTo(600, 1));
    expect(tester.getSize(find.byType(Sheet)).width, Sheet.maxWidth);
  });

  testWidgets('a bottom sheet across a narrow window', (tester) async {
    await open(tester, width: 390);
    final rect = tester.getRect(find.byType(Sheet));
    expect(rect.bottom, closeTo(800, 2));
    expect(rect.width, closeTo(390, 2));
    expect(tester.takeException(), isNull);
  });

  testWidgets('the close button, Escape and a click outside close it', (
    tester,
  ) async {
    await open(tester);
    await tester.tap(find.byType(IconBtn));
    await tester.pump();
    expect(find.text('Start work'), findsNothing);

    await tester.tap(find.text('Open'));
    await tester.pump();
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pump();
    expect(find.text('Start work'), findsNothing);

    await tester.tap(find.text('Open'));
    await tester.pump();
    await tester.tapAt(const Offset(10, 790));
    await tester.pump();
    expect(find.text('Start work'), findsNothing);
  });

  testWidgets('the close button is named', (tester) async {
    await open(tester);
    expect(tester.widget<IconBtn>(find.byType(IconBtn)).tooltip, 'Close');
  });

  testWidgets('a primary action that is not first is refused', (tester) async {
    await open(
      tester,
      content: sheet(
        actions: [
          Btn('Keep editing', onTap: () {}),
          Btn.primary('Start agent', onTap: () {}),
        ],
      ),
    );
    expect(tester.takeException(), isA<AssertionError>());
  });
}
