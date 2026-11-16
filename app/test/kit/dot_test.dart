import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  double opacity(WidgetTester tester) => tester
      .widget<FadeTransition>(
        find.descendant(
          of: find.byType(Dot),
          matching: find.byType(FadeTransition),
        ),
      )
      .opacity
      .value;

  testWidgets('each status: its colour, its shape, its word', (tester) async {
    for (final (status, colour, hollow) in [
      (Status.needsYou, e.warn, false),
      (Status.running, e.fg, false),
      (Status.ready, e.ok, false),
      (Status.failed, e.err, false),
      (Status.idle, e.faint, true),
    ]) {
      await pumpKit(tester, Dot(status));
      final d = decorationUnder(tester, find.byType(Dot));
      expect(d.shape, BoxShape.circle);
      if (hollow) {
        expect(d.color, isNull, reason: '$status is hollow');
        expect(borderColour(d), colour);
      } else {
        expect(d.color, colour, reason: '$status');
        expect(d.border, isNull);
      }
      expect(tester.getSize(find.byType(Dot)), const Size(8, 8));
      expect(hasTip(tester, find.byType(Container), status.word), isTrue);
    }
  });

  testWidgets('a fuller tooltip replaces the word', (tester) async {
    await pumpKit(tester, const Dot(Status.running, tooltip: 'Worker running'));
    expect(hasTip(tester, find.byType(Container), 'Worker running'), isTrue);
  });

  testWidgets('running pulses; nothing else moves', (tester) async {
    await pumpKit(tester, const Dot(Status.running));
    final start = opacity(tester);
    await tester.pump(const Duration(milliseconds: 550));
    expect(opacity(tester), lessThan(start));

    await pumpKit(tester, const Dot(Status.ready));
    await tester.pump(const Duration(milliseconds: 550));
    expect(opacity(tester), 1);
    expect(tester.hasRunningAnimations, isFalse);
  });

  testWidgets('reduced motion stops the pulse', (tester) async {
    await pumpKit(tester, const Dot(Status.running), reduceMotion: true);
    await tester.pump(const Duration(milliseconds: 550));
    expect(opacity(tester), 1);
    expect(tester.hasRunningAnimations, isFalse);
  });

  testWidgets('a dot that stops running stops pulsing', (tester) async {
    await pumpKit(tester, const Dot(Status.running));
    await tester.pump(const Duration(milliseconds: 300));
    await pumpKit(tester, const Dot(Status.ready));
    expect(opacity(tester), 1);
    expect(tester.hasRunningAnimations, isFalse);
  });
}
