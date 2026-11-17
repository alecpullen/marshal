import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('a paragraph is read at the reading role', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 400,
        child: Narration(text: 'There are 96 fixture files.'),
      ),
    );
    final root =
        tester
                .widget<Text>(
                  find.descendant(
                    of: find.byType(Narration),
                    matching: find.byType(Text),
                  ),
                )
                .textSpan!
            as TextSpan;
    final first = root.children!.first as TextSpan;
    expect(first.style!.fontSize, e.reading.fontSize);
  });

  testWidgets('strong and inline code take their styles from the tokens', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 400,
        child: Narration(text: 'There are **96** files and `82` load.'),
      ),
    );
    final rich = tester.widget<Text>(
      find.descendant(of: find.byType(Narration), matching: find.byType(Text)),
    );
    final spans = (rich.textSpan! as TextSpan).children!.cast<TextSpan>();
    final strong = spans.firstWhere((span) => span.text == '96');
    final code = spans.firstWhere((span) => span.text == '82');
    expect(strong.style!.fontWeight, e.bodyStrong.fontWeight);
    expect(code.style!.fontFamily, Ember.monoFamily);
  });

  testWidgets('a list keeps its markers and every item', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 400,
        child: Narration(text: '- one\n- two\n\n1. first\n2. second'),
      ),
    );
    expect(find.text('•'), findsNWidgets(2));
    expect(find.text('1.'), findsOneWidget);
    expect(find.text('2.'), findsOneWidget);
  });

  testWidgets('a fenced block is drawn as output, not as prose', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 400,
        child: Narration(text: 'Ran it:\n\n```\nok  0.12s\n```'),
      ),
    );
    expect(find.byType(OutputBlock), findsOneWidget);
  });

  testWidgets('plain text with no Markdown is still shown', (tester) async {
    await pumpKit(tester, const Narration(text: 'Just words'));
    expect(find.text('Just words'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });
}
