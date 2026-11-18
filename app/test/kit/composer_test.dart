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

  testWidgets('no mode chip when the mode is unknown', (tester) async {
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    await pumpKit(
      tester,
      SizedBox(
        width: 500,
        child: Composer(
          controller: controller,
          hint: 'Reply to the agent…',
          mode: null,
          onSend: () {},
        ),
      ),
    );
    // A mode nobody reported is no fact to state: the chip and its tooltip
    // are both left out, and the field still sends.
    expect(find.byType(Pill), findsNothing);
    expect(find.byTooltip('Permission mode'), findsNothing);
    expect(find.text('Send'), findsOneWidget);
  });

  testWidgets('the mode chip defaults to Ask', (tester) async {
    final controller = TextEditingController();
    addTearDown(controller.dispose);
    await pumpKit(
      tester,
      SizedBox(
        width: 500,
        child: Composer(controller: controller, hint: 'Reply to the agent…'),
      ),
    );
    // An omitted mode is the default, not an unknown one: every caller that
    // says nothing still draws the chip.
    expect(find.text('Ask'), findsOneWidget);
    expect(hasTip(tester, find.text('Ask'), 'Permission mode'), isTrue);
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
