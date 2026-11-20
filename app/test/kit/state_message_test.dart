import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

import '../support/kit.dart';

void main() {
  final e = tokens();

  Future<void> pump(WidgetTester tester, StateMessage message) =>
      pumpKit(tester, SizedBox(width: 480, child: message));

  testWidgets('loading: quiet placeholder lines, no spinner', (tester) async {
    await pump(tester, const StateMessage.loading(lines: 4));
    final bars = decorationsUnder(tester, find.byType(StateMessage));
    expect(bars, hasLength(4));
    expect(bars.map((b) => b.color), everyElement(e.border));
    expect(find.byType(Text), findsNothing);
    expect(tester.hasRunningAnimations, isFalse);
  });

  testWidgets('empty: one sentence and one action', (tester) async {
    var started = 0;
    await pump(
      tester,
      StateMessage.empty(
        message: 'Agents you start appear here.',
        action: StateAction('Start an agent', onTap: () => started++),
      ),
    );
    expect(find.text('Agents you start appear here.'), findsOneWidget);
    expect(find.byType(Btn), findsOneWidget);
    await tester.tap(find.text('Start an agent'));
    expect(started, 1);
  });

  testWidgets('stale: marked with its age, never as a failure', (tester) async {
    await pump(
      tester,
      const StateMessage.stale(age: '4m', detail: 'Reconnecting to studio-box'),
    );
    expect(find.text('Last updated 4m ago'), findsOneWidget);
    expect(find.text('Reconnecting to studio-box'), findsOneWidget);
    expect(tester.widget<Icon>(find.byType(Icon)).color, e.muted);
    expect(find.textContaining('fail'), findsNothing);
  });

  testWidgets('error: what failed, and one action to retry', (tester) async {
    var retried = 0;
    await pump(
      tester,
      StateMessage.error(
        message: 'The fleet could not be read.',
        retry: StateAction('Try again', onTap: () => retried++),
      ),
    );
    expect(find.text('The fleet could not be read.'), findsOneWidget);
    expect(tester.widget<Icon>(find.byType(Icon)).color, e.err);
    await tester.tap(find.text('Try again'));
    expect(retried, 1);
  });

  testWidgets('unavailable: the host, and when it was last seen', (
    tester,
  ) async {
    await pump(
      tester,
      StateMessage.unavailable(
        host: 'vps-syd',
        lastSeen: '4m',
        action: StateAction('Try again', onTap: () {}),
      ),
    );
    expect(find.text('vps-syd is unreachable'), findsOneWidget);
    expect(find.text('Last seen 4m ago'), findsOneWidget);
    expect(find.text('Try again'), findsOneWidget);
  });

  testWidgets('unauthorized: the host, signed out, and what clears it', (
    tester,
  ) async {
    await pump(tester, const StateMessage.unauthorized(host: 'studio-box'));
    expect(find.text('studio-box is signed out'), findsOneWidget);
    expect(find.text('Reload the page to sign in again.'), findsOneWidget);
    expect(
      tester.widget<Icon>(find.byType(Icon)).color,
      e.warn,
      reason: 'a refusal is marked in the warning tone, not as a failure',
    );
    expect(
      styleOf(tester, 'studio-box is signed out').color,
      e.fg,
      reason: 'the sentence itself reads as text, not as an alarm',
    );
    expect(
      find.byType(Btn),
      findsNothing,
      reason: 'no retry: only a page load clears a refusal (one-daemon D13)',
    );
  });

  testWidgets('unauthorized without a host says "This Studio"', (tester) async {
    await pump(tester, const StateMessage.unauthorized());
    expect(find.text('This Studio is signed out'), findsOneWidget);
    expect(find.text('Reload the page to sign in again.'), findsOneWidget);
  });

  testWidgets('planned: a disabled row saying so, never drawn as working', (
    tester,
  ) async {
    await pump(
      tester,
      const StateMessage.planned(
        name: 'Signal and Element',
        note: 'Notifications there are planned.',
      ),
    );
    expect(find.text('Planned'), findsOneWidget);
    expect(styleOf(tester, 'Signal and Element').color, e.muted);
    expect(find.byType(Btn), findsNothing);
  });

  testWidgets('planned keeps its pill beside its words in a tall box', (
    tester,
  ) async {
    await pumpKit(
      tester,
      const SizedBox(
        width: 400,
        height: 800,
        child: StateMessage.planned(
          name: 'Signal and Element',
          note: 'Notifications there are planned.',
        ),
      ),
      width: 400,
      height: 800,
    );
    final nameTop = tester.getTopLeft(find.text('Signal and Element')).dy;
    final pillTop = tester.getTopLeft(find.byType(Pill)).dy;
    expect(
      (pillTop - nameTop).abs(),
      lessThanOrEqualTo(Space.s8),
      reason: 'the pill belongs beside the name, not centred down the box',
    );
  });

  testWidgets('every kind has a constructor', (tester) async {
    final kinds = {
      const StateMessage.loading().kind,
      StateMessage.empty(
        message: 'x',
        action: StateAction('y', onTap: () {}),
      ).kind,
      const StateMessage.stale(age: '1m').kind,
      StateMessage.error(
        message: 'x',
        retry: StateAction('y', onTap: () {}),
      ).kind,
      const StateMessage.unavailable(host: 'h', lastSeen: '1m').kind,
      const StateMessage.unauthorized(host: 'h').kind,
      const StateMessage.planned(name: 'n', note: 'x').kind,
    };
    expect(kinds, StateKind.values.toSet());
  });
}
