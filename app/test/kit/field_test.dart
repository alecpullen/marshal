import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  Color edge(WidgetTester tester) =>
      borderColour(decorationUnder(tester, find.byType(Field)));

  Future<void> pump(WidgetTester tester, Field field) =>
      pumpKit(tester, SizedBox(width: 320, child: field));

  testWidgets('the label sits above a bordered input 30 high', (tester) async {
    await pump(tester, const Field(label: 'Branch', hint: 'main'));
    expect(
      tester.getBottomLeft(find.text('Branch')).dy,
      lessThanOrEqualTo(tester.getTopLeft(find.byType(EditableText)).dy),
    );
    expect(edge(tester), e.border);
    expect(styleOf(tester, 'Branch').color, e.muted);
    final box = find
        .ancestor(
          of: find.byType(EditableText),
          matching: find.byType(Container),
        )
        .first;
    expect(tester.getSize(box).height, Sizes.row);
  });

  testWidgets('typing reports each change and the submit', (tester) async {
    final changes = <String>[];
    String? submitted;
    await pump(
      tester,
      Field(
        label: 'Branch',
        onChanged: changes.add,
        onSubmitted: (v) => submitted = v,
      ),
    );
    await tester.enterText(find.byType(EditableText), 'factory/x');
    await tester.testTextInput.receiveAction(TextInputAction.done);
    expect(changes.last, 'factory/x');
    expect(submitted, 'factory/x');
  });

  testWidgets('focus draws the border in accent', (tester) async {
    await pump(tester, const Field(label: 'Branch'));
    await tester.tap(find.byType(EditableText));
    await tester.pump();
    expect(edge(tester), e.accent);
  });

  testWidgets('an error is in err, and in words under it', (tester) async {
    await pump(
      tester,
      const Field(label: 'Branch', error: 'That branch does not exist'),
    );
    expect(edge(tester), e.err);
    expect(styleOf(tester, 'That branch does not exist').color, e.err);
  });

  testWidgets('disabled takes no input', (tester) async {
    await pump(tester, const Field(label: 'Branch', enabled: false));
    expect(
      tester.widget<EditableText>(find.byType(EditableText)).readOnly,
      isTrue,
    );
  });
}
