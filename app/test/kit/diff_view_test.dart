import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  const diff =
      'diff --git a/lib/main.dart b/lib/main.dart\n'
      '--- a/lib/main.dart\n'
      '+++ b/lib/main.dart\n'
      '@@ -1,3 +1,3 @@\n'
      ' one\n'
      '-two\n'
      '+three\n'
      ' four\n';

  test('the parser reads a unified diff into its lines', () {
    final lines = parseDiff(diff);
    expect(lines.map((l) => l.kind), [
      DiffLineKind.header,
      DiffLineKind.header,
      DiffLineKind.header,
      DiffLineKind.hunk,
      DiffLineKind.context,
      DiffLineKind.removed,
      DiffLineKind.added,
      DiffLineKind.context,
    ]);
    // The hunk's ranges are the file's line numbers, one per side.
    final context = lines[4];
    expect((context.oldLine, context.newLine), (1, 1));
    expect((lines[5].oldLine, lines[5].newLine), (2, null));
    expect((lines[6].oldLine, lines[6].newLine), (null, 2));
    expect((lines[7].oldLine, lines[7].newLine), (3, 3));
    // A removed line is a comment on the old side, an added one on the new.
    expect(lines[5].side, DiffSide.before);
    expect(lines[6].side, DiffSide.after);
    expect(context.side, isNull);
  });

  test('a body line without a marker is read as context, not dropped', () {
    final lines = parseDiff('not a diff at all');
    expect(lines.single.kind, DiffLineKind.context);
    expect(lines.single.text, 'not a diff at all');
  });

  testWidgets(
    'a removed line is on diffRemoved and an added one on diffAdded',
    (tester) async {
      final e = tokens();
      await pumpKit(tester, const DiffView(diff: diff));
      final fills = [
        for (final d in decorationsUnder(tester, find.byType(DiffView)))
          if (d.color != null) d.color,
      ];
      expect(fills, containsAll([e.diffRemoved, e.diffAdded]));

      // Every diff line is mono: output is read for its characters (§3).
      final texts = tester.widgetList<Text>(
        find.descendant(of: find.byType(DiffView), matching: find.byType(Text)),
      );
      for (final text in texts.where((t) => t.data == 'three')) {
        expect(text.style?.fontFamily, Ember.monoFamily);
      }
    },
  );

  testWidgets('a diff with no lines says so', (tester) async {
    await pumpKit(tester, const DiffView(diff: '', emptyNote: 'Nothing here'));
    expect(find.text('Nothing here'), findsOneWidget);
    expect(find.byType(Box), findsNothing);
  });

  testWidgets('only a changed line takes a comment, and it names the line', (
    tester,
  ) async {
    final left = <DiffLine>[];
    await pumpKit(
      tester,
      DiffView(diff: diff, onComment: (line) => left.add(line)),
    );
    // Two body lines are commentable: the removed and the added one.
    final tips = tester
        .widgetList<Tip>(find.byType(Tip))
        .map((tip) => tip.message)
        .toList();
    expect(tips, ['Comment on line 2', 'Comment on line 2']);

    await tester.tap(find.byType(Tip).first);
    await tester.pumpAndSettle();
    expect(left.single.kind, DiffLineKind.removed);
    expect(left.single.side, DiffSide.before);
  });

  testWidgets('a diff nobody can comment on draws no control', (tester) async {
    await pumpKit(tester, const DiffView(diff: diff));
    expect(find.byType(Tip), findsNothing);
  });
}
