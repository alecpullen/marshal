import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final picked = <String>[];

  Widget menu() => Menu(
    choices: [
      MenuChoice(
        'Investigate only',
        'Report the cause, change nothing',
        onSelect: () => picked.add('investigate'),
      ),
      MenuChoice(
        'Fix with a note',
        'Tell the worker what you suspect',
        onSelect: () => picked.add('note'),
      ),
    ],
    anchor: (context, toggle) => IconBtn(
      Glyphs.chevron_down,
      tooltip: 'Other ways to send this',
      onTap: toggle,
    ),
  );

  setUp(picked.clear);

  testWidgets('closed until its anchor opens it', (tester) async {
    await pumpKit(tester, menu());
    expect(find.text('Investigate only'), findsNothing);
    await tester.tap(find.byType(IconBtn));
    await tester.pump();
    expect(find.text('Investigate only'), findsOneWidget);
    expect(find.text('Report the cause, change nothing'), findsOneWidget);
  });

  testWidgets('each item is a name and one line of explanation', (
    tester,
  ) async {
    await pumpKit(tester, menu());
    await tester.tap(find.byType(IconBtn));
    await tester.pump();
    final e = tokens();
    expect(styleOf(tester, 'Fix with a note').color, e.fg);
    expect(styleOf(tester, 'Tell the worker what you suspect').color, e.muted);
    expect(
      tester.getTopLeft(find.text('Investigate only')).dy,
      greaterThan(tester.getBottomLeft(find.byType(IconBtn)).dy),
      reason: 'it opens under its anchor',
    );
  });

  testWidgets('a choice acts and closes the menu', (tester) async {
    await pumpKit(tester, menu());
    await tester.tap(find.byType(IconBtn));
    await tester.pump();
    await tester.tap(find.text('Fix with a note'));
    await tester.pump();
    expect(picked, ['note']);
    expect(find.text('Fix with a note'), findsNothing);
  });

  testWidgets('Escape and a click outside close it without a choice', (
    tester,
  ) async {
    await pumpKit(tester, menu());
    await tester.tap(find.byType(IconBtn));
    // The popover takes focus on the frame after it opens.
    await tester.pump();
    await tester.pump();
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pump();
    expect(find.text('Investigate only'), findsNothing);

    await tester.tap(find.byType(IconBtn));
    await tester.pump();
    await tester.tapAt(const Offset(700, 500));
    await tester.pump();
    expect(find.text('Investigate only'), findsNothing);
    expect(picked, isEmpty);
  });

  testWidgets('near the right edge it stays on screen', (tester) async {
    await pumpKit(
      tester,
      Align(alignment: Alignment.topRight, child: menu()),
      width: 390,
    );
    await tester.tap(find.byType(IconBtn));
    await tester.pump();
    expect(
      tester.getTopRight(find.text('Investigate only')).dx,
      lessThanOrEqualTo(390),
    );
    expect(tester.takeException(), isNull);
  });
}
