import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  testWidgets('words alone: outlined, round, caption in muted', (tester) async {
    await pumpKit(tester, const Pill('Draft'));
    final d = decorationUnder(tester, find.byType(Pill));
    expect(d.color, isNull);
    expect(borderColour(d), e.border);
    expect(d.borderRadius, BorderRadius.circular(Radii.full));
    expect(styleOf(tester, 'Draft').fontSize, e.caption.fontSize);
    expect(styleOf(tester, 'Draft').color, e.muted);
    expect(find.byType(Tip), findsNothing);
  });

  testWidgets('words with a tooltip', (tester) async {
    await pumpKit(
      tester,
      const Pill('Draft', tooltip: 'Not yet marked ready on the forge'),
    );
    expect(
      hasTip(tester, find.text('Draft'), 'Not yet marked ready on the forge'),
      isTrue,
    );
  });

  testWidgets('with an icon, the tooltip names it', (tester) async {
    await pumpKit(
      tester,
      const Pill.icon('main', icon: Glyphs.arrow_branch, tooltip: 'Base'),
    );
    expect(hasTip(tester, find.byType(Icon), 'Base'), isTrue);
    expect(tester.widget<Icon>(find.byType(Icon)).size, Sizes.iconInline);
  });

  testWidgets('mono sets an identifier in Geist Mono', (tester) async {
    await pumpKit(tester, const Pill('factory/voice', mono: true));
    expect(styleOf(tester, 'factory/voice').fontFamily, Ember.monoFamily);
  });

  testWidgets('a long label ellipsizes', (tester) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 60,
        child: Pill('qwen3.5-9b · local · loaded at 32k'),
      ),
    );
    expect(tester.takeException(), isNull);
  });
}
