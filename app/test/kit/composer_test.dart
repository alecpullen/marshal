import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('the field carries its hint and its chips', (tester) async {
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    await pumpKit(
      tester,
      SizedBox(
        width: 500,
        child: Composer(
          controller: controller,
          hint: 'Reply to the agent…',
          mode: 'Act',
          model: 'qwen3-coder',
          onSend: () {},
        ),
      ),
    );
    expect(find.text('Reply to the agent…'), findsOneWidget);
    expect(find.text('Act'), findsOneWidget);
    expect(find.text('qwen3-coder'), findsOneWidget);
    expect(find.text('Send'), findsOneWidget);
    expect(find.text('Stop'), findsNothing);
  });

  testWidgets('the chips lead and the actions trail', (tester) async {
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    await pumpKit(
      tester,
      SizedBox(
        width: 700,
        child: Composer(
          controller: controller,
          hint: 'Reply to the agent…',
          mode: 'Act',
          model: 'qwen3-coder',
          onSend: () {},
        ),
      ),
    );

    // The row spans the composer: the chips start at its left edge and the
    // action ends at its right, rather than the whole row floating in the
    // middle of a centring column.
    final composer = tester.getRect(find.byType(Composer));
    final mode = tester.getRect(
      find.ancestor(of: find.text('Act'), matching: find.byType(Pill)),
    );
    final send = tester.getRect(find.byType(Btn));
    expect(mode.left - composer.left, lessThanOrEqualTo(Space.s12));
    expect(composer.right - send.right, lessThanOrEqualTo(Space.s8));
  });

  testWidgets('on a narrow composer the actions keep the end', (tester) async {
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    Future<void> pump(double width) => pumpKit(
      tester,
      SizedBox(
        width: width,
        child: Composer(
          controller: controller,
          hint: 'Reply to the agent…',
          mode: 'Act',
          model: 'qwen3-coder',
          onSend: () {},
        ),
      ),
    );

    // A phone's width. Send holds the composer's end and the mode chip leads
    // it, as at 700: 390 still fits both groups on one row, because the chips
    // are given the row's spare width rather than sized to their content.
    await pump(390);
    final composer = tester.getRect(find.byType(Composer));
    final mode = tester.getRect(
      find.ancestor(of: find.text('Act'), matching: find.byType(Pill)),
    );
    final send = tester.getRect(
      find.ancestor(of: find.text('Send'), matching: find.byType(Btn)),
    );
    expect(composer.right - send.right, lessThanOrEqualTo(Space.s8));
    expect(mode.left - composer.left, lessThanOrEqualTo(Space.s12));
    // One row, not two: the actions sit beside the chips, not below them.
    expect(send.top, lessThan(mode.bottom));

    // Narrower still, the chips take a second run inside their half of the
    // row. The actions stay at the end — the defect was their landing at the
    // left when the group they shared a run with no longer fitted.
    await pump(320);
    final narrow = tester.getRect(find.byType(Composer));
    final narrowMode = tester.getRect(
      find.ancestor(of: find.text('Act'), matching: find.byType(Pill)),
    );
    final mic = tester.getRect(find.byType(IconBtn).last);
    final narrowSend = tester.getRect(
      find.ancestor(of: find.text('Send'), matching: find.byType(Btn)),
    );
    expect(mic.top, greaterThan(narrowMode.top), reason: 'the chips wrap');
    expect(narrow.right - narrowSend.right, lessThanOrEqualTo(Space.s8));
    expect(narrowSend.left, greaterThan(mic.right));
  });

  testWidgets('a running turn steers and offers Stop', (tester) async {
    var stops = 0;
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    await pumpKit(
      tester,
      SizedBox(
        width: 500,
        child: Composer(
          controller: controller,
          hint: 'Steer the agent…',
          running: true,
          onSend: () {},
          onStop: () => stops++,
        ),
      ),
    );
    expect(find.text('Steer'), findsOneWidget);
    expect(find.text('Send'), findsNothing);
    await tester.tap(find.text('Stop'));
    expect(stops, 1);
  });

  testWidgets('a read-only session is one line saying why', (tester) async {
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    await pumpKit(
      tester,
      SizedBox(
        width: 500,
        child: Composer(
          controller: controller,
          hint: 'Reply to the agent…',
          readOnly: true,
          readOnlyReason:
              'This session is in ask mode with no editing granted.',
        ),
      ),
    );
    expect(
      find.text('This session is in ask mode with no editing granted.'),
      findsOneWidget,
    );
    expect(
      styleOf(
        tester,
        'This session is in ask mode with no editing granted.',
      ).color,
      e.muted,
    );
    expect(find.byType(EditableText), findsNothing);
    expect(find.text('Send'), findsNothing);
  });

  testWidgets('every icon-only control is named by its tooltip', (
    tester,
  ) async {
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    await pumpKit(
      tester,
      SizedBox(
        width: 500,
        child: Composer(
          controller: controller,
          hint: 'Reply to the agent…',
          mode: 'Act',
          model: 'qwen3-coder',
          onSend: () {},
        ),
      ),
    );
    expect(find.byType(IconBtn), findsNWidgets(2));
    for (final button in tester.widgetList<IconBtn>(find.byType(IconBtn))) {
      expect(button.tooltip, isNotEmpty);
    }
    expect(hasTip(tester, find.text('Act'), 'Permission mode'), isTrue);
    expect(hasTip(tester, find.text('qwen3-coder'), 'Model'), isTrue);
  });

  testWidgets('the send button is disabled with no callback', (tester) async {
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    await pumpKit(
      tester,
      SizedBox(
        width: 500,
        child: Composer(controller: controller, hint: 'Reply to the agent…'),
      ),
    );
    final send = tester.widget<Btn>(find.byType(Btn));
    expect(send.onTap, isNull);
  });
}
