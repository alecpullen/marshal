import 'package:flutter_test/flutter_test.dart';
import 'package:marshal_app/src/ui/debug/connection_check.dart';
import 'package:marshal_app/src/ui/kit/kit.dart';

/// The debug connection check: each host with its reach, its age and its
/// agents, and each §7 state it can be in.
void main() {
  final at = DateTime.utc(2026, 11, 17, 4, 12);
  final hosts = [
    HostRow(
      name: 'local',
      kind: 'local',
      reach: HostReachView.live,
      lastContact: DateTime.utc(2026, 11, 17, 4, 8),
      agentCount: 3,
    ),
    HostRow(
      name: 'studio',
      kind: 'server',
      reach: HostReachView.unreachable,
      lastContact: DateTime.utc(2026, 11, 17, 3, 55),
      agentCount: null,
    ),
  ];

  Future<void> pump(WidgetTester tester, Widget screen) =>
      tester.pumpWidget(EmberApp(home: screen));

  testWidgets('lists each host with its reach, its age and its agents', (
    tester,
  ) async {
    await pump(tester, ConnectionCheck(hosts: hosts, now: at));

    expect(find.text('local'), findsOneWidget);
    expect(find.text('studio'), findsOneWidget);
    expect(find.textContaining('live'), findsWidgets);
    expect(find.textContaining('unreachable'), findsWidgets);
    // Relative and short (§8): the clock the screen is given decides.
    expect(find.textContaining('4m'), findsOneWidget);
    expect(find.textContaining('17m'), findsOneWidget);
    expect(find.text('3 agents'), findsOneWidget);
    expect(find.text('agents unavailable'), findsOneWidget);
  });

  testWidgets('a stale host keeps its data and shows its age', (tester) async {
    await pump(
      tester,
      ConnectionCheck(hosts: hosts, state: StateKind.stale, now: at),
    );

    expect(find.textContaining('4m'), findsWidgets);
    expect(find.text('local'), findsOneWidget);
    expect(find.text('studio'), findsOneWidget);
  });

  testWidgets('an unavailable host says when it was last seen', (tester) async {
    await pump(
      tester,
      ConnectionCheck(hosts: hosts, state: StateKind.unavailable, now: at),
    );

    expect(find.textContaining('local'), findsOneWidget);
    expect(find.textContaining('last seen 4m'), findsOneWidget);
  });

  testWidgets('the loading state is a quiet layout, not the data', (
    tester,
  ) async {
    await pump(tester, const ConnectionCheck(state: StateKind.loading));

    expect(find.text('local'), findsNothing);
    expect(find.text('studio'), findsNothing);
  });
}
