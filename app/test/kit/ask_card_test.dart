import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  AskCard ask({
    AskKind kind = AskKind.decision,
    AskPlacement placement = AskPlacement.inbox,
    List<AskAction>? actions,
    VoidCallback? onOpenSource,
  }) => AskCard(
    kind: kind,
    title: kind == AskKind.approval
        ? 'go test ./internal/...'
        : 'Should recovery survive a daemon restart?',
    body: 'Persisting approvals widens the scope by one pull request.',
    waiting: '12m',
    waitingSince: 'Asked at 14:02',
    source: 'marshal › Reliable session recovery',
    onOpenSource: onOpenSource,
    placement: placement,
    actions:
        actions ??
        [
          AskAction('Keep to reconnects', onTap: () {}),
          AskAction('Expand scope', onTap: () {}),
          AskAction('Discuss', onTap: () {}),
        ],
  );

  Future<void> pump(WidgetTester tester, AskCard card) =>
      pumpKit(tester, SizedBox(width: 640, child: card));

  testWidgets('a blocking ask needs you: attention border, warn icon, word', (
    tester,
  ) async {
    await pump(tester, ask());
    expect(tester.widget<Box>(find.byType(Box)).tone, BoxTone.attention);
    expect(tester.widget<Icon>(find.byType(Icon).first).color, e.warn);
    expect(find.text('Decision'), findsOneWidget);
  });

  testWidgets('a review draft or intake is there to look at: plain border', (
    tester,
  ) async {
    for (final kind in [AskKind.review, AskKind.intake]) {
      await pump(tester, ask(kind: kind));
      expect(tester.widget<Box>(find.byType(Box)).tone, BoxTone.plain);
      expect(tester.widget<Icon>(find.byType(Icon).first).color, e.muted);
      expect(find.text(kind.word), findsOneWidget);
    }
  });

  testWidgets('the marked action is the one primary', (tester) async {
    var kept = 0;
    await pump(
      tester,
      ask(
        actions: [
          AskAction('Keep to reconnects', primary: true, onTap: () => kept++),
          AskAction('Expand scope', onTap: () {}),
          AskAction('Discuss', onTap: () {}),
        ],
      ),
    );
    final buttons = tester.widgetList<Btn>(find.byType(Btn)).toList();
    expect(buttons.map((b) => b.primary), [true, false, false]);
    await tester.tap(find.text('Keep to reconnects'));
    expect(kept, 1);
  });

  testWidgets('a card with no marked action has no primary', (tester) async {
    await pump(tester, ask());
    final buttons = tester.widgetList<Btn>(find.byType(Btn)).toList();
    expect(buttons.map((b) => b.primary), [false, false, false]);
  });

  testWidgets('an approval shows its command in mono', (tester) async {
    await pump(tester, ask(kind: AskKind.approval));
    expect(
      styleOf(tester, 'go test ./internal/...').fontFamily,
      Ember.monoFamily,
    );
  });

  testWidgets('placements: inbox shows all, overview drops the body, thread '
      'drops the source', (tester) async {
    const body = 'Persisting approvals widens the scope by one pull request.';
    const source = 'marshal › Reliable session recovery';

    await pump(tester, ask(kind: AskKind.question));
    expect(find.text(body), findsOneWidget);
    expect(find.text(source), findsOneWidget);

    await pump(
      tester,
      ask(kind: AskKind.question, placement: AskPlacement.overview),
    );
    expect(find.text(body), findsNothing);
    expect(find.text(source), findsOneWidget);

    // A decision keeps its body on the Overview: it cannot be answered
    // without it.
    await pump(tester, ask(placement: AskPlacement.overview));
    expect(find.text(body), findsOneWidget);

    await pump(tester, ask(placement: AskPlacement.thread));
    expect(find.text(source), findsNothing);
  });

  testWidgets('the source opens its thread; the wait has its exact time', (
    tester,
  ) async {
    var opened = 0;
    await pump(tester, ask(onOpenSource: () => opened++));
    await tester.tap(find.text('marshal › Reliable session recovery'));
    expect(opened, 1);
    expect(hasTip(tester, find.text('waiting 12m'), 'Asked at 14:02'), isTrue);
  });

  testWidgets('on a phone the actions wrap and nothing overflows', (
    tester,
  ) async {
    await pumpKit(tester, SizedBox(width: 340, child: ask()), width: 390);
    expect(tester.takeException(), isNull);
  });
}
