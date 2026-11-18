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

  testWidgets('the search field has no label above it', (tester) async {
    final handle = tester.ensureSemantics();
    await pump(tester, const Field.search(hint: 'Search or jump to'));

    // No label is drawn and no room is left for one: the border starts at the
    // field's own top.
    expect(find.text('Search'), findsNothing);
    final box = find
        .ancestor(
          of: find.byType(EditableText),
          matching: find.byType(Container),
        )
        .first;
    expect(tester.getSize(box).height, Sizes.row);
    expect(tester.getTopLeft(box).dy, tester.getTopLeft(find.byType(Field)).dy);

    // A magnifying glass leads the input, in muted, with Space.s8 before the
    // text.
    final mark = find.byIcon(Glyphs.search);
    expect(mark, findsOneWidget);
    final icon = tester.widget<Icon>(mark);
    expect(icon.size, Sizes.iconRow);
    expect(icon.color, e.muted);
    final input = tester.getTopLeft(find.byType(EditableText)).dx;
    expect(input - tester.getTopRight(mark).dx, Space.s8);
    expect(tester.getTopLeft(mark).dx, lessThan(input));

    // The mark names it for assistive technology, as a label would (§9): the
    // field's semantics label leads with `Search`, and its hint follows.
    expect(find.bySemanticsLabel(RegExp('^Search')), findsOneWidget);
    handle.dispose();
  });

  testWidgets('disabled takes no input', (tester) async {
    await pump(tester, const Field(label: 'Branch', enabled: false));
    expect(
      tester.widget<EditableText>(find.byType(EditableText)).readOnly,
      isTrue,
    );
  });
}
